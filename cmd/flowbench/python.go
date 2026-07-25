package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blackprince001/flowbench/internal/ir"
)

// minPythonMinor mirrors requires-python in sdk-python/pyproject.toml. The
// SDK uses 3.10 syntax, so an older interpreter fails at import.
const minPythonMinor = 10

// compilePythonScenario runs a Python flow file with FLOWBENCH_COMPILE_ONLY
// set, which makes flow.run(...) print the compiled IR as JSON instead of
// executing. Per ADR 0012, Python that only constructs the IR runs entirely
// on this (the Go) engine at full VU scale — this is the compile step, not
// a runtime bridge; the resulting *ir.Scenario feeds the exact same
// execute() pipeline a YAML flow does.
//
// Locating sdk-python assumes a dev checkout (a repo with sdk-python/ as a
// sibling of the flow file, findable by searching upward) — a packaged/
// installed flowbench binary has no such sibling. That's an accepted,
// documented limitation for now; $FLOWBENCH_SDK_PATH is the escape hatch.
func compilePythonScenario(path string) (*ir.Scenario, error) {
	sdkPath, err := findSDKPath(path)
	if err != nil {
		return nil, err
	}
	python, err := pythonInterpreter(sdkPath)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(python, path)
	cmd.Env = append(os.Environ(),
		"FLOWBENCH_COMPILE_ONLY=1",
		"PYTHONPATH="+filepath.Join(sdkPath, "src"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("compiling python flow %s: %w\n%s", path, err, stderr.String())
	}

	sc, err := ir.DecodeScenario(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("decoding python-compiled IR for %s: %w", path, err)
	}
	return sc, nil
}

// findSDKPath locates the sdk-python directory: $FLOWBENCH_SDK_PATH if set,
// else searching upward from the current directory and from the flow
// file's directory for a sibling named sdk-python.
func findSDKPath(scenarioPath string) (string, error) {
	if p := os.Getenv("FLOWBENCH_SDK_PATH"); p != "" {
		return p, nil
	}

	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if abs, err := filepath.Abs(scenarioPath); err == nil {
		starts = append(starts, filepath.Dir(abs))
	}
	for _, start := range starts {
		if found := searchUpward(start, "sdk-python"); found != "" {
			return found, nil
		}
	}
	return "", fmt.Errorf(
		"could not locate sdk-python/ (searched upward from the working directory and %s); "+
			"set $FLOWBENCH_SDK_PATH to sdk-python's directory", scenarioPath)
}

func searchUpward(start, name string) string {
	dir := start
	for {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// pythonInterpreter resolves the interpreter that compiles a Python flow:
// $FLOWBENCH_PYTHON, else the uv-managed sdk-python/.venv, else python3 on
// PATH — checked for a usable version either way, since a stale system
// python3 (macOS still ships 3.9) cannot even import the SDK.
func pythonInterpreter(sdkPath string) (string, error) {
	python := resolvePythonBinary(sdkPath)
	minor, err := pythonMinorVersion(python)
	if err != nil {
		return "", err
	}
	if minor < minPythonMinor {
		return "", fmt.Errorf(
			"%s is Python 3.%d, the SDK needs >= 3.%d; run `uv sync --project sdk-python`",
			python, minor, minPythonMinor)
	}
	return python, nil
}

func resolvePythonBinary(sdkPath string) string {
	if p := os.Getenv("FLOWBENCH_PYTHON"); p != "" {
		return p
	}
	for _, rel := range []string{filepath.Join("bin", "python"), filepath.Join("Scripts", "python.exe")} {
		candidate := filepath.Join(sdkPath, ".venv", rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "python3"
}

func pythonMinorVersion(python string) (int, error) {
	out, err := exec.Command(python, "-c", "import sys; print(sys.version_info[1])").Output()
	if err != nil {
		return 0, fmt.Errorf("no usable python interpreter (%s): %w", python, err)
	}
	minor, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parsing %s version %q: %w", python, out, err)
	}
	return minor, nil
}
