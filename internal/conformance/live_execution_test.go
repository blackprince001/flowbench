package conformance

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/blackprince001/flowbench/internal/span"
	"github.com/blackprince001/flowbench/internal/store"
)

// buildFlowbenchOnce builds the flowbench binary a single time per test
// process (multiple tests in this package may want it) and returns its path.
var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

func flowbenchBinary(t *testing.T, repoRoot string) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "flowbench-conformance-bin")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, "flowbench")
		cmd := exec.Command("go", "build", "-o", builtBin, "./cmd/flowbench")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("building flowbench: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Skipf("could not build flowbench binary: %v", buildErr)
	}
	return builtBin
}

// liveCheckoutStub serves the authenticated_checkout_live fixture's three
// endpoints, throttling the first /orders hit once so both entry points
// exercise the retry path identically.
func liveCheckoutStub() (*httptest.Server, *int32) {
	var orderHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"access_token":"tok-live"}}`))
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&orderHits, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"data":{"id":"ord-live-1"}}`))
	})
	mux.HandleFunc("POST /orders/{id}/pay", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	return httptest.NewServer(mux), &orderHits
}

// TestAuthenticatedCheckoutLiveExecutionParity is issue #25's acceptance
// check: the same flow produces equivalent run artifacts whether run
// through the CLI (flowbench run <flow>.py, which compiles to IR and
// executes on the Go engine) or directly (python3 <flow>.py, ADR 0012's
// Python-driven path writing straight to the run store).
func TestAuthenticatedCheckoutLiveExecutionParity(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	python := pythonInterpreter(t, repoRoot)
	bin := flowbenchBinary(t, repoRoot)
	flowsDir := filepath.Join(repoRoot, "tests", "flows")

	srv, orderHits := liveCheckoutStub()
	defer srv.Close()

	targetsDir := t.TempDir()
	targetPath := filepath.Join(targetsDir, "conformance.yaml")
	if err := os.WriteFile(targetPath,
		[]byte("name: conformance\nbase_urls:\n  - "+srv.URL+"\n"), 0o600); err != nil {
		t.Fatalf("writing target file: %v", err)
	}

	// Entry point A: flowbench run on the YAML fixture -- compiles nothing,
	// runs directly on the Go engine's executeOnce.
	storeGo := t.TempDir()
	runGo := exec.Command(bin, "run", "authenticated_checkout_live.flow.yaml",
		"--target", "conformance", "--targets-dir", targetsDir, "--store", storeGo)
	runGo.Dir = flowsDir
	if out, err := runGo.CombinedOutput(); err != nil {
		t.Fatalf("flowbench run (yaml): %v\n%s", err, out)
	}

	atomic.StoreInt32(orderHits, 0) // fair, independent retry exercise for the second run

	// Entry point B: python3 authenticated_checkout_live.py, direct
	// execution -- ADR 0012's Python-driven producer.
	storePy := t.TempDir()
	runPy := exec.Command(python, "authenticated_checkout_live.py")
	runPy.Dir = flowsDir
	runPy.Env = append(os.Environ(),
		"PYTHONPATH="+filepath.Join(repoRoot, "sdk-python", "src"),
		"FLOWBENCH_BIN="+bin,
		"FLOWBENCH_TARGET=conformance",
		"FLOWBENCH_TARGETS_DIR="+targetsDir,
		"FLOWBENCH_STORE="+storePy,
	)
	if out, err := runPy.CombinedOutput(); err != nil {
		t.Fatalf("python direct execution: %v\n%s", err, out)
	}

	stGo, err := store.Open(storeGo)
	if err != nil {
		t.Fatalf("opening go store: %v", err)
	}
	stPy, err := store.Open(storePy)
	if err != nil {
		t.Fatalf("opening python store: %v", err)
	}

	runsGo, err := stGo.List()
	if err != nil || len(runsGo) != 1 {
		t.Fatalf("go store: %d runs, err %v", len(runsGo), err)
	}
	runsPy, err := stPy.List()
	if err != nil || len(runsPy) != 1 {
		t.Fatalf("python store: %d runs, err %v", len(runsPy), err)
	}
	mGo, mPy := runsGo[0], runsPy[0]

	if mGo.Iterations != mPy.Iterations || mGo.FlowRuns != mPy.FlowRuns {
		t.Errorf("iteration counts diverged: go=%d/%d python=%d/%d",
			mGo.Iterations, mGo.FlowRuns, mPy.Iterations, mPy.FlowRuns)
	}
	if mGo.ErrorRate != mPy.ErrorRate {
		t.Errorf("error_rate diverged: go=%v python=%v", mGo.ErrorRate, mPy.ErrorRate)
	}
	if mGo.ThrottleRate != mPy.ThrottleRate {
		t.Errorf("throttle_rate diverged: go=%v python=%v", mGo.ThrottleRate, mPy.ThrottleRate)
	}

	tracesGo, err := stGo.LoadTraces(mGo.ID)
	if err != nil {
		t.Fatalf("loading go traces: %v", err)
	}
	tracesPy, err := stPy.LoadTraces(mPy.ID)
	if err != nil {
		t.Fatalf("loading python traces: %v", err)
	}
	outcomesGo := rootOutcomes(tracesGo)
	outcomesPy := rootOutcomes(tracesPy)
	if !reflect.DeepEqual(outcomesGo, outcomesPy) {
		t.Errorf("per-iteration outcomes diverged: go=%v python=%v", outcomesGo, outcomesPy)
	}
}

// rootOutcomes is each iteration's root-span outcome, in order -- the
// coarse-grained "did the same rows pass/fail the same way" comparison.
func rootOutcomes(traces []*span.Span) []string {
	out := make([]string, len(traces))
	for i, tr := range traces {
		out[i] = string(tr.Outcome)
	}
	return out
}
