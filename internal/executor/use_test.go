package executor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// loginFlow is the reusable login from #102: it takes credentials as inputs
// and hands back only the token.
func loginFlow() *ir.Flow {
	emailDefault := "{{ env.FLOWBENCH_TEST_EMAIL }}"
	return &ir.Flow{
		Name:    "login",
		Inputs:  []ir.Input{{Name: "email", Default: &emailDefault}, {Name: "password"}},
		Outputs: []string{"token"},
		Steps: []ir.Step{{
			ID:   "login",
			Type: ir.StepCall,
			Call: &ir.CallSpec{
				Method: "POST", URL: "/auth/login",
				Body: json.RawMessage(`{"email":"{{ inputs.email }}","password":"{{ inputs.password }}"}`),
			},
			Extract: []ir.Extraction{{Var: "token", Path: "$.data.access_token"}, {Var: "internal", Path: "$.data.access_token"}},
			Assert:  []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
		}},
	}
}

// checkoutUsingLogin is checkoutFlow with its login step replaced by a use of
// loginFlow, the #102 acceptance shape.
func checkoutUsingLogin(onFailure ir.FailureAction) ir.Flow {
	f := checkoutFlow()
	f.Steps[0] = ir.Step{
		ID: "auth", Type: ir.StepUse, OnFailure: onFailure,
		Use: &ir.UseSpec{
			Path: "login.flow.yaml",
			Flow: loginFlow(),
			With: map[string]string{"email": "{{ user.email }}", "password": "{{ user.password }}"},
		},
	}
	for i := 1; i < len(f.Steps); i++ {
		for k, v := range f.Steps[i].Call.Headers {
			f.Steps[i].Call.Headers[k] = strings.ReplaceAll(v, "{{ token }}", "{{ auth.token }}")
		}
	}
	return f
}

func TestUsedFlowRunsInsideTheCaller(t *testing.T) {
	srv := checkoutServer(t)
	defer srv.Close()

	flow := checkoutUsingLogin("")
	sc := &ir.Scenario{Name: "s", Flows: []ir.Flow{flow}, Profile: ir.Profile{Mode: ir.ModeIntegration},
		DataPools: []ir.DataPool{{Name: "user", Source: "users.csv"}}}
	if err := sc.Validate(); err != nil {
		t.Fatalf("the rewritten checkout should validate: %v", err)
	}

	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
	scope := executor.NewScope("user", map[string]string{"email": "a@b.com", "password": "pw"})
	it, err := r.RunFlow(context.Background(), flow, scope)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Failures) != 0 || it.Outcome != span.OutcomeOK {
		t.Fatalf("want a clean run like today's checkout, got outcome %q failures %+v", it.Outcome, it.Failures)
	}

	if len(it.Spans) != 3 || it.Spans[0].Name != "auth" {
		t.Fatalf("want auth, create_order, pay at the top level, got %d spans", len(it.Spans))
	}
	auth := it.Spans[0]
	if len(auth.Children) != 1 || auth.Children[0].Name != "login" {
		t.Fatalf("the used flow's steps should nest under auth, got %v", childNames(auth))
	}
	if login := auth.Children[0]; login.Start < auth.Start || login.Start+login.Duration > auth.Start+auth.Duration {
		t.Errorf("login span [%v +%v] should sit inside auth [%v +%v] on the same clock",
			login.Start, login.Duration, auth.Start, auth.Duration)
	}

	if v, ok := scope.Lookup("auth.token"); !ok || v != "tok-abc" {
		t.Errorf("auth.token = %v (%v), want tok-abc", v, ok)
	}
	for _, leaked := range []string{"token", "internal", "auth.internal", "inputs.email"} {
		if _, ok := scope.Lookup(leaked); ok {
			t.Errorf("%q leaked from the used flow into the caller's scope", leaked)
		}
	}
}

func TestInputDefaultResolvesWhenCallerPassesNothing(t *testing.T) {
	t.Setenv("FLOWBENCH_TEST_EMAIL", "default@b.com")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Email string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body.Email
		w.Write([]byte(`{"data":{"access_token":"t"}}`))
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{{
		ID: "auth", Type: ir.StepUse,
		Use: &ir.UseSpec{Path: "login.flow.yaml", Flow: loginFlow(), With: map[string]string{"password": "pw"}},
	}}}
	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
	if _, err := r.RunFlow(context.Background(), flow, executor.NewScope("", nil)); err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if got != "default@b.com" {
		t.Errorf("email sent = %q, want the env default", got)
	}
}

// TestUsedFlowFailureFailsTheUseStep covers F5: the failure lands on the use
// step, and the use step's own on_failure decides whether the caller goes on.
func TestUsedFlowFailureFailsTheUseStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/login" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	next := ir.Step{ID: "after", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/after"}}
	run := func(onFailure ir.FailureAction) *executor.Iteration {
		t.Helper()
		flow := ir.Flow{Name: "f", Steps: []ir.Step{{
			ID: "auth", Type: ir.StepUse, OnFailure: onFailure,
			Use: &ir.UseSpec{Path: "login.flow.yaml", Flow: loginFlow(), With: map[string]string{"email": "e", "password": "p"}},
		}, next}}
		r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
		it, err := r.RunFlow(context.Background(), flow, executor.NewScope("", nil))
		if err != nil {
			t.Fatalf("RunFlow: %v", err)
		}
		return it
	}

	it := run("")
	if len(it.Spans) != 1 || it.Spans[0].Outcome != span.OutcomeFailed {
		t.Fatalf("default: auth should fail and stop the flow, got %d spans", len(it.Spans))
	}
	if len(it.Failures) != 1 || it.Failures[0].StepID != "auth/login" {
		t.Errorf("failure should name the use step and the used step, got %+v", it.Failures)
	}

	it = run(ir.FailureRecord)
	if len(it.Spans) != 2 || it.Spans[1].Name != "after" {
		t.Fatalf("record: the caller should go on to the next step, got %d spans", len(it.Spans))
	}
	if it.Aborted {
		t.Error("record must not abort the run")
	}

	if it = run(ir.FailureAbortRun); !it.Aborted {
		t.Error("abort_run on the use step should abort the run")
	}
}

// TestSecretPassedThroughWithStaysRedacted covers F3: an env value handed to
// a used flow is still a secret there.
func TestSecretPassedThroughWithStaysRedacted(t *testing.T) {
	const pass = "hunter2-do-not-leak-9f8e7d"
	t.Setenv("FLOWBENCH_TEST_PASS", pass)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusTeapot) // forces a failure whose detail carries the echo
		fmt.Fprintf(w, `{"echo":%q}`, body.Password)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{{
		ID: "auth", Type: ir.StepUse,
		Use: &ir.UseSpec{Path: "login.flow.yaml", Flow: loginFlow(),
			With: map[string]string{"email": "e", "password": "{{ env.FLOWBENCH_TEST_PASS }}"}},
	}}}
	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
	scope := executor.NewScope("", nil)
	it, err := r.RunFlow(context.Background(), flow, scope)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if !scope.Secrets().Contains(pass) {
		t.Fatal("an env value passed through with must be registered as a secret")
	}
	if len(it.Failures) == 0 {
		t.Fatal("want the forced failure")
	}
	for _, f := range it.Failures {
		if strings.Contains(f.Detail, pass) {
			t.Errorf("failure detail leaks the secret: %q", f.Detail)
		}
	}
}

// TestNestedUsedFlowsNestTheirSpans covers #103's acceptance: A uses B uses C
// runs, the spans nest three levels, and a failure deep down is named by the
// whole path of use steps that led to it.
func TestNestedUsedFlowsNestTheirSpans(t *testing.T) {
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`{"data":{"access_token":"t"}}`))
	}))
	defer srv.Close()
	t.Setenv("FLOWBENCH_TEST_EMAIL", "default@b.com")

	mid := &ir.Flow{Name: "token", Steps: []ir.Step{{
		ID: "login", Type: ir.StepUse,
		Use: &ir.UseSpec{Path: "login.flow.yaml", Flow: loginFlow(), With: map[string]string{"password": "pw"}},
	}}}
	flow := ir.Flow{Name: "a", Steps: []ir.Step{{
		ID: "auth", Type: ir.StepUse,
		Use: &ir.UseSpec{Path: "token.flow.yaml", Flow: mid},
	}}}
	sc := &ir.Scenario{Name: "s", Flows: []ir.Flow{flow}, Profile: ir.Profile{Mode: ir.ModeIntegration}}
	if err := sc.Validate(); err != nil {
		t.Fatalf("three levels should validate: %v", err)
	}

	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
	it, err := r.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if it.Outcome != span.OutcomeOK || len(it.Spans) != 1 {
		t.Fatalf("want one clean top-level span, got outcome %q, %d spans", it.Outcome, len(it.Spans))
	}
	auth := it.Spans[0]
	if len(auth.Children) != 1 || auth.Children[0].Name != "login" {
		t.Fatalf("login (a use step) should nest under auth, got %v", childNames(auth))
	}
	login := auth.Children[0]
	if len(login.Children) != 1 || login.Children[0].Name != "login" {
		t.Fatalf("the request should nest under login, got %v", childNames(login))
	}

	fail = true
	it, err = r.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Failures) != 1 || it.Failures[0].StepID != "auth/login/login" {
		t.Errorf("failures = %+v, want one at auth/login/login", it.Failures)
	}
}
