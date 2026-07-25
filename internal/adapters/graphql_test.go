package adapters_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/ir"
)

func vars(t *testing.T, values map[string]string) adapters.Resolver {
	t.Helper()
	return func(ref string) (string, error) {
		v, ok := values[ref]
		if !ok {
			return "", errNotFound{ref}
		}
		return v, nil
	}
}

type errNotFound struct{ ref string }

func (e errNotFound) Error() string { return "no variable " + e.ref }

// TestBuildGraphQLRequestShape pins the wire format: a POST whose JSON body
// carries the document, the variables, and the operation name.
func TestBuildGraphQLRequestShape(t *testing.T) {
	req, err := adapters.BuildGraphQLRequest(&ir.GraphQLSpec{
		URL:       "/graphql",
		Query:     "query FindProduct($sku: String!) { product(sku: $sku) { id } }",
		Variables: json.RawMessage(`{"sku":"FB-001"}`),
		Operation: "FindProduct",
		Headers:   map[string]string{"X-Trace": "abc"},
	}, vars(t, nil))
	if err != nil {
		t.Fatalf("BuildGraphQLRequest: %v", err)
	}

	if req.Method != "POST" || req.URL != "/graphql" {
		t.Errorf("request = %s %s, want POST /graphql", req.Method, req.URL)
	}
	if got := req.Headers["Content-Type"]; got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := req.Headers["X-Trace"]; got != "abc" {
		t.Errorf("declared header did not survive: %q", got)
	}

	var payload struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
		Operation string         `json:"operationName"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, req.Body)
	}
	if !strings.HasPrefix(payload.Query, "query FindProduct") {
		t.Errorf("query = %q", payload.Query)
	}
	if payload.Variables["sku"] != "FB-001" {
		t.Errorf("variables = %v", payload.Variables)
	}
	if payload.Operation != "FindProduct" {
		t.Errorf("operationName = %q", payload.Operation)
	}
}

// TestVariablesKeepTheirJSONTypes is why values travel as variables rather
// than spliced into the document: the server sees a number as a number, and
// its schema can type-check it.
func TestVariablesKeepTheirJSONTypes(t *testing.T) {
	req, err := adapters.BuildGraphQLRequest(&ir.GraphQLSpec{
		URL:       "/graphql",
		Query:     "mutation M($n: Int!, $flag: Boolean!, $id: ID!) { m(n: $n, flag: $flag, id: $id) { ok } }",
		Variables: json.RawMessage(`{"n":2,"flag":true,"id":"{{ order_id }}"}`),
	}, vars(t, map[string]string{"order_id": "ord_1"}))
	if err != nil {
		t.Fatalf("BuildGraphQLRequest: %v", err)
	}

	var payload struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if n, ok := payload.Variables["n"].(float64); !ok || n != 2 {
		t.Errorf("n = %#v, want the number 2", payload.Variables["n"])
	}
	if flag, ok := payload.Variables["flag"].(bool); !ok || !flag {
		t.Errorf("flag = %#v, want the boolean true", payload.Variables["flag"])
	}
	if payload.Variables["id"] != "ord_1" {
		t.Errorf("templated id = %#v", payload.Variables["id"])
	}
}

// TestTemplatedVariableCannotBreakTheDocument is the injection case: an
// extracted value full of quotes and braces stays one JSON string.
func TestTemplatedVariableCannotBreakTheDocument(t *testing.T) {
	nasty := `" } evil(x: "pwned`
	req, err := adapters.BuildGraphQLRequest(&ir.GraphQLSpec{
		URL:       "/graphql",
		Query:     "query Q($name: String!) { search(name: $name) { id } }",
		Variables: json.RawMessage(`{"name":"{{ name }}"}`),
	}, vars(t, map[string]string{"name": nasty}))
	if err != nil {
		t.Fatalf("BuildGraphQLRequest: %v", err)
	}

	var payload struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, req.Body)
	}
	if payload.Variables["name"] != nasty {
		t.Errorf("value = %#v, want it carried through intact", payload.Variables["name"])
	}
	if strings.Contains(payload.Query, "evil") {
		t.Errorf("the value reached the document: %q", payload.Query)
	}
}

func TestBuildGraphQLRequestReportsResolutionFailures(t *testing.T) {
	_, err := adapters.BuildGraphQLRequest(&ir.GraphQLSpec{
		URL:       "/graphql",
		Query:     "query Q($id: ID!) { a(id: $id) { b } }",
		Variables: json.RawMessage(`{"id":"{{ missing }}"}`),
	}, vars(t, nil))
	if err == nil || !strings.Contains(err.Error(), "variables") {
		t.Fatalf("want a variables resolution error, got %v", err)
	}
}

func TestReadGraphQLResult(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		errors    []string
		hasData   bool
		malformed bool
	}{
		{
			name:    "clean response",
			body:    `{"data":{"product":{"id":"p1"}}}`,
			hasData: true,
		},
		{
			name:   "errors with no data",
			body:   `{"data":null,"errors":[{"message":"boom"}]}`,
			errors: []string{"boom"},
		},
		{
			name:    "partial success keeps both",
			body:    `{"data":{"a":1},"errors":[{"message":"subgraph down","path":["reviews"]}]}`,
			errors:  []string{"subgraph down (at reviews)"},
			hasData: true,
		},
		{
			name:   "path renders list indexes",
			body:   `{"errors":[{"message":"bad","path":["items",2,"name"]}]}`,
			errors: []string{"bad (at items[2].name)"},
		},
		{
			name:   "error without a message still reports",
			body:   `{"errors":[{}]}`,
			errors: []string{"(no message)"},
		},
		{
			name:      "not a graphql document",
			body:      `<html>502</html>`,
			malformed: true,
		},
		{
			name:    "empty data object still counts as data",
			body:    `{"data":{}}`,
			hasData: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := adapters.ReadGraphQLResult([]byte(tc.body))
			if (got.Malformed != nil) != tc.malformed {
				t.Fatalf("malformed = %v, want %v", got.Malformed, tc.malformed)
			}
			if got.HasData != tc.hasData {
				t.Errorf("HasData = %v, want %v", got.HasData, tc.hasData)
			}
			if len(got.Errors) != len(tc.errors) {
				t.Fatalf("errors = %v, want %v", got.Errors, tc.errors)
			}
			for i, want := range tc.errors {
				if got.Errors[i] != want {
					t.Errorf("error %d = %q, want %q", i, got.Errors[i], want)
				}
			}
		})
	}
}
