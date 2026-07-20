package eval_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/eval"
	"github.com/blackprince001/flowbench/internal/ir"
)

type fakeTarget struct {
	status  int
	headers http.Header
	body    []byte
	latency time.Duration
}

func (f fakeTarget) Status() int { return f.status }
func (f fakeTarget) Header(name string) (string, bool) {
	if f.headers == nil {
		return "", false
	}
	if _, ok := f.headers[http.CanonicalHeaderKey(name)]; !ok {
		return "", false
	}
	return f.headers.Get(name), true
}
func (f fakeTarget) Body() []byte           { return f.body }
func (f fakeTarget) Latency() time.Duration { return f.latency }

func val(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestExtractBodyPaths(t *testing.T) {
	tgt := fakeTarget{body: []byte(`{"data":{"access_token":"tok-9","items":[{"id":"a"},{"id":"b"}]}}`)}
	cases := map[string]struct {
		path  string
		want  any
		found bool
	}{
		"nested key":  {"$.data.access_token", "tok-9", true},
		"array index": {"$.data.items[1].id", "b", true},
		"bracket key": {"$['data']['access_token']", "tok-9", true},
		"missing key": {"$.data.nope", nil, false},
		"index oob":   {"$.data.items[9].id", nil, false},
		"whole root":  {"$", nil, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, found, err := eval.Extract(ir.Extraction{Path: c.path}, tgt)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if found != c.found {
				t.Fatalf("found = %v, want %v", found, c.found)
			}
			if c.found && c.want != nil && got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestExtractHeaderAndStatus(t *testing.T) {
	tgt := fakeTarget{status: 201, headers: http.Header{"X-Request-Id": {"req-7"}}}

	got, found, err := eval.Extract(ir.Extraction{From: ir.ExtractHeader, Path: "x-request-id"}, tgt)
	if err != nil || !found || got != "req-7" {
		t.Errorf("header extract = %v %v %v", got, found, err)
	}
	got, found, _ = eval.Extract(ir.Extraction{From: ir.ExtractStatus}, tgt)
	if !found || got != 201 {
		t.Errorf("status extract = %v %v", got, found)
	}
}

func TestAssertStatusAndVar(t *testing.T) {
	tgt := fakeTarget{status: 200}
	vars := func(name string) (any, bool) {
		if name == "token" {
			return "tok-9", true
		}
		return nil, false
	}
	cases := []struct {
		name string
		a    ir.Assertion
		pass bool
	}{
		{"status eq ok", ir.Assertion{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}, true},
		{"status eq bad", ir.Assertion{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(500)}, false},
		{"status lt", ir.Assertion{Source: ir.AssertStatus, Op: ir.OpLt, Value: val(300)}, true},
		{"var exists", ir.Assertion{Source: ir.AssertVar, Key: "token", Op: ir.OpExists}, true},
		{"missing var exists", ir.Assertion{Source: ir.AssertVar, Key: "nope", Op: ir.OpExists}, false},
		{"missing var not_exists", ir.Assertion{Source: ir.AssertVar, Key: "nope", Op: ir.OpNotExists}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := eval.Assert(c.a, tgt, vars)
			if err != nil {
				t.Fatalf("Assert: %v", err)
			}
			if res.Pass != c.pass {
				t.Errorf("pass = %v (%s), want %v", res.Pass, res.Detail, c.pass)
			}
		})
	}
}

func TestAssertLatencyAndBody(t *testing.T) {
	tgt := fakeTarget{
		latency: 120 * time.Millisecond,
		body:    []byte(`{"count":3,"name":"widget","tags":["red","blue"]}`),
	}
	nilVars := func(string) (any, bool) { return nil, false }
	cases := []struct {
		name string
		a    ir.Assertion
		pass bool
	}{
		{"latency under", ir.Assertion{Source: ir.AssertLatency, Op: ir.OpLt, Value: val("800ms")}, true},
		{"latency over", ir.Assertion{Source: ir.AssertLatency, Op: ir.OpLt, Value: val("50ms")}, false},
		{"body num eq", ir.Assertion{Source: ir.AssertBody, Key: "$.count", Op: ir.OpEq, Value: val(3)}, true},
		{"body contains", ir.Assertion{Source: ir.AssertBody, Key: "$.name", Op: ir.OpContains, Value: val("idge")}, true},
		{"body matches", ir.Assertion{Source: ir.AssertBody, Key: "$.name", Op: ir.OpMatches, Value: val("^wid")}, true},
		{"array contains", ir.Assertion{Source: ir.AssertBody, Key: "$.tags", Op: ir.OpContains, Value: val("blue")}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := eval.Assert(c.a, tgt, nilVars)
			if err != nil {
				t.Fatalf("Assert: %v", err)
			}
			if res.Pass != c.pass {
				t.Errorf("pass = %v (%s), want %v", res.Pass, res.Detail, c.pass)
			}
		})
	}
}
