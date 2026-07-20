package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Scope holds one iteration's variable roots: the process environment, the
// flow's bound data-pool row (addressed as <pool>.<field>), and values
// extracted from earlier steps (addressed by flat name).
type Scope struct {
	pool string
	row  map[string]string
	vars map[string]any
	env  func(string) (string, bool)
}

func NewScope(pool string, row map[string]string) *Scope {
	return &Scope{pool: pool, row: row, vars: map[string]any{}, env: os.LookupEnv}
}

// Set records a value extracted from a step's response.
func (s *Scope) Set(name string, v any) { s.vars[name] = v }

// Lookup returns an extracted variable, for var assertions.
func (s *Scope) Lookup(name string) (any, bool) {
	v, ok := s.vars[name]
	return v, ok
}

// Resolve turns a template reference into its string form for injection.
func (s *Scope) Resolve(ref string) (string, error) {
	root, rest, hasDot := strings.Cut(ref, ".")
	switch {
	case root == "env":
		if !hasDot {
			return "", fmt.Errorf("env reference needs a variable name")
		}
		v, ok := s.env(rest)
		if !ok {
			return "", fmt.Errorf("environment variable %q is not set", rest)
		}
		return v, nil
	case s.pool != "" && root == s.pool:
		v, ok := s.row[rest]
		if !ok {
			return "", fmt.Errorf("data row has no field %q", rest)
		}
		return v, nil
	default:
		v, ok := s.vars[ref]
		if !ok {
			return "", fmt.Errorf("variable %q has no value yet", ref)
		}
		return stringify(v)
	}
}

func stringify(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(x), nil
	case nil:
		return "", fmt.Errorf("value is null")
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
