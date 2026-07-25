package executor_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/data"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
	"github.com/blackprince001/flowbench/internal/span"
)

func holdSchedule(mode ir.Mode, vus int, d time.Duration) *planner.Schedule {
	return &planner.Schedule{
		Mode:    mode,
		Arrival: planner.Closed,
		Segments: []planner.Segment{
			{Kind: planner.Hold, StartVUs: vus, EndVUs: vus, Duration: ir.Duration(d)},
		},
		Stop:     planner.StopDuration,
		PeakVUs:  vus,
		Duration: ir.Duration(d),
	}
}

// TestPoolIsolationUnderLoad drives hundreds of VUs through a login→check flow.
// The server issues a per-login session cookie and rejects any /check whose
// cookie disagrees with the X-Expect header the flow carries over from its own
// login. A shared cookie jar or leaked variable scope across VUs would surface
// as a 409 and a recorded failure, so a clean run is the isolation proof.
func TestPoolIsolationUnderLoad(t *testing.T) {
	var counter int64
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&counter, 1)
		http.SetCookie(w, &http.Cookie{Name: "sess", Value: strconv.FormatInt(n, 10)})
		fmt.Fprintf(w, `{"n": %d}`, n)
	})
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("sess")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if c.Value != r.Header.Get("X-Expect") {
			w.WriteHeader(http.StatusConflict)
			return
		}
		fmt.Fprintf(w, `{"n": %s}`, c.Value)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	flow := ir.Flow{Name: "session", Steps: []ir.Step{
		{
			ID: "login", Type: ir.StepCall,
			Call:    &ir.CallSpec{Method: "POST", URL: "/login"},
			Extract: []ir.Extraction{{Var: "n", Path: "$.n"}},
			Assert:  []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
		},
		{
			ID: "check", Type: ir.StepCall,
			Call:   &ir.CallSpec{Method: "GET", URL: "/check", Headers: map[string]string{"X-Expect": "{{ n }}"}},
			Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
		},
	}}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, 200, 300*time.Millisecond),
		Flows:    []ir.Flow{flow},
		BaseURL:  srv.URL,
		Metrics:  30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Aborted {
		t.Fatal("run aborted unexpectedly")
	}
	if res.Failed() != 0 {
		t.Fatalf("isolation broke: %d failed flow-runs of %d", res.Failed(), len(res.Samples))
	}
	if res.Iterations < 200 {
		t.Fatalf("only %d iterations at 200 VUs — VUs did not run", res.Iterations)
	}
	if res.Outcomes[span.OutcomeOK] != len(res.Samples) {
		t.Fatalf("outcomes = %v, want all ok", res.Outcomes)
	}

	var peak int
	for _, m := range res.Metrics {
		if m.ActiveVUs > peak {
			peak = m.ActiveVUs
		}
	}
	if len(res.Metrics) == 0 || peak == 0 {
		t.Fatalf("no self-metrics captured: %d samples, peak active %d", len(res.Metrics), peak)
	}
}

// TestPoolDataRowIsolation checks that concurrent VUs each draw their own row:
// a 24-row unique-per-vu pool cycled under load should place every id on the
// wire, none corrupted.
func TestPoolDataRowIsolation(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		mu.Lock()
		seen[id] = true
		mu.Unlock()
	}))
	defer srv.Close()

	rows := make([]data.Row, 24)
	for i := range rows {
		rows[i] = data.Row{"id": strconv.Itoa(i)}
	}
	pool, err := data.New(ir.DataPool{Name: "ids", Distribution: ir.DistributeUniquePerVU, OnExhausted: ir.ExhaustCycle}, rows, 1)
	if err != nil {
		t.Fatalf("data.New: %v", err)
	}

	flow := ir.Flow{Name: "use", Data: "ids", Steps: []ir.Step{{
		ID: "use", Type: ir.StepCall,
		Call:   &ir.CallSpec{Method: "GET", URL: "/use", Query: map[string]string{"id": "{{ ids.id }}"}},
		Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
	}}}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, 20, 250*time.Millisecond),
		Flows:    []ir.Flow{flow},
		Pools:    map[string]*data.Pool{"ids": pool},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed() != 0 {
		t.Fatalf("%d failed flow-runs", res.Failed())
	}
	mu.Lock()
	got := len(seen)
	mu.Unlock()
	if got != 24 {
		t.Fatalf("saw %d distinct ids, want all 24 drawn under load", got)
	}
}

// TestPoolAbort verifies the kill switch: an abort_run failure stops the whole
// pool well before the schedule's deadline.
func TestPoolAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "boom", Steps: []ir.Step{{
		ID: "hit", Type: ir.StepCall,
		Call:      &ir.CallSpec{Method: "GET", URL: "/"},
		Assert:    []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
		OnFailure: ir.FailureAbortRun,
	}}}

	start := time.Now()
	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, 10, 5*time.Second),
		Flows:    []ir.Flow{flow},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Aborted {
		t.Fatal("expected the run to abort")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("abort took %s — kill switch did not stop the pool promptly", elapsed)
	}
}

// TestPoolStopOnce runs each VU exactly one iteration for the once-shaped modes.
func TestPoolStopOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	flow := ir.Flow{Name: "smoke", Steps: []ir.Step{{
		ID: "ping", Type: ir.StepCall,
		Call:   &ir.CallSpec{Method: "GET", URL: "/"},
		Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
	}}}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: &planner.Schedule{Mode: ir.ModeIntegration, Arrival: planner.Closed, Stop: planner.StopOnce, PeakVUs: 3},
		Flows:    []ir.Flow{flow},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Iterations != 3 {
		t.Fatalf("iterations = %d, want 3 (one per VU)", res.Iterations)
	}
	if res.Failed() != 0 {
		t.Fatalf("%d failed", res.Failed())
	}
}

// TestPoolCoordinatedOmission confirms honest latency under a paced (open)
// model: one worker that cannot keep up with the arrival rate must report the
// queueing delay as latency, not just the fast per-request service time.
func TestPoolCoordinatedOmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "slow", Steps: []ir.Step{{
		ID: "hit", Type: ir.StepCall,
		Call:   &ir.CallSpec{Method: "GET", URL: "/"},
		Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
	}}}

	sched := &planner.Schedule{
		Mode:       ir.ModeStress,
		Arrival:    planner.Open,
		Segments:   []planner.Segment{{Kind: planner.Hold, StartVUs: 1, EndVUs: 1, Duration: ir.Duration(400 * time.Millisecond)}},
		Stop:       planner.StopThresholds,
		ArrivalCap: &planner.Rate{Count: 100, Per: ir.Duration(time.Second)}, // 100/s: far above one worker's ~50/s
		PeakVUs:    1,
		Duration:   ir.Duration(400 * time.Millisecond),
	}

	res, err := executor.Run(context.Background(), executor.Options{Schedule: sched, Flows: []ir.Flow{flow}, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Samples) == 0 {
		t.Fatal("no samples")
	}

	var maxLat, maxSvc time.Duration
	for _, s := range res.Samples {
		if l := s.Latency(); l > maxLat {
			maxLat = l
		}
		if s.Service > maxSvc {
			maxSvc = s.Service
		}
	}
	if maxLat < 3*maxSvc {
		t.Fatalf("coordinated omission not accounted: max latency %s barely exceeds max service %s", maxLat, maxSvc)
	}
}

// A target that accepts a connection and then says nothing must not hold its VU
// for the adapter's default. The run's own budget ends the call, and the
// flow-run is recorded as a failure rather than a stall.
func TestRequestTimeoutBoundsAHungCall(t *testing.T) {
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hung.Close()

	start := time.Now()
	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, 1, 900*time.Millisecond),
		Flows: []ir.Flow{{Name: "hang", Steps: []ir.Step{{
			ID: "wait_forever", Type: ir.StepCall,
			Call: &ir.CallSpec{Method: http.MethodGet, URL: hung.URL + "/"},
		}}}},
		RequestTimeout: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the run took %s: the budget did not bound the call", elapsed)
	}
	if res.Failed() == 0 {
		t.Error("a call that never answered should be recorded as a failure")
	}
	// Several iterations fit inside the hold, which they could not if each one
	// waited out the adapter's 30s default.
	if len(res.Samples) < 3 {
		t.Errorf("only %d flow-runs completed; the timeout is not being applied", len(res.Samples))
	}
}
