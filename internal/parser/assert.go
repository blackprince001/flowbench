package parser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/blackprince001/flowbench/internal/ir"
)

// The assertion expression grammar of the YAML surface:
//
//	<subject> <op> <value>
//
// subject: "status" | "latency" | "$<jsonpath>" (body) | "header.<Name>" |
// a variable name. op: == != < <= > >= contains matches. value: null, true,
// false, a number, a quoted string, or a bare word (kept as a string, so
// `latency < 800ms` works). "<var> != null" / "== null" compile to the IR's
// exists / not_exists.
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

// encodeScalar turns an expression's value token into canonical JSON:
// booleans and numbers stay typed; quoted and bare tokens become strings.
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
