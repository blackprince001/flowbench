package parser_test

import (
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
)

// TestParsesGraphQLChainFixture reads the checked-in fixture, so the shape the
// docs show is the shape the parser accepts.
func TestParsesGraphQLChainFixture(t *testing.T) {
	res, err := parser.ParseFlowFile("../../tests/flows/graphql/chain.flow.yaml", nil)
	if err != nil {
		t.Fatalf("fixture should parse, got:\n%v", err)
	}
	flow := res.Scenario.Flows[0]
	if len(flow.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(flow.Steps))
	}

	find, order := flow.Steps[0], flow.Steps[1]
	if find.Type != ir.StepGraphQL || find.GraphQL == nil {
		t.Fatalf("step 0 = %q with graphql=%v", find.Type, find.GraphQL)
	}
	// A `|` block scalar is the natural way to write a document, so the
	// multi-line query has to survive parsing intact.
	if !strings.Contains(find.GraphQL.Query, "query FindProduct($sku: String!)") ||
		!strings.Contains(find.GraphQL.Query, "product(sku: $sku)") {
		t.Errorf("query document = %q", find.GraphQL.Query)
	}
	if string(find.GraphQL.Variables) != `{"sku":"FB-001"}` {
		t.Errorf("variables = %s", find.GraphQL.Variables)
	}
	if len(find.Extract) != 2 || find.Extract[0].Path != "$.data.product.id" {
		t.Errorf("extractions = %+v", find.Extract)
	}

	// The mutation's variable references the earlier step's extraction, so the
	// variable graph has to see through the JSON blob to find it.
	if !strings.Contains(string(order.GraphQL.Variables), "{{ product_id }}") {
		t.Errorf("mutation variables = %s", order.GraphQL.Variables)
	}
}

func TestGraphQLOnErrorsPolicies(t *testing.T) {
	res, err := parser.ParseFlowFile("../../tests/flows/graphql/partial.flow.yaml", nil)
	if err != nil {
		t.Fatalf("fixture should parse, got:\n%v", err)
	}
	if got := res.Scenario.Flows[0].Steps[0].GraphQL.OnErrors; got != ir.GraphQLErrorsAllowPartial {
		t.Errorf("on_errors = %q, want allow_partial", got)
	}
}

func TestGraphQLErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "no url",
			src: `
flow: f
steps:
  - id: s
    graphql:
      query: "{ a }"
`,
			want: "graphql step needs a url",
		},
		{
			name: "no query",
			src: `
flow: f
steps:
  - id: s
    graphql:
      url: /graphql
`,
			want: "graphql step needs a query",
		},
		{
			name: "unknown key",
			src: `
flow: f
steps:
  - id: s
    graphql:
      url: /graphql
      query: "{ a }"
      mutation: "{ b }"
`,
			want: `unknown graphql key "mutation"`,
		},
		{
			name: "unknown error policy",
			src: `
flow: f
steps:
  - id: s
    graphql:
      url: /graphql
      query: "{ a }"
      on_errors: shrug
`,
			want: `unknown graphql on_errors "shrug"`,
		},
		{
			name: "variables must be a mapping",
			src: `
flow: f
steps:
  - id: s
    graphql:
      url: /graphql
      query: "{ a }"
      variables: [1, 2]
`,
			want: "graphql variables must be a mapping",
		},
		{
			name: "call-shaped body has nowhere to go",
			src: `
flow: f
steps:
  - id: s
    graphql:
      url: /graphql
      query: "{ a }"
    body: { a: 1 }
`,
			want: "carries its values in the operation's variables",
		},
		{
			name: "both call and graphql",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    graphql:
      url: /graphql
      query: "{ a }"
`,
			want: "sets more than one of call, graphql, ws, grpc, wait, poll",
		},
		{
			name: "variable with no upstream source",
			src: `
flow: f
steps:
  - id: s
    graphql:
      url: /graphql
      query: "query Q($id: ID!) { a(id: $id) { b } }"
      variables: { id: "{{ nothing_extracts_this }}" }
`,
			want: "has no upstream source",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseAuthFlowErr(t, tc.src); !strings.Contains(got, tc.want) {
				t.Errorf("error = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// TestGraphQLHeadersStayAtStepLevel keeps one spelling for HTTP headers: they
// are ordinary headers on an ordinary POST, so they live where a call step
// puts them rather than inside the graphql block.
func TestGraphQLHeadersStayAtStepLevel(t *testing.T) {
	flow := parseAuthFlow(t, `
flow: f
steps:
  - id: s
    graphql:
      url: /graphql
      query: "{ a }"
    headers: { X-Trace: "{{ env.TRACE_ID }}" }
`)
	if got := flow.Steps[0].GraphQL.Headers["X-Trace"]; got != "{{ env.TRACE_ID }}" {
		t.Errorf("headers = %v", flow.Steps[0].GraphQL.Headers)
	}
}

// TestGraphQLStepTakesAuthAndRetry proves the adapter inherits the machinery
// that already exists rather than reimplementing it.
func TestGraphQLStepTakesAuthAndRetry(t *testing.T) {
	flow := parseAuthFlow(t, `
flow: f
auth:
  scheme: bearer
  token: "{{ env.TOKEN }}"
steps:
  - id: s
    graphql:
      url: /graphql
      query: "{ a }"
    retry:
      on_status: [429]
      backoff: honor_retry_after
      max_attempts: 3
`)
	st := flow.Steps[0]
	if st.Auth == nil || st.Auth.Scheme != ir.AuthBearer {
		t.Errorf("graphql step should inherit the flow's auth, got %+v", st.Auth)
	}
	if st.Retry == nil || st.Retry.MaxAttempts != 3 {
		t.Errorf("graphql step should accept a retry policy, got %+v", st.Retry)
	}
}
