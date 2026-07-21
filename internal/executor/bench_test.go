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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if latency > 0 {
			time.Sleep(latency)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "hit", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/"},
	}}}

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

	var peakGoroutines, peakActive int
	var peakHeap uint64
	var cpuStart, cpuEnd float64
	var atStart, atEnd time.Duration
	for i, m := range res.Metrics {
		if m.Goroutines > peakGoroutines {
			peakGoroutines = m.Goroutines
		}
		if m.ActiveVUs > peakActive {
			peakActive = m.ActiveVUs
		}
		if m.HeapAlloc > peakHeap {
			peakHeap = m.HeapAlloc
		}
		if i == 0 {
			cpuStart, atStart = m.CPUSeconds, m.At
		}
		cpuEnd, atEnd = m.CPUSeconds, m.At
	}

	throughput := float64(res.Iterations) / res.Duration.Seconds()
	cpuCores := 0.0
	if atEnd > atStart {
		cpuCores = (cpuEnd - cpuStart) / (atEnd - atStart).Seconds()
	}
	perVU := float64(peakHeap-base.HeapAlloc) / float64(vus)

	t.Log("=== FlowBench VU footprint benchmark ===")
	t.Logf("VUs=%d  duration=%s  target-latency=%s  cores=%d", vus, runFor, latency, runtime.NumCPU())
	t.Logf("iterations=%d  throughput=%.0f iter/s", res.Iterations, throughput)
	t.Logf("peak goroutines=%d  peak active VUs=%d", peakGoroutines, peakActive)
	t.Logf("peak heap=%.1f MiB  per-VU=%.1f KiB", float64(peakHeap)/(1<<20), perVU/1024)
	if n := runtime.NumCPU(); n > 0 {
		t.Logf("generator CPU=%.2f cores (%.0f%% of %d)", cpuCores, 100*cpuCores/float64(n), n)
	}

	if res.Iterations == 0 {
		t.Fatal("no iterations ran")
	}
	if peakGoroutines < vus {
		t.Errorf("peak goroutines %d < %d VUs: goroutine-per-VU did not hold", peakGoroutines, vus)
	}
	if perVU > 100*1024 {
		t.Errorf("per-VU heap %.1f KiB exceeds the 100 KiB sanity budget", perVU/1024)
	}
}
