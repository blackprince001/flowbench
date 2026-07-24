// Package conformance holds the two-surface conformance suite (issue #22,
// ADR 0002): for a given flow, the YAML parser's IR and the Python SDK
// compiler's IR must be structurally equivalent. This is the acceptance
// check for "both surfaces produce the same canonical flow representation."
package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
)

func TestAuthenticatedCheckoutTwoSurfaceParity(t *testing.T) {
	yamlRes, err := parser.ParseFlowFile("../../tests/flows/authenticated_checkout.flow.yaml", nil)
	if err != nil {
		t.Fatalf("parsing YAML fixture: %v", err)
	}

	pyJSON := compilePythonFlow(t, "../../tests/flows/authenticated_checkout.py")
	pySc, err := ir.DecodeScenario(pyJSON)
	if err != nil {
		t.Fatalf("decoding Python-compiled IR: %v\n%s", err, pyJSON)
	}

	got, want := canonicalize(t, pySc), canonicalize(t, yamlRes.Scenario)
	if got != want {
		t.Errorf("IR diverged between surfaces:\n--- yaml ---\n%s\n--- python ---\n%s", want, got)
	}
}

// compilePythonFlow runs a Python flow file with FLOWBENCH_COMPILE_ONLY set,
// which makes flow.run(...) print the compiled IR as JSON instead of
// executing (there is no runtime execution path for Python-driven flows
// yet; that's issue #25). Skips, rather than fails, when no usable Python
// interpreter with the flowbench package is available, so `go test ./...`
// stays usable for Go-only contributors; CI installs Python explicitly so
// this check is never silently skipped there.
func compilePythonFlow(t *testing.T, path string) []byte {
	t.Helper()

	python := os.Getenv("FLOWBENCH_PYTHON")
	if python == "" {
		python = "python3"
	}
	if _, err := exec.LookPath(python); err != nil {
		t.Skipf("no %s interpreter on PATH: %v", python, err)
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	sdkSrc := filepath.Join(repoRoot, "sdk-python", "src")

	cmd := exec.Command(python, path)
	cmd.Env = append(os.Environ(),
		"FLOWBENCH_COMPILE_ONLY=1",
		"PYTHONPATH="+sdkSrc,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok && bytes.Contains(stderr.Bytes(), []byte("ModuleNotFoundError: No module named 'flowbench'")) {
			t.Skipf("flowbench Python package not importable: %s", stderr.String())
		}
		t.Fatalf("running %s %s: %v\nstderr:\n%s", python, path, err, stderr.String())
	}
	return stdout.Bytes()
}

// canonicalize re-decodes a Scenario's JSON into a generic tree, strips
// every "pos" key (YAML source positions the Python surface never sets),
// and re-marshals with sorted keys so two structurally-equal scenarios
// produce byte-identical, directly diffable strings.
func canonicalize(t *testing.T, sc *ir.Scenario) string {
	t.Helper()

	raw, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshaling scenario: %v", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("unmarshaling scenario into tree: %v", err)
	}
	tree = stripPos(tree)

	out, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		t.Fatalf("marshaling canonical tree: %v", err)
	}
	return string(out)
}

func stripPos(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "pos" {
				continue
			}
			out[k] = stripPos(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripPos(val)
		}
		return out
	default:
		return v
	}
}
