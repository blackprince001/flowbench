package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/store"
)

func TestGitInfo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "flow.yaml")
	if err := os.WriteFile(file, []byte("flow: f\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@example.test"},
		{"config", "user.name", "t"},
		{"add", "flow.yaml"},
		{"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	commit, dirty := gitInfo(dir, file)
	if commit == "" {
		t.Fatal("expected a HEAD commit")
	}
	if dirty {
		t.Error("a committed file should not be dirty")
	}

	if err := os.WriteFile(file, []byte("flow: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, dirty := gitInfo(dir, file); !dirty {
		t.Error("a modified file should be dirty")
	}
}

// TestRunLoadWritesRunStore is the issue #18 acceptance: a load run leaves a run
// directory carrying attribution metadata.
func TestRunLoadWritesRunStore(t *testing.T) {
	srv := slowStub(t, 2*time.Millisecond)
	loose := strings.Replace(loadFlow, "p95(latency) < 20ms", "p95(latency) < 500ms", 1)
	scenario, targetPath := writeScenario(t, loose, srv.URL)
	storeDir := t.TempDir()

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", storeDir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "run saved to") {
		t.Errorf("output should report the saved run:\n%s", stdout.String())
	}

	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want one stored run, got %d", len(runs))
	}
	m := runs[0]
	if m.Target != "teststub" || m.Mode != "load" || m.Initiator == "" {
		t.Errorf("run metadata missing attribution: %+v", m)
	}
}

// TestRunIntegrationWritesRunStore is issue #25's Go-side prerequisite:
// integration/system mode previously never persisted a run artifact at all,
// so there was nothing to compare a Python-driven run against. It now saves
// the same way load/stress/soak already did.
func TestRunIntegrationWritesRunStore(t *testing.T) {
	srv := checkoutStub(t, http.StatusAccepted)
	scenario, targetPath := writeScenario(t, checkoutFlow, srv.URL)
	storeDir := t.TempDir()

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", storeDir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "run saved to") {
		t.Errorf("output should report the saved run:\n%s", stdout.String())
	}

	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want one stored run, got %d", len(runs))
	}
	m := runs[0]
	if m.Mode != "integration" || m.FlowRuns != 2 || m.Iterations != 2 {
		t.Errorf("run metadata off: %+v", m)
	}
	if m.ErrorRate != 0 {
		t.Errorf("error_rate = %v, want 0 for an all-passing run", m.ErrorRate)
	}

	traces, err := st.LoadTraces(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 {
		t.Fatalf("want a kept trace per iteration (small scale, no sampling), got %d", len(traces))
	}
	if !strings.HasPrefix(traces[0].Name, "flow:") {
		t.Errorf("trace root name = %q, want a flow: prefix", traces[0].Name)
	}

	folded, err := st.LoadFolded(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if folded == nil || folded.Root == nil || len(folded.Root.Children) == 0 {
		t.Error("folded aggregate should be non-empty")
	}
}
