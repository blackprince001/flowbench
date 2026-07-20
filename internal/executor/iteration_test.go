package executor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

func val(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// checkoutFlow is the PRD section 11 chain, trimmed to what the M1 slice runs:
// login → extract token → authenticated order → extract id → pay → assert.
func checkoutFlow() ir.Flow {
	return ir.Flow{
		Name: "authenticated_checkout",
		Data: "user",
		Steps: []ir.Step{
			{
				ID:   "login",
				Type: ir.StepCall,
				Call: &ir.CallSpec{
					Method: "POST", URL: "/auth/login",
					Body: json.RawMessage(`{"email":"{{ user.email }}","password":"{{ user.password }}"}`),
				},
				Extract: []ir.Extraction{{Var: "token", Path: "$.data.access_token"}},
				Assert: []ir.Assertion{
					{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)},
					{Source: ir.AssertVar, Key: "token", Op: ir.OpExists},
				},
			},
			{
				ID:   "create_order",
				Type: ir.StepCall,
				Call: &ir.CallSpec{
					Method: "POST", URL: "/orders",
					Headers: map[string]string{"Authorization": "Bearer {{ token }}"},
				},
				Extract: []ir.Extraction{{Var: "order_id", Path: "$.data.id"}},
			},
			{
				ID:   "pay",
				Type: ir.StepCall,
				Call: &ir.CallSpec{
					Method: "POST", URL: "/orders/{{ order_id }}/pay",
					Headers: map[string]string{"Authorization": "Bearer {{ token }}"},
				},
				Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(202)}},
			},
		},
	}
}

func checkoutServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Email, Password string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
			http.Error(w, "bad login", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"data":{"access_token":"tok-abc"}}`))
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-abc" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"id":"ord-1"}}`))
	})
	mux.HandleFunc("POST /orders/{id}/pay", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-abc" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	return httptest.NewServer(mux)
}

func TestChainedFlowRunsEndToEnd(t *testing.T) {
	srv := checkoutServer(t)
	defer srv.Close()

	r := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: srv.URL,
		Mode:    ir.ModeIntegration,
	}
	scope := executor.NewScope("user", map[string]string{"email": "a@b.com", "password": "pw"})

	it, err := r.RunFlow(context.Background(), checkoutFlow(), scope)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}

	if len(it.Failures) != 0 {
		t.Fatalf("expected no failures, got %+v", it.Failures)
	}
	if it.Outcome != span.OutcomeOK {
		t.Errorf("outcome = %q, want ok", it.Outcome)
	}
	if len(it.Spans) != 3 {
		t.Fatalf("want a span per step, got %d", len(it.Spans))
	}

	if tok, ok := scope.Lookup("token"); !ok || tok != "tok-abc" {
		t.Errorf("token = %v (%v), want tok-abc", tok, ok)
	}
	if id, ok := scope.Lookup("order_id"); !ok || id != "ord-1" {
		t.Errorf("order_id = %v (%v), want ord-1", id, ok)
	}

	// login span should carry the extraction and assertion child spans.
	login := it.Spans[0]
	if names := childNames(login); !names["token"] || !names["assert_status"] || !names["assert_var_token"] {
		t.Errorf("login children = %v, missing extract/assert spans", names)
	}
}

func TestFailingAssertionIsRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{{
		ID:        "hit",
		Type:      ir.StepCall,
		Call:      &ir.CallSpec{Method: "GET", URL: "/x"},
		Assert:    []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
		OnFailure: ir.FailureRecord,
	}}}

	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
	it, err := r.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Failures) != 1 || it.Failures[0].StepID != "hit" {
		t.Fatalf("want one recorded failure on hit, got %+v", it.Failures)
	}
	if it.Outcome != span.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", it.Outcome)
	}
	if it.Aborted {
		t.Error("record must not abort the run")
	}
}

func TestIntegrationDefaultAbortsFlowOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{
		{ID: "one", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/a"},
			Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}}},
		{ID: "two", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/b"}},
	}}

	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
	it, err := r.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Spans) != 1 {
		t.Errorf("integration failure should stop the flow; ran %d steps", len(it.Spans))
	}
	if len(it.Failures) != 1 {
		t.Errorf("want one failure, got %+v", it.Failures)
	}
}

func childNames(sp *span.Span) map[string]bool {
	names := map[string]bool{}
	for _, c := range sp.Children {
		names[c.Name] = true
	}
	return names
}
