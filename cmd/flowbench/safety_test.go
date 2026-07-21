package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRun lays a scenario and a custom target file into a temp dir.
func writeRun(t *testing.T, flowYAML, targetYAML string) (scenario, targetPath string) {
	t.Helper()
	dir := t.TempDir()
	scenario = filepath.Join(dir, "s.flow.yaml")
	targetPath = filepath.Join(dir, "t.yaml")
	if err := os.WriteFile(scenario, []byte(flowYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte(targetYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return scenario, targetPath
}

const stressFlow = `flow: hammer
steps:
  - id: hit
    call: GET /
profile:
  mode: stress
  vus: 5
  hold: 200ms
`

// TestRunDisallowedModeRefusedPreRun is a #20 acceptance: a stress run against a
// config that disallows stress is refused before any load.
func TestRunDisallowedModeRefusedPreRun(t *testing.T) {
	srv := slowStub(t, time.Millisecond)
	target := fmt.Sprintf("name: prod\nbase_urls:\n  - %s\ndisallowed_modes: [stress]\n", srv.URL)
	scenario, targetPath := writeRun(t, stressFlow, target)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", t.TempDir()})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (pre-run refusal)\nstderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "disallows") {
		t.Errorf("stderr should explain the mode refusal:\n%s", stderr.String())
	}
}

const bigLoadFlow = `flow: hammer
steps:
  - id: hit
    call: GET /
profile:
  mode: load
  vus: 500
  hold: 200ms
`

// TestRunVUCeilingRefusedPreRun is a #20 acceptance: a schedule that peaks above
// the target's VU ceiling is refused before any load.
func TestRunVUCeilingRefusedPreRun(t *testing.T) {
	srv := slowStub(t, time.Millisecond)
	target := fmt.Sprintf("name: prod\nbase_urls:\n  - %s\nmax_vus: 100\n", srv.URL)
	scenario, targetPath := writeRun(t, bigLoadFlow, target)

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", t.TempDir()})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (VU ceiling refusal)\nstderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ceiling") {
		t.Errorf("stderr should explain the ceiling refusal:\n%s", stderr.String())
	}
}
