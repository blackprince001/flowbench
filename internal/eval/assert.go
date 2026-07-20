package eval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
)

// Target is the response surface assertions and extractions read. Adapters
// build one per call so eval never depends on a specific protocol.
type Target interface {
	Status() int
	Header(name string) (string, bool)
	Body() []byte
	Latency() time.Duration
}

// VarLookup resolves a variable extracted earlier in the same iteration.
type VarLookup func(name string) (any, bool)

// Result is the verdict on one assertion; Detail explains a failure.
type Result struct {
	Pass   bool
	Detail string
}

func fail(format string, args ...any) Result {
	return Result{Detail: fmt.Sprintf(format, args...)}
}

// Assert evaluates one assertion against the target.
func Assert(a ir.Assertion, t Target, vars VarLookup) (Result, error) {
	actual, exists, err := actualValue(a, t, vars)
	if err != nil {
		return Result{}, err
	}

	switch a.Op {
	case ir.OpExists:
		if exists {
			return Result{Pass: true}, nil
		}
		return fail("%s does not exist", subject(a)), nil
	case ir.OpNotExists:
		if !exists {
			return Result{Pass: true}, nil
		}
		return fail("%s exists", subject(a)), nil
	}

	if !exists {
		return fail("%s does not exist", subject(a)), nil
	}

	if a.Source == ir.AssertLatency {
		return compareLatency(a.Op, actual.(time.Duration), a.Value)
	}

	expected, err := decodeValue(a.Value)
	if err != nil {
		return Result{}, err
	}
	ok, err := compare(a.Op, actual, expected)
	if err != nil {
		return Result{}, err
	}
	if ok {
		return Result{Pass: true}, nil
	}
	return fail("%s: %v %s %v", subject(a), actual, a.Op, expected), nil
}

func actualValue(a ir.Assertion, t Target, vars VarLookup) (any, bool, error) {
	switch a.Source {
	case ir.AssertStatus:
		return t.Status(), true, nil
	case ir.AssertLatency:
		return t.Latency(), true, nil
	case ir.AssertHeader:
		v, ok := t.Header(a.Key)
		return v, ok, nil
	case ir.AssertBody:
		return queryJSON(t.Body(), a.Key)
	case ir.AssertVar:
		v, ok := vars(a.Key)
		return v, ok, nil
	default:
		return nil, false, fmt.Errorf("unknown assertion source %q", a.Source)
	}
}

func compareLatency(op ir.AssertionOp, actual time.Duration, raw json.RawMessage) (Result, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return Result{}, fmt.Errorf("latency bound %s must be a duration string: %w", raw, err)
	}
	want, err := time.ParseDuration(s)
	if err != nil {
		return Result{}, fmt.Errorf("latency bound %q is not a duration: %w", s, err)
	}
	ok, err := numericCompare(op, float64(actual), float64(want))
	if err != nil {
		return Result{}, err
	}
	if ok {
		return Result{Pass: true}, nil
	}
	return fail("latency %s %s %s", actual, op, want), nil
}

func compare(op ir.AssertionOp, actual, expected any) (bool, error) {
	switch op {
	case ir.OpEq:
		return valuesEqual(actual, expected), nil
	case ir.OpNe:
		return !valuesEqual(actual, expected), nil
	case ir.OpLt, ir.OpLte, ir.OpGt, ir.OpGte:
		af, aok := toFloat(actual)
		ef, eok := toFloat(expected)
		if !aok || !eok {
			return false, fmt.Errorf("%s needs numeric operands, got %v and %v", op, actual, expected)
		}
		return numericCompare(op, af, ef)
	case ir.OpContains:
		return contains(actual, expected)
	case ir.OpMatches:
		as, aok := actual.(string)
		es, eok := expected.(string)
		if !aok || !eok {
			return false, fmt.Errorf("matches needs string operands, got %T and %T", actual, expected)
		}
		re, err := regexp.Compile(es)
		if err != nil {
			return false, fmt.Errorf("matches pattern %q is invalid: %w", es, err)
		}
		return re.MatchString(as), nil
	default:
		return false, fmt.Errorf("unsupported operator %q", op)
	}
}

func numericCompare(op ir.AssertionOp, a, b float64) (bool, error) {
	switch op {
	case ir.OpLt:
		return a < b, nil
	case ir.OpLte:
		return a <= b, nil
	case ir.OpGt:
		return a > b, nil
	case ir.OpGte:
		return a >= b, nil
	}
	return false, fmt.Errorf("%q is not a numeric operator", op)
}

func contains(actual, expected any) (bool, error) {
	switch v := actual.(type) {
	case string:
		es, ok := expected.(string)
		if !ok {
			return false, fmt.Errorf("contains on a string needs a string operand")
		}
		return strings.Contains(v, es), nil
	case []any:
		for _, el := range v {
			if valuesEqual(el, expected) {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("contains needs a string or array subject, got %T", actual)
	}
}

func decodeValue(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("assertion value %s is not valid JSON: %w", raw, err)
	}
	return v, nil
}

func valuesEqual(a, b any) bool {
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
		return false
	}
	return reflect.DeepEqual(a, b)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func subject(a ir.Assertion) string {
	switch a.Source {
	case ir.AssertHeader:
		return "header " + a.Key
	case ir.AssertBody:
		return a.Key
	case ir.AssertVar:
		return "var " + a.Key
	default:
		return string(a.Source)
	}
}
