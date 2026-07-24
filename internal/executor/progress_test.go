package executor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
)

func pingFlow(url string) ir.Flow {
	return ir.Flow{Name: "ping", Steps: []ir.Step{{
		ID: "hit", Type: ir.StepCall,
		Call:   &ir.CallSpec{Method: "GET", URL: "/"},
		Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
	}}}
}

// The live view reads a run through the Progress callback, so its snapshots must
// arrive during the run, never go backwards, and agree with the final Result.
func TestRunStreamsProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Millisecond)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var snaps []executor.Progress
	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeStress, 4, 250*time.Millisecond),
		Flows:    []ir.Flow{pingFlow(srv.URL)},
		BaseURL:  srv.URL,
		Metrics:  20 * time.Millisecond, // tick often so the run yields several snapshots
		Progress: func(p executor.Progress) {
			mu.Lock()
			snaps = append(snaps, p)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(snaps) < 2 {
		t.Fatalf("want several progress snapshots during the run, got %d", len(snaps))
	}

	var last int64
	for _, s := range snaps {
		if s.Completed < last {
			t.Errorf("completed went backwards: %d after %d", s.Completed, last)
		}
		last = s.Completed
		if s.PeakVUs != 4 {
			t.Errorf("PeakVUs = %d, want 4", s.PeakVUs)
		}
	}
	if last == 0 {
		t.Error("progress never reported a completed flow-run")
	}
	// The final snapshot's live tally matches the run's own count.
	if last != int64(len(res.Samples)) {
		t.Errorf("final progress completed = %d, but the run recorded %d flow-runs", last, len(res.Samples))
	}
}

// A browser abort cancels the run's context. Like Ctrl-C, that must end the run
// early with a partial Result still flushed — and it is not the kill switch, so
// Aborted stays false.
func TestRunCancelYieldsPartialResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel() // stand in for POST /abort mid-run
	}()

	res, err := executor.Run(ctx, executor.Options{
		Schedule: holdSchedule(ir.ModeStress, 4, 5*time.Second), // long; the cancel ends it early
		Flows:    []ir.Flow{pingFlow(srv.URL)},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Samples) == 0 {
		t.Error("a cancelled run should still flush the work it completed")
	}
	if res.Aborted {
		t.Error("a context cancel is not the kill switch; Aborted must stay false")
	}
	if res.Duration >= 5*time.Second {
		t.Errorf("cancel should end the run well before its 5s schedule, ran %s", res.Duration)
	}
}
