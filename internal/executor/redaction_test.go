package executor_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/secret"
)

// TestEnvCredentialNeverReachesArtifacts is the issue #6 acceptance: a flow
// injects an env credential into a live request; the credential must appear
// nowhere in the recorded artifacts even when the target echoes it back.
func TestEnvCredentialNeverReachesArtifacts(t *testing.T) {
	const token = "s3cr3t-abcdef-0123456789"
	t.Setenv("FLOWBENCH_TEST_TOKEN", token)

	// A leaky endpoint: it reflects the Authorization header into its body,
	// and only answers 200 when the credential resolved correctly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"seen":%q}`, auth)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "whoami", Steps: []ir.Step{{
		ID:   "call_whoami",
		Type: ir.StepCall,
		Call: &ir.CallSpec{
			Method: "GET", URL: "/whoami",
			Headers: map[string]string{"Authorization": "Bearer {{ env.FLOWBENCH_TEST_TOKEN }}"},
		},
		Assert: []ir.Assertion{
			{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)},
			// Forced miss: the failure detail carries the echoed credential.
			{Source: ir.AssertBody, Key: "$.seen", Op: ir.OpEq, Value: val("Bearer wrong")},
		},
		OnFailure: ir.FailureRecord,
	}}}

	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeIntegration}
	scope := executor.NewScope("", nil)

	it, err := r.RunFlow(context.Background(), flow, scope)
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}

	// Resolution worked: the status assertion passed (server accepted the
	// credential), so the only recorded failure is the forced body miss.
	if len(it.Failures) != 1 {
		t.Fatalf("want exactly the forced body failure, got %+v", it.Failures)
	}

	// The credential was registered as a secret when the template resolved.
	if !scope.Secrets().Contains(token) {
		t.Errorf("env credential was not registered as a secret")
	}

	// No recorded artifact leaks the raw credential; the value is redacted.
	detail := it.Failures[0].Detail
	if strings.Contains(detail, token) {
		t.Fatalf("recorded failure leaks the credential: %q", detail)
	}
	if !strings.Contains(detail, secret.Placeholder) {
		t.Errorf("expected the credential redacted in the failure detail, got %q", detail)
	}

	// A captured response body, run through the scope's redactor, is clean.
	captured := []byte(fmt.Sprintf(`{"seen":"Bearer %s"}`, token))
	if strings.Contains(string(scope.Secrets().RedactBytes(captured)), token) {
		t.Errorf("redactor left the credential in a captured body")
	}
}
