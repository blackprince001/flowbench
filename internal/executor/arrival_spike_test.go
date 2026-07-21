package executor_test

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
)

// TestArrivalCapSpike is the measurement harness for issue #15: it compares two
// ways of holding a profile-level arrival cap and reports the accuracy that
// ADR 0013 decides on. It is skipped under -short. See docs/decisions/0013.
//
//	HARD — open-loop generator (the executor's open arrival model): requests
//	       are launched on a fixed 1/N schedule, decoupled from response time.
//	SOFT — self-paced closed loop: N/vus per VU via post-request sleep, the
//	       aggregate approximating N/s with no global coordination.
func TestArrivalCapSpike(t *testing.T) {
	if testing.Short() {
		t.Skip("arrival-cap measurement harness; run without -short")
	}
	// A realistic target: mostly fast, with a heavy tail. The tail is what
	// separates the two models — a slow response stalls a self-pacing VU, while
	// the open-loop generator keeps dispatching on schedule.
	var seq atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seq.Add(1)%8 == 0 {
			time.Sleep(100 * time.Millisecond)
		} else {
			time.Sleep(2 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const (
		target = 1000
		dur    = 2 * time.Second
		window = 100 * time.Millisecond
		warmup = 400 * time.Millisecond
	)

	hard := windowStats(hardOpen(srv.URL, target, 64, dur), dur, window, warmup)
	soft := windowStats(softPaced(srv.URL, target, 50, dur), dur, window, warmup)

	t.Logf("target = %d req/s over %s (steady-state after %s warmup, %s windows)", target, dur, warmup, window)
	t.Logf("HARD  open-loop generator : mean=%.0f/s peak-window=%.0f/s CoV=%.3f overshoot=%+.1f%%",
		hard.mean, hard.peak, hard.cov, pct(hard.peak, target))
	t.Logf("SOFT  self-paced closed   : mean=%.0f/s peak-window=%.0f/s CoV=%.3f overshoot=%+.1f%%",
		soft.mean, soft.peak, soft.cov, pct(soft.peak, target))

	if d := math.Abs(hard.mean-target) / target; d > 0.10 {
		t.Errorf("hard mean rate off by %.1f%%, want within 10%% of the cap", 100*d)
	}
	if hard.peak > 1.20*target {
		t.Errorf("hard peak window %.0f/s exceeds the cap by more than 20%%", hard.peak)
	}
}

func pct(v float64, target int) float64 { return 100 * (v - float64(target)) / float64(target) }

// hardOpen drives the executor's open (arrival-paced) model and returns each
// request's dispatch offset from the run start.
func hardOpen(url string, target, workers int, dur time.Duration) []time.Duration {
	sched := &planner.Schedule{
		Mode:       ir.ModeStress,
		Arrival:    planner.Open,
		Segments:   []planner.Segment{{Kind: planner.Hold, StartVUs: workers, EndVUs: workers, Duration: ir.Duration(dur)}},
		Stop:       planner.StopThresholds,
		ArrivalCap: &planner.Rate{Count: target, Per: ir.Duration(time.Second)},
		PeakVUs:    workers,
		Duration:   ir.Duration(dur),
	}
	flow := ir.Flow{Name: "hit", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/"},
	}}}
	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: sched, Flows: []ir.Flow{flow}, BaseURL: url, Metrics: -1,
	})
	if err != nil {
		return nil
	}
	offs := make([]time.Duration, len(res.Samples))
	for i, s := range res.Samples {
		offs[i] = s.Actual
	}
	return offs
}

// softPaced runs vus self-pacing goroutines, each firing every vus/target
// seconds, and returns each request's start offset from the run start.
func softPaced(url string, target, vus int, dur time.Duration) []time.Duration {
	period := time.Duration(int64(time.Second) * int64(vus) / int64(target))
	start := time.Now()
	deadline := start.Add(dur)
	var mu sync.Mutex
	var all []time.Duration
	var wg sync.WaitGroup
	for range vus {
		wg.Go(func() {
			client := &http.Client{}
			var local []time.Duration
			next := time.Now()
			for time.Now().Before(deadline) {
				local = append(local, time.Since(start))
				if req, err := http.NewRequest(http.MethodGet, url, nil); err == nil {
					if resp, err := client.Do(req); err == nil {
						io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}
				}
				next = next.Add(period)
				if d := time.Until(next); d > 0 {
					time.Sleep(d)
				} else {
					next = time.Now()
				}
			}
			mu.Lock()
			all = append(all, local...)
			mu.Unlock()
		})
	}
	wg.Wait()
	return all
}

type rateStats struct {
	mean float64 // req/s averaged over the post-warmup windows
	peak float64 // highest single-window rate (req/s) — the overshoot indicator
	cov  float64 // coefficient of variation across windows
}

// windowStats buckets request offsets into fixed windows over the steady-state
// span [warmup, total) and summarizes the per-window arrival rate.
func windowStats(offsets []time.Duration, total, window, warmup time.Duration) rateStats {
	n := int((total - warmup) / window)
	if n <= 0 {
		return rateStats{}
	}
	buckets := make([]int, n)
	for _, off := range offsets {
		if off < warmup || off >= total {
			continue
		}
		if idx := int((off - warmup) / window); idx >= 0 && idx < n {
			buckets[idx]++
		}
	}

	winSec := window.Seconds()
	rates := make([]float64, n)
	var sum, peak float64
	for i, c := range buckets {
		r := float64(c) / winSec
		rates[i] = r
		sum += r
		if r > peak {
			peak = r
		}
	}
	mean := sum / float64(n)

	var variance float64
	for _, r := range rates {
		d := r - mean
		variance += d * d
	}
	variance /= float64(n)

	cov := 0.0
	if mean > 0 {
		cov = math.Sqrt(variance) / mean
	}
	return rateStats{mean: mean, peak: peak, cov: cov}
}
