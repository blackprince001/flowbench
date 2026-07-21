package executor_test

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
)

// TestArrivalCapHoldsRate is the issue #16 acceptance: a profile with an
// arrival_cap, planned and run with abundant VUs, holds the request rate the
// target sees at N/s within the ±5% ADR 0013 tolerance — and does so
// independent of VU count.
func TestArrivalCapHoldsRate(t *testing.T) {
	const (
		target = 500 // req/s
		runFor = 1400 * time.Millisecond
		warmup = 300 * time.Millisecond
		window = 100 * time.Millisecond
	)

	for _, vus := range []int{32, 128} {
		t.Run(fmt.Sprintf("vus=%d", vus), func(t *testing.T) {
			var mu sync.Mutex
			var start time.Time
			var recv []time.Duration
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				if !start.IsZero() {
					recv = append(recv, time.Since(start))
				}
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			sched, err := planner.Plan(ir.Profile{
				Mode:       ir.ModeStress,
				VUs:        vus,
				Hold:       ir.Duration(runFor),
				ArrivalCap: fmt.Sprintf("%d/s", target),
			})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if sched.Arrival != planner.Open || sched.ArrivalCap == nil {
				t.Fatalf("planner did not produce an open, capped schedule: %+v", sched)
			}

			flow := ir.Flow{Name: "hit", Steps: []ir.Step{{
				ID: "call", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/"},
			}}}

			mu.Lock()
			start = time.Now()
			mu.Unlock()
			if _, err := executor.Run(context.Background(), executor.Options{
				Schedule: sched, Flows: []ir.Flow{flow}, BaseURL: srv.URL, Metrics: -1,
			}); err != nil {
				t.Fatalf("Run: %v", err)
			}

			mu.Lock()
			got := append([]time.Duration(nil), recv...)
			mu.Unlock()

			st := windowStats(got, runFor, window, warmup)
			miss := math.Abs(st.mean-target) / target
			t.Logf("vus=%d: observed mean=%.0f/s peak=%.0f/s (cap %d/s, miss %.1f%%)", vus, st.mean, st.peak, target, 100*miss)

			if miss > 0.05 {
				t.Errorf("observed rate %.0f/s misses the %d/s cap by %.1f%%, want within 5%%", st.mean, target, 100*miss)
			}
			if st.peak > 1.15*target {
				t.Errorf("peak window %.0f/s exceeds the cap ceiling of %d/s", st.peak, target)
			}
		})
	}
}
