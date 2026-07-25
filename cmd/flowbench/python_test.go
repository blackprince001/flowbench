package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const checkoutPy = `from flowbench import Flow, Profile, Retry, expect

flow = Flow("checkout", data="users.csv")


@flow.step
def login(ctx):
    r = ctx.http.post("/auth/login", json={
        "email": ctx.user["email"],
        "password": ctx.user["password"],
    })
    expect(r.status).to_be(200)
    ctx.vars["token"] = r.json_path("$.data.access_token")
    expect(ctx.vars["token"]).not_to_be(None)


@flow.step
def create_order(ctx):
    r = ctx.http.post(
        "/orders",
        headers={"Authorization": f"Bearer {ctx.vars['token']}"},
    )
    ctx.vars["order_id"] = r.json_path("$.data.id")


@flow.step
def pay(ctx):
    r = ctx.http.post(f"/orders/{ctx.vars['order_id']}/pay")
    expect(r.status).to_be(202)


if __name__ == "__main__":
    flow.run(Profile(mode="integration"))
`

// requirePython skips the test when no usable Python/SDK is available,
// unless FLOWBENCH_REQUIRE_PYTHON is set (CI sets it, so a provisioning
// failure can't pass as a quiet skip) — mirrors internal/conformance's
// skipOrFail.
func requirePython(t *testing.T) {
	t.Helper()
	if os.Getenv("FLOWBENCH_REQUIRE_PYTHON") != "" {
		return
	}
	python := os.Getenv("FLOWBENCH_PYTHON")
	if python == "" {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("no python3 on PATH")
		}
		python = "python3"
	}
	if out, err := exec.Command(python, "-c", "import flowbench").CombinedOutput(); err != nil {
		t.Skipf("flowbench Python package not importable: %s", out)
	}
}

// writePythonScenario mirrors writeScenario for a .py flow: lays the flow
// file, its CSV fixture, and a target file into a temp dir.
func writePythonScenario(t *testing.T, flowPy, baseURL string) (scenario, targetPath string) {
	t.Helper()
	dir := t.TempDir()
	scenario = filepath.Join(dir, "checkout.py")
	targetPath = filepath.Join(dir, "stub.yaml")
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(scenario, flowPy)
	write(filepath.Join(dir, "users.csv"), usersCSV)
	write(targetPath, "name: teststub\nbase_urls:\n  - "+baseURL+"\n")
	return scenario, targetPath
}

func TestRunPythonFlowCompilesAndExecutesOnGoEngine(t *testing.T) {
	requirePython(t)

	srv := checkoutStub(t, 202)
	scenario, targetPath := writePythonScenario(t, checkoutPy, srv.URL)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", t.TempDir()})
	if code != exitOK {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "2 iteration(s): 2 passed, 0 failed") {
		t.Errorf("summary missing from output:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "run saved to") {
		t.Errorf("expected the run to be persisted:\n%s", stdout.String())
	}
}

func TestRunPythonFlowFailingAssertionExitsNonzero(t *testing.T) {
	requirePython(t)

	srv := checkoutStub(t, 500) // pay returns 500, not 202
	scenario, targetPath := writePythonScenario(t, checkoutPy, srv.URL)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", t.TempDir()})
	if code != exitFail {
		t.Fatalf("exit = %d, want 1 (a failing assertion)\nstdout:\n%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "pay") {
		t.Errorf("output should name the failing step:\n%s", stdout.String())
	}
}
