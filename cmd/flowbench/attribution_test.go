package main

import (
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
