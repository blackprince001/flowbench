package executor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
	"github.com/blackprince001/flowbench/internal/span"
	"github.com/blackprince001/flowbench/internal/target"
)

// operation is one GraphQL request as the stub receives it — the wire shape
// the adapter is responsible for producing.
type operation struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
	Operation string         `json:"operationName"`
}

// graphqlStub is a small graph: a product query, a placeOrder mutation, a
// restricted field that errors, and a reviews field that fails while the rest
// of the query still resolves. It dispatches on the operation the request
// actually carried, so a step that sent the wrong document gets the wrong
// answer rather than a blanket 200.
func graphqlStub(t *testing.T, seen *[]operation) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("GraphQL operation arrived as %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		var op operation
		if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
			t.Errorf("decoding operation: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if seen != nil {
			*seen = append(*seen, op)
		}

		// Every answer below is a 200: in GraphQL the status describes the
		// transport, not the operation.
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(op.Query, "costPriceCents"):
			w.Write([]byte(`{"data":null,"errors":[
				{"message":"field 'costPriceCents' is restricted","path":["product","costPriceCents"]},
				{"message":"insufficient scope"}]}`))

		case strings.Contains(op.Query, "reviews"):
			// Partial success: the product resolved, the reviews subgraph did not.
			w.Write([]byte(`{"data":{"product":{"id":"prod_42","name":"Flowbench Tee"},"reviews":null},
				"errors":[{"message":"reviews subgraph timed out","path":["reviews"]}]}`))

		case strings.Contains(op.Query, "placeOrder"):
			if got := op.Variables["productId"]; got != "prod_42" {
				t.Errorf("mutation productId = %v, want the extracted %q", got, "prod_42")
			}
			// JSON numbers decode as float64; the point is that 2 arrived as a
			// number, not the string "2".
			if got, ok := op.Variables["quantity"].(float64); !ok || got != 2 {
				t.Errorf("mutation quantity = %#v, want the number 2", op.Variables["quantity"])
			}
			w.Write([]byte(`{"data":{"placeOrder":{"id":"ord_777","status":"PENDING"}}}`))

		case strings.Contains(op.Query, "product"):
			if got := op.Variables["sku"]; got != "FB-001" {
				t.Errorf("query sku = %v, want FB-001", got)
			}
			w.Write([]byte(`{"data":{"product":{"id":"prod_42","name":"Flowbench Tee","priceCents":2500}}}`))

		default:
			w.Write([]byte(`{"errors":[{"message":"unknown operation"}]}`))
		}
	})
}

func runGraphQLFixture(t *testing.T, fixture, baseURL string) (*executor.Iteration, *executor.Scope) {
	t.Helper()

	path := "../../tests/flows/graphql/" + fixture
	res, err := parser.ParseFlowFile(path, nil)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	tgt, err := target.New(&ir.TargetConfig{Name: "stub", BaseURLs: []string{baseURL}})
	if err != nil {
		t.Fatalf("building target: %v", err)
	}
	if err := tgt.Check(res.Scenario); err != nil {
		t.Fatalf("target gate refused %s: %v", path, err)
	}

	scope := executor.NewScope("", nil)
	runner := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: tgt.BaseURL(),
		Mode:    res.Scenario.Profile.Mode,
		Allow:   tgt.Allows,
	}
	it, err := runner.RunFlow(context.Background(), res.Scenario.Flows[0], scope)
	if err != nil {
		t.Fatalf("running %s: %v", path, err)
	}
	return it, scope
}

// TestGraphQLQueryMutationChain is the issue #26 acceptance: a query and a
// mutation chained by an extracted variable, against a GraphQL stub, in
// integration mode.
func TestGraphQLQueryMutationChain(t *testing.T) {
	var seen []operation
	srv := httptest.NewServer(graphqlStub(t, &seen))
	defer srv.Close()

	it, scope := runGraphQLFixture(t, "chain.flow.yaml", srv.URL)
	if len(it.Failures) > 0 {
		t.Fatalf("chain should pass, got %v", it.Failures)
	}

	// Extraction over the data shape fed the mutation.
	if got, _ := scope.Lookup("product_id"); got != "prod_42" {
		t.Errorf("product_id = %v, want prod_42", got)
	}
	if got, _ := scope.Lookup("order_id"); got != "ord_777" {
		t.Errorf("order_id = %v, want ord_777", got)
	}

	if len(seen) != 2 {
		t.Fatalf("stub saw %d operations, want 2", len(seen))
	}
	// The document went over the wire verbatim, with values alongside it as
	// variables rather than substituted into it.
	if !strings.Contains(seen[0].Query, "query FindProduct($sku: String!)") {
		t.Errorf("query document was rewritten: %q", seen[0].Query)
	}
	if strings.Contains(seen[1].Query, "prod_42") {
		t.Errorf("an extracted value was spliced into the document: %q", seen[1].Query)
	}
}

// TestGraphQLSpansMatchHTTP is the "spans identical to HTTP" half of the
// issue: a GraphQL step is an HTTP request, so it carries the same phase
// children and the same recorded call metadata as a native call step.
func TestGraphQLSpansMatchHTTP(t *testing.T) {
	srv := httptest.NewServer(graphqlStub(t, nil))
	defer srv.Close()

	it, _ := runGraphQLFixture(t, "chain.flow.yaml", srv.URL)
	if len(it.Spans) != 2 {
		t.Fatalf("want a span per step, got %d", len(it.Spans))
	}

	step := it.Spans[0]
	if step.Name != "find_product" || step.Outcome != span.OutcomeOK {
		t.Errorf("step span = %q (%s)", step.Name, step.Outcome)
	}
	var httpCall *span.Span
	for _, c := range step.Children {
		if c.Name == "http_call" {
			httpCall = c
		}
	}
	if httpCall == nil {
		t.Fatalf("GraphQL step has no http_call child; children = %v", childNames(step))
	}
	// The transport phases the HTTP adapter emits, unchanged.
	phases := childNames(httpCall)
	for _, want := range []string{"connect", "ttfb"} {
		if !phases[want] {
			t.Errorf("http_call is missing the %q phase; got %v", want, phases)
		}
	}
}

// TestGraphQLErrorsFailTheStepByDefault is the classification decision: the
// operation failed inside a 200, and nothing asserted on it.
func TestGraphQLErrorsFailTheStepByDefault(t *testing.T) {
	srv := httptest.NewServer(graphqlStub(t, nil))
	defer srv.Close()

	it, _ := runGraphQLFixture(t, "errors.flow.yaml", srv.URL)
	if len(it.Failures) != 1 {
		t.Fatalf("a GraphQL error should fail the step, got %v", it.Failures)
	}

	detail := it.Failures[0].Detail
	// The message and the field that failed both survive into the artifact.
	if !strings.Contains(detail, "restricted") || !strings.Contains(detail, "product.costPriceCents") {
		t.Errorf("failure detail should name the error and its path, got %q", detail)
	}
	if !strings.Contains(detail, "insufficient scope") {
		t.Errorf("every error should be reported, got %q", detail)
	}

	// It folds under its own structural name, so a run where one query kept
	// erroring shows up in the flame graph.
	if !childNames(it.Spans[0])["graphql_errors"] {
		t.Errorf("want a graphql_errors child span, got %v", childNames(it.Spans[0]))
	}
	if it.Spans[0].Outcome != span.OutcomeFailed {
		t.Errorf("step outcome = %s, want failed", it.Spans[0].Outcome)
	}
}

// TestGraphQLAllowPartialAcceptsDegradedData covers the federated case: one
// subgraph failed, the rest resolved, and the response is still useful.
func TestGraphQLAllowPartialAcceptsDegradedData(t *testing.T) {
	srv := httptest.NewServer(graphqlStub(t, nil))
	defer srv.Close()

	it, scope := runGraphQLFixture(t, "partial.flow.yaml", srv.URL)
	if len(it.Failures) > 0 {
		t.Fatalf("a partial success should pass under allow_partial, got %v", it.Failures)
	}
	// Extraction still ran, over the half of the document that resolved.
	if got, _ := scope.Lookup("product_id"); got != "prod_42" {
		t.Errorf("product_id = %v, want prod_42", got)
	}
}

// TestGraphQLAllowPartialStillFailsWithoutData is the other half of the
// policy: partial means partial, not "errors never matter".
func TestGraphQLAllowPartialStillFailsWithoutData(t *testing.T) {
	srv := httptest.NewServer(graphqlStub(t, nil))
	defer srv.Close()

	flow := ir.Flow{Name: "no_data", Steps: []ir.Step{{
		ID:   "restricted",
		Type: ir.StepGraphQL,
		GraphQL: &ir.GraphQLSpec{
			URL:      "/graphql",
			Query:    "query Restricted { product(sku: \"FB-001\") { id costPriceCents } }",
			OnErrors: ir.GraphQLErrorsAllowPartial,
		},
		OnFailure: ir.FailureRecord,
	}}}

	runner := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: srv.URL,
		Mode:    ir.ModeIntegration,
	}
	it, err := runner.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Failures) != 1 {
		t.Fatalf("errors with no data should fail even under allow_partial, got %v", it.Failures)
	}
}

// TestGraphQLIgnoreLeavesItToTheFlow hands classification back to the author's
// own assertions.
func TestGraphQLIgnoreLeavesItToTheFlow(t *testing.T) {
	srv := httptest.NewServer(graphqlStub(t, nil))
	defer srv.Close()

	step := ir.Step{
		ID:   "restricted",
		Type: ir.StepGraphQL,
		GraphQL: &ir.GraphQLSpec{
			URL:      "/graphql",
			Query:    "query Restricted { product(sku: \"FB-001\") { id costPriceCents } }",
			OnErrors: ir.GraphQLErrorsIgnore,
		},
		OnFailure: ir.FailureRecord,
	}
	runner := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: srv.URL,
		Mode:    ir.ModeIntegration,
	}

	it, err := runner.RunFlow(context.Background(), ir.Flow{Name: "f", Steps: []ir.Step{step}}, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Failures) > 0 {
		t.Fatalf("ignore should leave the errors alone, got %v", it.Failures)
	}

	// The author's own assertion is what catches it now.
	step.Assert = []ir.Assertion{{Source: ir.AssertBody, Key: "$.errors", Op: ir.OpNotExists}}
	it, err = runner.RunFlow(context.Background(), ir.Flow{Name: "f", Steps: []ir.Step{step}}, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Failures) != 1 {
		t.Fatalf("the flow's own assertion should have failed, got %v", it.Failures)
	}
}

// TestGraphQLMalformedResponseFails covers a proxy answering 200 with HTML:
// there is no operation result in it to extract from, so the step fails
// rather than reporting a mysteriously empty extraction.
func TestGraphQLMalformedResponseFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{{
		ID:        "op",
		Type:      ir.StepGraphQL,
		GraphQL:   &ir.GraphQLSpec{URL: "/graphql", Query: "query Q { a }"},
		OnFailure: ir.FailureRecord,
	}}}

	runner := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: srv.URL,
		Mode:    ir.ModeIntegration,
	}
	it, err := runner.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Failures) != 1 || !strings.Contains(it.Failures[0].Detail, "not a GraphQL document") {
		t.Fatalf("want a malformed-response failure, got %v", it.Failures)
	}
}
