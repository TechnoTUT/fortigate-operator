// Package fortigate is a small client for the FortiOS REST API configuration endpoints (/api/v2/cmdb).
package fortigate

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Object is one table entry as returned by the API.
type Object = map[string]any

// Config configures a Client.
type Config struct {
	BaseURL            string // for example https://192.168.99.129
	Token              string // API key of the REST API administrator (config system api-user)
	VDOM               string // omitted from requests when empty
	TokenInQuery       bool   // send the key as the access_token query parameter (for versions that reject the Authorization header)
	CAFile             string // CA bundle (PEM) used to verify the FortiGate certificate
	InsecureSkipVerify bool
	Timeout            time.Duration
}

// Client is a FortiOS REST API client.
type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client
}

// New creates a Client.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid FortiGate URL: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid FortiGate URL: %q", cfg.BaseURL)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("FortiGate API key is not set")
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // only when explicitly requested
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates in CA file: %s", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	return &Client{cfg: cfg, base: base, http: &http.Client{Transport: tr, Timeout: timeout}}, nil
}

// APIError is returned when the FortiGate reports a failure.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = describeCode(e.Code)
	}
	return fmt.Sprintf("%s %s: HTTP %d (error %d): %s", e.Method, e.Path, e.StatusCode, e.Code, msg)
}

// IsNotFound reports whether err means that the requested entry does not exist.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusNotFound
}

// describeCode gives a hint for common FortiOS error codes, which often come without a message.
func describeCode(code int) string {
	switch code {
	case -3:
		return "entry not found (a referenced object may not exist)"
	case -5:
		return "an object with the same name already exists"
	case -23:
		return "the object is still referenced by other objects"
	case -37:
		return "permission denied"
	case -651:
		return "invalid value"
	default:
		return ""
	}
}

type response struct {
	Status     string          `json:"status"`
	HTTPStatus int             `json:"http_status"`
	Results    json.RawMessage `json:"results"`
	MKey       json.RawMessage `json:"mkey"`
	Error      int             `json:"error"`
	CLIError   string          `json:"cli_error"`
}

func (c *Client) url(path, mkey string, extra url.Values) string {
	p := c.base.Path + "/api/v2/cmdb/" + path
	raw := p
	if mkey != "" {
		p += "/" + mkey
		raw += "/" + url.PathEscape(mkey)
	}
	u := *c.base
	u.Path = p
	u.RawPath = raw
	q := url.Values{}
	for k, v := range extra {
		q[k] = v
	}
	if c.cfg.VDOM != "" {
		q.Set("vdom", c.cfg.VDOM)
	}
	if c.cfg.TokenInQuery {
		q.Set("access_token", c.cfg.Token)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) do(ctx context.Context, method, path, mkey string, query url.Values, body any) (*response, error) {
	label := path
	if mkey != "" {
		label += "/" + mkey
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path, mkey, query), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !c.cfg.TokenInQuery {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		// The URL may contain access_token, so do not include the url.Error URL.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("%s %s: %w", method, label, ue.Err)
		}
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var r response
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &r); err != nil && res.StatusCode < 300 {
			return nil, fmt.Errorf("%s %s: cannot parse response: %w", method, label, err)
		}
	}
	if res.StatusCode >= 300 || (r.Status != "" && r.Status != "success") {
		msg := r.CLIError
		if msg == "" && r.Status == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		code := res.StatusCode
		if code < 300 && r.HTTPStatus >= 300 {
			code = r.HTTPStatus
		}
		return nil, &APIError{Method: method, Path: label, StatusCode: code, Code: r.Error, Message: msg}
	}
	return &r, nil
}

func decodeResults(raw json.RawMessage) ([]Object, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []Object
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	// Some versions return an object instead of an array when a single mkey is requested.
	var one Object
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, fmt.Errorf("cannot parse results: %w", err)
	}
	return []Object{one}, nil
}

// List returns every entry of a table, in the order the FortiGate keeps them
// (for firewall/policy, the order in which policies are evaluated).
func (c *Client) List(ctx context.Context, path string) ([]Object, error) {
	r, err := c.do(ctx, http.MethodGet, path, "", nil, nil)
	if err != nil {
		return nil, err
	}
	list, err := decodeResults(r.Results)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	return list, nil
}

// Get returns one entry. Use IsNotFound to check for a missing entry.
func (c *Client) Get(ctx context.Context, path, mkey string) (Object, error) {
	r, err := c.do(ctx, http.MethodGet, path, mkey, nil, nil)
	if err != nil {
		return nil, err
	}
	list, err := decodeResults(r.Results)
	if err != nil {
		return nil, fmt.Errorf("GET %s/%s: %w", path, mkey, err)
	}
	if len(list) != 1 {
		return nil, &APIError{Method: http.MethodGet, Path: path + "/" + mkey, StatusCode: http.StatusNotFound, Message: fmt.Sprintf("got %d entries", len(list))}
	}
	return list[0], nil
}

// Create adds an entry and returns its key as reported by the FortiGate.
func (c *Client) Create(ctx context.Context, path string, body Object) (string, error) {
	r, err := c.do(ctx, http.MethodPost, path, "", nil, body)
	if err != nil {
		return "", err
	}
	return mkeyString(r.MKey), nil
}

// Update changes the given fields of an entry. Fields not in body are left unchanged.
func (c *Client) Update(ctx context.Context, path, mkey string, body Object) error {
	_, err := c.do(ctx, http.MethodPut, path, mkey, nil, body)
	return err
}

// Delete removes an entry.
func (c *Client) Delete(ctx context.Context, path, mkey string) error {
	_, err := c.do(ctx, http.MethodDelete, path, mkey, nil, nil)
	return err
}

// Move moves an entry of an ordered table (such as firewall/policy) directly before or after another one.
// where is "before" or "after"; ref is the key of the other entry.
func (c *Client) Move(ctx context.Context, path, mkey, where, ref string) error {
	if where != "before" && where != "after" {
		return fmt.Errorf("invalid move direction %q", where)
	}
	q := url.Values{"action": {"move"}, where: {ref}}
	_, err := c.do(ctx, http.MethodPut, path, mkey, q, nil)
	return err
}

func mkeyString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}
