package parser

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
)

func TestParseAssertion(t *testing.T) {
	cases := map[string]struct {
		expr string
		want ir.Assertion
	}{
		"status equality": {
			"status == 200",
			ir.Assertion{Source: ir.AssertStatus, Op: ir.OpEq, Value: json.RawMessage(`200`)},
		},
		"var exists via != null": {
			"token != null",
			ir.Assertion{Source: ir.AssertVar, Key: "token", Op: ir.OpExists},
		},
		"var absent via == null": {
			"legacy_id == null",
			ir.Assertion{Source: ir.AssertVar, Key: "legacy_id", Op: ir.OpNotExists},
		},
		"latency bound keeps duration as string": {
			"latency < 800ms",
			ir.Assertion{Source: ir.AssertLatency, Op: ir.OpLt, Value: json.RawMessage(`"800ms"`)},
		},
		"body jsonpath subject": {
			"$.data.id >= 5",
			ir.Assertion{Source: ir.AssertBody, Key: "$.data.id", Op: ir.OpGte, Value: json.RawMessage(`5`)},
		},
		"header contains": {
			"header.Content-Type contains json",
			ir.Assertion{Source: ir.AssertHeader, Key: "Content-Type", Op: ir.OpContains, Value: json.RawMessage(`"json"`)},
		},
		"quoted string value": {
			`$.data.state == "PAID"`,
			ir.Assertion{Source: ir.AssertBody, Key: "$.data.state", Op: ir.OpEq, Value: json.RawMessage(`"PAID"`)},
		},
		"matches with regex value": {
			`order_id matches '^ord-\d+$'`,
			ir.Assertion{Source: ir.AssertVar, Key: "order_id", Op: ir.OpMatches, Value: json.RawMessage(`"^ord-\\d+$"`)},
		},
		"boolean value stays typed": {
			"$.data.active == true",
			ir.Assertion{Source: ir.AssertBody, Key: "$.data.active", Op: ir.OpEq, Value: json.RawMessage(`true`)},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseAssertion(tc.expr)
			if err != nil {
				t.Fatalf("parseAssertion(%q): %v", tc.expr, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAssertion(%q)\n got: %+v\nwant: %+v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestParseAssertionRejects(t *testing.T) {
	for name, expr := range map[string]string{
		"no operator":         "status",
		"unknown operator":    "status equals 200",
		"null with less-than": "token < null",
		"missing value":       "status ==",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAssertion(expr); err == nil {
				t.Errorf("parseAssertion(%q) should fail", expr)
			}
		})
	}
}
