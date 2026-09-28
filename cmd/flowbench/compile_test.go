package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
)

const compileFixtureFlow = `flow: login
inputs:
  email:
  password:
steps:
  - id: login
    call: POST /auth/login
    body: { email: "{{ inputs.email }}", password: "{{ inputs.password }}" }
    extract: { token: $.data.access_token }
outputs: [token]
`

func TestCompileCommandPrintsScenarioJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "login.flow.yaml")
	if err := os.WriteFile(path, []byte(compileFixtureFlow), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"compile", path})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr.String())
	}

	var sc ir.Scenario
	if err := json.Unmarshal([]byte(stdout.String()), &sc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	if len(sc.Flows) != 1 || sc.Flows[0].Name != "login" {
		t.Fatalf("scenario = %+v, want one flow named login", sc)
	}
	f := sc.Flows[0]
	if len(f.Inputs) != 2 || len(f.Outputs) != 1 || f.Outputs[0] != "token" {
		t.Errorf("flow = %+v, want 2 inputs and outputs [token]", f)
	}
}

func TestCompileCommandInvalidFlowFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.flow.yaml")
	if err := os.WriteFile(path, []byte("flow: bad\nsteps: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"compile", path})
	if code != exitPreRun {
		t.Fatalf("exit code = %d, want %d", code, exitPreRun)
	}
	if !strings.Contains(stderr.String(), "at least one step") {
		t.Errorf("stderr = %q, want it to name the validation error", stderr.String())
	}
}

func TestCompileCommandMissingFileFails(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"compile", "nope.flow.yaml"})
	if code != exitPreRun {
		t.Fatalf("exit code = %d, want %d", code, exitPreRun)
	}
}

func TestCompileCommandUsage(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"compile"})
	if code != exitPreRun {
		t.Fatalf("exit code = %d, want %d", code, exitPreRun)
	}
	if !strings.Contains(stderr.String(), "usage: flowbench compile") {
		t.Errorf("stderr = %q, want usage text", stderr.String())
	}
}
