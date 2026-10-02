package executor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
)

func benchInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func benchDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// footprint is what runFootprintBenchmark measures: the same numbers
// docs/benchmarks/10k-vu-footprint.md reports, so two flow shapes run through
// it produce directly comparable figures.
type footprint struct {
	Iterations     int
	Throughput     float64
	PeakGoroutines int
	PeakActive     int
	PeakHeap       uint64
	PerVU          float64
	CPUCores       float64
}

// runFootprintBenchmark drives flow under the goroutine-per-VU pool at vus
// against a target answering after latency, and reports generator headroom
// (CPU, goroutines) and per-VU memory — the issue #21 harness, factored out so
// #105 can run the identical measurement over a flow shaped with a `use` step
// and compare the two directly rather than eyeballing two separate logs.
func runFootprintBenchmark(t *testing.T, label string, vus int, runFor, latency time.Duration, flow ir.Flow) footprint {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if latency > 0 {
			time.Sleep(latency)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, vus, runFor),
		Flows:    []ir.Flow{flow},
		BaseURL:  srv.URL,
		Metrics:  250 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var fp footprint
	var cpuStart, cpuEnd float64
	var atStart, atEnd time.Duration
	for i, m := range res.Metrics {
		if m.Goroutines > fp.PeakGoroutines {
			fp.PeakGoroutines = m.Goroutines
		}
		if m.ActiveVUs > fp.PeakActive {
			fp.PeakActive = m.ActiveVUs
		}
		if m.HeapAlloc > fp.PeakHeap {
			fp.PeakHeap = m.HeapAlloc
		}
		if i == 0 {
			cpuStart, atStart = m.CPUSeconds, m.At
		}
		cpuEnd, atEnd = m.CPUSeconds, m.At
	}

	fp.Iterations = res.Iterations
	fp.Throughput = float64(res.Iterations) / res.Duration.Seconds()
	if atEnd > atStart {
		fp.CPUCores = (cpuEnd - cpuStart) / (atEnd - atStart).Seconds()
	}
	fp.PerVU = float64(fp.PeakHeap-base.HeapAlloc) / float64(vus)

	t.Logf("=== FlowBench VU footprint benchmark: %s ===", label)
	t.Logf("VUs=%d  duration=%s  target-latency=%s  cores=%d", vus, runFor, latency, runtime.NumCPU())
	t.Logf("iterations=%d  throughput=%.0f iter/s", fp.Iterations, fp.Throughput)
	t.Logf("peak goroutines=%d  peak active VUs=%d", fp.PeakGoroutines, fp.PeakActive)
	t.Logf("peak heap=%.1f MiB  per-VU=%.1f KiB", float64(fp.PeakHeap)/(1<<20), fp.PerVU/1024)
	if n := runtime.NumCPU(); n > 0 {
		t.Logf("generator CPU=%.2f cores (%.0f%% of %d)", fp.CPUCores, 100*fp.CPUCores/float64(n), n)
	}
	if fp.Iterations == 0 {
		t.Fatal("no iterations ran")
	}
	return fp
}

// TestVUFootprintBenchmark is the issue #21 harness: it drives the declarative
// fast path at a configurable VU count and reports generator headroom (CPU,
// goroutines) and per-VU memory. Skipped under -short. Scale it up on the
// reference node with FLOWBENCH_BENCH_VUS=10000 (needs ulimit -n well above the
// VU count). Re-run to detect regressions.
func TestVUFootprintBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark harness; run without -short (FLOWBENCH_BENCH_VUS to scale)")
	}
	vus := benchInt("FLOWBENCH_BENCH_VUS", 1000)
	runFor := benchDur("FLOWBENCH_BENCH_DUR", 2*time.Second)
	// A realistic target has latency, so VUs spend most of their time waiting
	// on I/O — that is where generator headroom shows. An instant stub would
	// instead measure the raw request-rate ceiling.
	latency := benchDur("FLOWBENCH_BENCH_LATENCY", 100*time.Millisecond)

	flow := ir.Flow{Name: "hit", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/"},
	}}}

	fp := runFootprintBenchmark(t, "flat", vus, runFor, latency, flow)
	if fp.PeakGoroutines < vus {
		t.Errorf("peak goroutines %d < %d VUs: goroutine-per-VU did not hold", fp.PeakGoroutines, vus)
	}
	if fp.PerVU > 100*1024 {
		t.Errorf("per-VU heap %.1f KiB exceeds the 100 KiB sanity budget", fp.PerVU/1024)
	}
}

// TestVUFootprintBenchmarkWithUse is #105's F1: the identical single call,
// wrapped one level inside a `use` step, run at the same VUs/duration/latency.
// ir.UseSpec.Flow is a single *Flow the parser compiles once — runUse only
// builds one extra child Scope and one extra span per iteration, never
// recompiling or copying the child flow — so the overhead against the flat
// baseline should be small. Run both and compare the logged numbers; a
// material gap here is the regression F1 warns about, not something to
// explain away. See docs/benchmarks/10k-vu-use-overhead.md.
func TestVUFootprintBenchmarkWithUse(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark harness; run without -short (FLOWBENCH_BENCH_VUS to scale)")
	}
	vus := benchInt("FLOWBENCH_BENCH_VUS", 1000)
	runFor := benchDur("FLOWBENCH_BENCH_DUR", 2*time.Second)
	latency := benchDur("FLOWBENCH_BENCH_LATENCY", 100*time.Millisecond)

	child := &ir.Flow{Name: "hit_child", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/"},
	}}}
	flow := ir.Flow{Name: "hit_via_use", Steps: []ir.Step{{
		ID: "wrap", Type: ir.StepUse,
		Use: &ir.UseSpec{Path: "hit_child.flow.yaml", Flow: child},
	}}}

	fp := runFootprintBenchmark(t, "use-wrapped", vus, runFor, latency, flow)
	if fp.PeakGoroutines < vus {
		t.Errorf("peak goroutines %d < %d VUs: goroutine-per-VU did not hold", fp.PeakGoroutines, vus)
	}
	if fp.PerVU > 100*1024 {
		t.Errorf("per-VU heap %.1f KiB exceeds the 100 KiB sanity budget", fp.PerVU/1024)
	}
}
