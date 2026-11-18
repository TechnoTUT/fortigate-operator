package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"

	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

// fakeFG is an in-memory FortiGate. Tables keep their entries in order.
// firewall/policy is keyed by policyid, other tables by name.
type fakeFG struct {
	mu     sync.Mutex
	tables map[string][]fortigate.Object
	nextID int
	writes []string
}

func newFakeFG() *fakeFG { return &fakeFG{tables: map[string][]fortigate.Object{}, nextID: 1} }

func keyField(path string) string {
	if path == policyPath {
		return "policyid"
	}
	return "name"
}

// roundTrip makes v look like decoded API JSON (numbers as float64, lists as []any).
func roundTrip(v fortigate.Object) fortigate.Object {
	b, _ := json.Marshal(v)
	var out fortigate.Object
	_ = json.Unmarshal(b, &out)
	return out
}

func notFound(path, mkey string) error {
	return &fortigate.APIError{Method: "GET", Path: path + "/" + mkey, StatusCode: http.StatusNotFound, Code: -3}
}

func (f *fakeFG) index(path, mkey string) int {
	k := keyField(path)
	for i, o := range f.tables[path] {
		if scalarString(o[k]) == mkey {
			return i
		}
	}
	return -1
}

func (f *fakeFG) put(path string, o fortigate.Object) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o = roundTrip(o)
	if path == policyPath {
		id, _ := strconv.Atoi(scalarString(o["policyid"]))
		if id >= f.nextID {
			f.nextID = id + 1
		}
	}
	f.tables[path] = append(f.tables[path], o)
}

func (f *fakeFG) get(path, mkey string) fortigate.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.index(path, mkey); i >= 0 {
		return f.tables[path][i]
	}
	return nil
}

func (f *fakeFG) names(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, o := range f.tables[path] {
		out = append(out, scalarString(o["name"]))
	}
	return out
}

func (f *fakeFG) List(_ context.Context, path string) ([]fortigate.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fortigate.Object, 0, len(f.tables[path]))
	for _, o := range f.tables[path] {
		out = append(out, roundTrip(o))
	}
	return out, nil
}

func (f *fakeFG) Get(_ context.Context, path, mkey string) (fortigate.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(path, mkey)
	if i < 0 {
		return nil, notFound(path, mkey)
	}
	return roundTrip(f.tables[path][i]), nil
}

func (f *fakeFG) Create(_ context.Context, path string, body fortigate.Object) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := roundTrip(body)
	var mkey string
	if path == policyPath {
		for _, e := range f.tables[path] {
			if e["name"] == o["name"] {
				return "", &fortigate.APIError{Method: "POST", Path: path, StatusCode: 500, Code: -5}
			}
		}
		o["policyid"] = float64(f.nextID)
		mkey = strconv.Itoa(f.nextID)
		f.nextID++
	} else {
		mkey = scalarString(o["name"])
		if f.index(path, mkey) >= 0 {
			return "", &fortigate.APIError{Method: "POST", Path: path, StatusCode: 500, Code: -5}
		}
	}
	f.tables[path] = append(f.tables[path], o)
	f.writes = append(f.writes, "create "+path+"/"+mkey)
	return mkey, nil
}

func (f *fakeFG) Update(_ context.Context, path, mkey string, body fortigate.Object) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(path, mkey)
	if i < 0 {
		return notFound(path, mkey)
	}
	for k, v := range roundTrip(body) {
		f.tables[path][i][k] = v
	}
	f.writes = append(f.writes, "update "+path+"/"+mkey)
	return nil
}

func (f *fakeFG) Delete(_ context.Context, path, mkey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(path, mkey)
	if i < 0 {
		return notFound(path, mkey)
	}
	f.tables[path] = slices.Delete(f.tables[path], i, i+1)
	f.writes = append(f.writes, "delete "+path+"/"+mkey)
	return nil
}

func (f *fakeFG) Move(_ context.Context, path, mkey, where, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.index(path, mkey)
	if i < 0 {
		return notFound(path, mkey)
	}
	o := f.tables[path][i]
	f.tables[path] = slices.Delete(f.tables[path], i, i+1)
	j := f.index(path, ref)
	if j < 0 {
		return notFound(path, ref)
	}
	if where == "after" {
		j++
	}
	f.tables[path] = slices.Insert(f.tables[path], j, o)
	f.writes = append(f.writes, fmt.Sprintf("move %s/%s %s %s", path, mkey, where, ref))
	return nil
}

func (f *fakeFG) takeWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.writes
	f.writes = nil
	return w
}
