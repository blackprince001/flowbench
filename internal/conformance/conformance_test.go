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
	"strconv"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
)

// fixtures are the flows written twice, once per surface. Each name resolves
// to ../../tests/flows/<name>.flow.yaml and ../../tests/flows/<name>.py.
var fixtures = []string{
	"authenticated_checkout", // the PRD section 11 sample: chaining (#22)
	"auth_schemes",           // every auth scheme, plus flow default and opt-out (#30)
	"graphql_operations",     // query, chained mutation, error policy (#26)
	"ws_session",             // sessions across steps, frame matching (#27)
}

func TestTwoSurfaceParity(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			yamlRes, err := parser.ParseFlowFile("../../tests/flows/"+name+".flow.yaml", nil)
			if err != nil {
				t.Fatalf("parsing YAML fixture: %v", err)
			}

			pyJSON := compilePythonFlow(t, "../../tests/flows/"+name+".py")
			pySc, err := ir.DecodeScenario(pyJSON)
			if err != nil {
				t.Fatalf("decoding Python-compiled IR: %v\n%s", err, pyJSON)
			}

			got, want := canonicalize(t, pySc), canonicalize(t, yamlRes.Scenario)
			if got != want {
				t.Errorf("IR diverged between surfaces:\n--- yaml ---\n%s\n--- python ---\n%s", want, got)
			}
		})
	}
}

// minPythonMinor mirrors requires-python in sdk-python/pyproject.toml. The
// SDK uses 3.10 syntax, so an older interpreter fails at import.
const minPythonMinor = 10

// compilePythonFlow runs a Python flow file with FLOWBENCH_COMPILE_ONLY set,
// which makes flow.run(...) print the compiled IR as JSON instead of
// executing (there is no runtime execution path for Python-driven flows
// yet; that's issue #25).
func compilePythonFlow(t *testing.T, path string) []byte {
	t.Helper()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	python := pythonInterpreter(t, repoRoot)

	cmd := exec.Command(python, path)
	cmd.Env = append(os.Environ(),
		"FLOWBENCH_COMPILE_ONLY=1",
		"PYTHONPATH="+filepath.Join(repoRoot, "sdk-python", "src"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok && bytes.Contains(stderr.Bytes(), []byte("ModuleNotFoundError: No module named 'flowbench'")) {
			skipOrFail(t, "flowbench Python package not importable: %s", stderr.String())
		}
		t.Fatalf("running %s %s: %v\nstderr:\n%s", python, path, err, stderr.String())
	}
	return stdout.Bytes()
}

// pythonInterpreter resolves the interpreter that compiles the Python
// fixture: $FLOWBENCH_PYTHON, else the uv-managed sdk-python/.venv, else
// python3 on PATH. That last one is a coin flip -- macOS still ships 3.9 as
// python3, which cannot even import the SDK -- so the version is checked
// whichever way it was found.
func pythonInterpreter(t *testing.T, repoRoot string) string {
	t.Helper()

	python := resolvePython(t, repoRoot)
	if minor := pythonMinor(t, python); minor < minPythonMinor {
		skipOrFail(t, "%s is Python 3.%d, the SDK needs >= 3.%d; run `uv sync --project sdk-python`",
			python, minor, minPythonMinor)
	}
	return python
}

func resolvePython(t *testing.T, repoRoot string) string {
	t.Helper()

	if python := os.Getenv("FLOWBENCH_PYTHON"); python != "" {
		return python
	}
	venv := filepath.Join(repoRoot, "sdk-python", ".venv")
	for _, python := range []string{
		filepath.Join(venv, "bin", "python"),
		filepath.Join(venv, "Scripts", "python.exe"),
	} {
		if _, err := os.Stat(python); err == nil {
			return python
		}
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		skipOrFail(t, "no python3 on PATH (%v); run `uv sync --project sdk-python`", err)
	}
	return python
}

func pythonMinor(t *testing.T, python string) int {
	t.Helper()

	out, err := exec.Command(python, "-c", "import sys; print(sys.version_info[1])").Output()
	if err != nil {
		t.Fatalf("querying %s version: %v", python, err)
	}
	minor, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parsing %s version %q: %v", python, out, err)
	}
	return minor
}

// skipOrFail skips when no usable Python environment is around, keeping `go
// test ./...` green for Go-only contributors -- unless FLOWBENCH_REQUIRE_PYTHON
// is set, which CI does so a provisioning failure can't pass as a quiet skip.
func skipOrFail(t *testing.T, format string, args ...any) {
	t.Helper()

	if os.Getenv("FLOWBENCH_REQUIRE_PYTHON") != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
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
