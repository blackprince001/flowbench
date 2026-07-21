package executor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// spanChildren returns the direct children of the step span whose names start
// with the given prefix.
func namedChildren(sp *span.Span, prefix string) []*span.Span {
	var out []*span.Span
	for _, c := range sp.Children {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// TestRetryHonorsRetryAfterThenSucceeds is the issue #14 acceptance: a stub
// that returns 429 with Retry-After then 200 yields one span per attempt, a
// backoff span at the honored delay, and a step whose duration includes it.
func TestRetryHonorsRetryAfterThenSucceeds(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&hits, 1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{{
		ID: "charge", Type: ir.StepCall,
		Call: &ir.CallSpec{Method: "GET", URL: "/"},
		Retry: &ir.RetryPolicy{
			OnStatus: []int{429}, Backoff: ir.BackoffHonorRetryAfter, MaxAttempts: 3,
		},
		Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
	}}}

	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeLoad}
	it, err := r.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if len(it.Failures) != 0 || it.Outcome != span.OutcomeOK {
		t.Fatalf("want a clean success after retry, got outcome %q failures %+v", it.Outcome, it.Failures)
	}

	step := it.Spans[0]
	if got := len(namedChildren(step, "attempt")); got != 2 {
		t.Fatalf("attempt spans = %d, want 2 (429 then 200)", got)
	}
	backoffs := namedChildren(step, "backoff")
	if len(backoffs) != 1 {
		t.Fatalf("backoff spans = %d, want 1", len(backoffs))
	}
	if backoffs[0].Duration < 900*time.Millisecond {
		t.Errorf("backoff = %s, want ~1s (the honored Retry-After)", backoffs[0].Duration)
	}
	if step.Duration < 900*time.Millisecond {
		t.Errorf("step duration = %s, want to include the backoff so retries don't hide latency", step.Duration)
	}
}

// TestRetryTerminatesAtMaxAttempts confirms a persistently-429 target stops at
// the bound, leaving the final response to be classified as throttled.
func TestRetryTerminatesAtMaxAttempts(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{{
		ID: "charge", Type: ir.StepCall,
		Call: &ir.CallSpec{Method: "GET", URL: "/"},
		Retry: &ir.RetryPolicy{
			OnStatus: []int{429}, Backoff: ir.BackoffFixed, MaxAttempts: 3,
			BaseDelay: ir.Duration(10 * time.Millisecond),
		},
	}}}

	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeStress}
	it, err := r.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Fatalf("server saw %d requests, want exactly max_attempts (3)", got)
	}
	step := it.Spans[0]
	if got := len(namedChildren(step, "attempt")); got != 3 {
		t.Fatalf("attempt spans = %d, want 3", got)
	}
	// Final response is still 429: classified throttled, and in stress it is data.
	if !it.Throttled || it.Outcome != span.OutcomeThrottled {
		t.Fatalf("want throttled classification after exhausting retries, got throttled=%v outcome=%q", it.Throttled, it.Outcome)
	}
}

// TestRetryStopsOnNonRetryableStatus checks the loop exits as soon as a status
// outside on_status arrives, without consuming the remaining attempts.
func TestRetryStopsOnNonRetryableStatus(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&hits, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // 500: not in on_status
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "f", Steps: []ir.Step{{
		ID: "charge", Type: ir.StepCall,
		Call: &ir.CallSpec{Method: "GET", URL: "/"},
		Retry: &ir.RetryPolicy{
			OnStatus: []int{429}, Backoff: ir.BackoffFixed, MaxAttempts: 5,
			BaseDelay: ir.Duration(5 * time.Millisecond),
		},
		Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
	}}}

	r := &executor.Runner{Session: adapters.NewSession(adapters.SessionOptions{}), BaseURL: srv.URL, Mode: ir.ModeLoad}
	it, err := r.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Fatalf("server saw %d requests, want 2 (429 then a non-retryable 500)", got)
	}
	if len(it.Failures) == 0 {
		t.Fatal("the 500 should fail the status assertion")
	}
}
