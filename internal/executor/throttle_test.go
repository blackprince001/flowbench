package executor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
)

func statusStub(code int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	}))
}

func callFlow(throttle *ir.ThrottleSpec) ir.Flow {
	return ir.Flow{Name: "hit", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall,
		Call:     &ir.CallSpec{Method: "GET", URL: "/"},
		Assert:   []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
		Throttle: throttle,
	}}}
}

// TestThrottleStressVsIntegration is the issue #13 acceptance: the same 429s
// are data in a stress run (throttle_rate up, error_rate flat) and failures in
// an integration run.
func TestThrottleStressVsIntegration(t *testing.T) {
	srv := statusStub(http.StatusTooManyRequests)
	defer srv.Close()
	flow := callFlow(nil)

	stress, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeStress, 20, 200*time.Millisecond),
		Flows:    []ir.Flow{flow},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("stress Run: %v", err)
	}
	if stress.ThrottleRate() == 0 {
		t.Fatal("stress: expected a nonzero throttle_rate")
	}
	if stress.ErrorRate() != 0 {
		t.Fatalf("stress: 429s must not count as errors, got error_rate %.2f", stress.ErrorRate())
	}
	if stress.Failed() != 0 || stress.Aborted {
		t.Fatalf("stress: failed=%d aborted=%v, want a clean throttled run", stress.Failed(), stress.Aborted)
	}
	if stress.Throttled() != len(stress.Samples) {
		t.Fatalf("stress: %d of %d flow-runs throttled, want all", stress.Throttled(), len(stress.Samples))
	}

	integ, err := executor.Run(context.Background(), executor.Options{
		Schedule: &planner.Schedule{Mode: ir.ModeIntegration, Arrival: planner.Closed, Stop: planner.StopOnce, PeakVUs: 5},
		Flows:    []ir.Flow{flow},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("integration Run: %v", err)
	}
	if integ.ErrorRate() == 0 {
		t.Fatal("integration: the same 429s must fail the run")
	}
	if integ.ThrottleRate() == 0 {
		t.Fatal("integration: throttle_rate is still tracked everywhere")
	}
}

// TestThrottleMappingAndOverride covers author-mapped statuses and the per-step
// as_error override.
func TestThrottleMappingAndOverride(t *testing.T) {
	srv := statusStub(http.StatusServiceUnavailable) // 503
	defer srv.Close()

	// 503 is not a throttle by default: in stress it is a plain failed assertion.
	plain, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeStress, 10, 150*time.Millisecond),
		Flows:    []ir.Flow{callFlow(nil)},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if plain.ThrottleRate() != 0 || plain.ErrorRate() == 0 {
		t.Fatalf("unmapped 503: want error, throttle 0 — got error_rate %.2f throttle_rate %.2f", plain.ErrorRate(), plain.ThrottleRate())
	}

	// Map 503 to throttled: now it is data in stress.
	mapped, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeStress, 10, 150*time.Millisecond),
		Flows:    []ir.Flow{callFlow(&ir.ThrottleSpec{Statuses: []int{503}})},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mapped.ThrottleRate() == 0 || mapped.ErrorRate() != 0 {
		t.Fatalf("mapped 503: want throttled data — got error_rate %.2f throttle_rate %.2f", mapped.ErrorRate(), mapped.ThrottleRate())
	}

	// Override as_error: a mapped throttle counts as an error even in stress.
	asError := true
	strict, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeStress, 10, 150*time.Millisecond),
		Flows:    []ir.Flow{callFlow(&ir.ThrottleSpec{Statuses: []int{503}, AsError: &asError})},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strict.ErrorRate() == 0 || strict.ThrottleRate() == 0 {
		t.Fatalf("override: want throttle counted as error — got error_rate %.2f throttle_rate %.2f", strict.ErrorRate(), strict.ThrottleRate())
	}
}
