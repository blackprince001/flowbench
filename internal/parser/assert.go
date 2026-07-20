package parser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/blackprince001/flowbench/internal/ir"
)

// grammar: <subject> <op> <value>, e.g. "status == 200", "latency < 800ms".
var exprRe = regexp.MustCompile(`^(\S+)\s+(==|!=|<=|>=|<|>|contains|matches)\s+(.+)$`)

var numberRe = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][+-]?\d+)?$`)

func parseAssertion(expr string) (ir.Assertion, error) {
	m := exprRe.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return ir.Assertion{}, fmt.Errorf(
			"cannot parse assertion %q: expected <subject> <op> <value>, e.g. \"status == 200\"", expr)
	}
	subject, op, rawValue := m[1], m[2], strings.TrimSpace(m[3])

	a := ir.Assertion{}
	switch {
	case subject == "status":
		a.Source = ir.AssertStatus
	case subject == "latency":
		a.Source = ir.AssertLatency
	case strings.HasPrefix(subject, "$"):
		a.Source, a.Key = ir.AssertBody, subject
	case strings.HasPrefix(subject, "header."):
		a.Source, a.Key = ir.AssertHeader, strings.TrimPrefix(subject, "header.")
	default:
		a.Source, a.Key = ir.AssertVar, subject
	}

	if rawValue == "null" {
		switch op {
		case "!=":
			a.Op = ir.OpExists
		case "==":
			a.Op = ir.OpNotExists
		default:
			return ir.Assertion{}, fmt.Errorf("null comparisons support only == and !=, got %q", op)
		}
		return a, nil
	}

	ops := map[string]ir.AssertionOp{
		"==": ir.OpEq, "!=": ir.OpNe,
		"<": ir.OpLt, "<=": ir.OpLte, ">": ir.OpGt, ">=": ir.OpGte,
		"contains": ir.OpContains, "matches": ir.OpMatches,
	}
	a.Op = ops[op]
	a.Value = encodeScalar(rawValue)
	return a, nil
}

func encodeScalar(raw string) json.RawMessage {
	if raw == "true" || raw == "false" || numberRe.MatchString(raw) {
		return json.RawMessage(raw)
	}
	if len(raw) >= 2 {
		if (raw[0] == '\'' && raw[len(raw)-1] == '\'') || (raw[0] == '"' && raw[len(raw)-1] == '"') {
			raw = raw[1 : len(raw)-1]
		}
	}
	b, _ := json.Marshal(raw)
	return b
}
