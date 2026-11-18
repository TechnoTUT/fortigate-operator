package fortigate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recorded struct {
	method, rawPath, query, auth, body string
}

func server(t *testing.T, status int, reply string) (*httptest.Server, *[]recorded) {
	t.Helper()
	var got []recorded
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, recorded{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), string(b)})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(s.Close)
	return s, &got
}

func newClient(t *testing.T, url string, mod func(*Config)) *Client {
	t.Helper()
	cfg := Config{BaseURL: url, Token: "secret", VDOM: "root", InsecureSkipVerify: true}
	if mod != nil {
		mod(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetEscapesNames(t *testing.T) {
	s, got := server(t, 200, `{"status":"success","results":[{"name":"VLAN10 subnet/x","subnet":"10.10.0.0 255.255.0.0"}]}`)
	c := newClient(t, s.URL, nil)
	o, err := c.Get(context.Background(), "firewall/address", "VLAN10 subnet/x")
	if err != nil {
		t.Fatal(err)
	}
	if o["subnet"] != "10.10.0.0 255.255.0.0" {
		t.Errorf("got %v", o)
	}
	r := (*got)[0]
	if r.rawPath != "/api/v2/cmdb/firewall/address/VLAN10%20subnet%2Fx" {
		t.Errorf("path = %s", r.rawPath)
	}
	if r.query != "vdom=root" || r.auth != "Bearer secret" {
		t.Errorf("query = %q auth = %q", r.query, r.auth)
	}
}

func TestTokenInQuery(t *testing.T) {
	s, got := server(t, 200, `{"status":"success","results":[]}`)
	c := newClient(t, s.URL, func(c *Config) { c.TokenInQuery = true })
	if _, err := c.List(context.Background(), "firewall/policy"); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.auth != "" || !strings.Contains(r.query, "access_token=secret") {
		t.Errorf("query = %q auth = %q", r.query, r.auth)
	}
}

func TestNotFound(t *testing.T) {
	s, _ := server(t, 404, `{"http_method":"GET","status":"error","http_status":404,"error":-3}`)
	c := newClient(t, s.URL, nil)
	_, err := c.Get(context.Background(), "firewall/address", "missing")
	if !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "entry not found") {
		t.Errorf("error lacks hint: %v", err)
	}
}

func TestCreateReturnsMKey(t *testing.T) {
	s, got := server(t, 200, `{"http_method":"POST","status":"success","http_status":200,"mkey":28}`)
	c := newClient(t, s.URL, nil)
	mkey, err := c.Create(context.Background(), "firewall/policy", Object{"policyid": 0, "name": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if mkey != "28" {
		t.Errorf("mkey = %q", mkey)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte((*got)[0].body), &body)
	if body["name"] != "x" || (*got)[0].method != http.MethodPost {
		t.Errorf("request = %+v", (*got)[0])
	}
}

func TestErrorWithCLIMessage(t *testing.T) {
	s, _ := server(t, 500, `{"status":"error","http_status":500,"error":-651,"cli_error":"node_check_object fail! for srcaddr nope"}`)
	c := newClient(t, s.URL, nil)
	err := c.Update(context.Background(), "firewall/policy", "3", Object{"srcaddr": []any{map[string]any{"name": "nope"}}})
	if err == nil || !strings.Contains(err.Error(), "srcaddr nope") || IsNotFound(err) {
		t.Errorf("err = %v", err)
	}
}

func TestMove(t *testing.T) {
	s, got := server(t, 200, `{"status":"success"}`)
	c := newClient(t, s.URL, nil)
	if err := c.Move(context.Background(), "firewall/policy", "28", "before", "3"); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.method != http.MethodPut || r.rawPath != "/api/v2/cmdb/firewall/policy/28" || r.query != "action=move&before=3&vdom=root" {
		t.Errorf("request = %+v", r)
	}
	if err := c.Move(context.Background(), "firewall/policy", "28", "up", "3"); err == nil {
		t.Error("invalid direction accepted")
	}
}

func TestErrorDoesNotLeakToken(t *testing.T) {
	c := newClient(t, "https://127.0.0.1:1", func(c *Config) { c.TokenInQuery = true })
	_, err := c.List(context.Background(), "firewall/policy")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v", err)
	}
}
