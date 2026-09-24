package executor

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/blackprince001/flowbench/internal/secret"
)

// Scope holds one iteration's variable roots: the process environment, the
// flow's bound data-pool row (addressed as <pool>.<field>), and values
// extracted from earlier steps (addressed by flat name).
type Scope struct {
	pool    string
	row     map[string]string
	vars    map[string]any
	env     func(string) (string, bool)
	secrets *secret.Set
}

func NewScope(pool string, row map[string]string) *Scope {
	return &Scope{pool: pool, row: row, vars: map[string]any{}, env: os.LookupEnv, secrets: secret.NewSet()}
}

// Child returns the scope a used flow runs in: the same env and secret set,
// so every env value it resolves is still redacted, but no data row and none
// of the caller's variables. Values reach it only as inputs.
func (s *Scope) Child() *Scope {
	return &Scope{vars: map[string]any{}, env: s.env, secrets: s.secrets}
}

// Secrets is the set of env-sourced values that must be scrubbed from any
// captured artifact before it reaches storage.
func (s *Scope) Secrets() *secret.Set { return s.secrets }

// Set records a value extracted from a step's response.
func (s *Scope) Set(name string, v any) { s.vars[name] = v }

// Lookup returns an extracted variable, for var assertions.
func (s *Scope) Lookup(name string) (any, bool) {
	v, ok := s.vars[name]
	return v, ok
}

// Resolve turns a template reference into its string form for injection.
func (s *Scope) Resolve(ref string) (string, error) {
	v, err := s.value(ref)
	if err != nil {
		return "", err
	}
	return stringify(v)
}

// ResolveJSON gives a reference's value as JSON with its type intact, for a
// JSON payload whose value is exactly one template. Env values and data-pool
// fields are always strings — a CSV cell "007" stays "007" — while an
// extracted value keeps the type it had in the response, null included.
func (s *Scope) ResolveJSON(ref string) (json.RawMessage, error) {
	v, err := s.value(ref)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// value looks a reference up in the scope's roots.
func (s *Scope) value(ref string) (any, error) {
	root, rest, hasDot := strings.Cut(ref, ".")
	switch {
	case root == "env":
		if !hasDot {
			return nil, fmt.Errorf("env reference needs a variable name")
		}
		v, ok := s.env(rest)
		if !ok {
			return nil, fmt.Errorf("environment variable %q is not set", rest)
		}
		s.secrets.Add(v)
		return v, nil
	case s.pool != "" && root == s.pool:
		v, ok := s.row[rest]
		if !ok {
			return nil, fmt.Errorf("data row has no field %q", rest)
		}
		return v, nil
	default:
		v, ok := s.vars[ref]
		if !ok {
			return nil, fmt.Errorf("variable %q has no value yet", ref)
		}
		return v, nil
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
