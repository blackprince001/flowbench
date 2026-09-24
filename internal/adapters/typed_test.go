package adapters_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/ir"
)

type typedVars map[string]any

func (v typedVars) Resolve(ref string) (string, error) {
	x, ok := v[ref]
	if !ok {
		return "", errors.New("unknown ref")
	}
	b, _ := json.Marshal(x)
	if s, ok := x.(string); ok {
		return s, nil
	}
	return string(b), nil
}

func (v typedVars) ResolveJSON(ref string) (json.RawMessage, error) {
	x, ok := v[ref]
	if !ok {
		return nil, errors.New("unknown ref")
	}
	return json.Marshal(x)
}

func TestBodyEscapesAndTypesAreBothRight(t *testing.T) {
	vars := typedVars{"n": 42.0, "q": `say "hi"`, "list": []any{1.0}}
	cases := []struct{ body, want string }{
		{`{"a":"{{ n }}"}`, `{"a":42}`},
		{`{"a":"{{ list }}"}`, `{"a":[1]}`},
		{`{"a":"{{ q }}"}`, `{"a":"say \"hi\""}`},
		{`{"a":"n={{ n }}"}`, `{"a":"n=42"}`},
		// An escaped quote before a template does not start a new string.
		{`{"a":"x\"{{ n }}"}`, `{"a":"x\"42"}`},
		{`{"{{ n }}":"{{ n }}"}`, `{"42":42}`},
		{`["{{ n }}", "{{ list }}"]`, `[42, [1]]`},
	}
	for _, tc := range cases {
		req, err := adapters.BuildRequest(&ir.CallSpec{Method: "POST", URL: "/x", Body: json.RawMessage(tc.body)}, vars)
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if string(req.Body) != tc.want {
			t.Errorf("%s → %s, want %s", tc.body, req.Body, tc.want)
		}
		if !json.Valid(req.Body) {
			t.Errorf("%s produced invalid JSON %s", tc.body, req.Body)
		}
	}
}

// A resolver with no typed lookup keeps the old behavior: text in a string.
func TestPlainResolverStillInjectsText(t *testing.T) {
	plain := adapters.ResolverFunc(func(string) (string, error) { return "42", nil })
	req, err := adapters.BuildRequest(&ir.CallSpec{Method: "POST", URL: "/x", Body: json.RawMessage(`{"a":"{{ n }}"}`)}, plain)
	if err != nil {
		t.Fatal(err)
	}
	if string(req.Body) != `{"a":"42"}` {
		t.Errorf("body = %s", req.Body)
	}
}

// Every JSON payload goes through the same expansion, so each keeps types.
func TestEveryJSONPayloadKeepsTypes(t *testing.T) {
	vars := typedVars{"n": 42.0}
	gql, err := adapters.BuildGraphQLRequest(&ir.GraphQLSpec{URL: "/graphql", Query: "q", Variables: json.RawMessage(`{"id":"{{ n }}"}`)}, vars)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct{ Variables map[string]any }
	_ = json.Unmarshal(gql.Body, &payload)
	if payload.Variables["id"] != 42.0 {
		t.Errorf("graphql variables = %s", gql.Body)
	}

	frame, err := adapters.BuildWSFrame(&ir.WSSpec{Send: json.RawMessage(`{"id":"{{ n }}"}`)}, vars)
	if err != nil || string(frame) != `{"id":42}` {
		t.Errorf("ws frame = %s (%v)", frame, err)
	}

	g, err := adapters.BuildGRPCRequest(&ir.GRPCSpec{URL: "grpc://localhost:1", Message: json.RawMessage(`{"id":"{{ n }}"}`)}, vars)
	if err != nil || string(g.Body) != `{"id":42}` {
		t.Errorf("grpc message = %s (%v)", g.Body, err)
	}
}
