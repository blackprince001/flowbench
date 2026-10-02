package executor_test

import (
	"context"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/report"
)

// limiter is examples/load-local/stub's token bucket, reused here so the test
// throttles the same way the real local target does.
type limiter struct {
	mu     sync.Mutex
	tokens float64
	rate   float64
	burst  float64
	last   time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{tokens: burst, rate: rate, burst: burst, last: time.Now()}
}

func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now
	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}

// rateLimitedCheckoutServer answers /auth/login unconditionally (the `use`
// step's own target never throttles) and rate-limits /orders to ~200/s with a
// burst of 200, plus a small real error floor — the #105 acceptance shape: a
// target that actually throttles the step that comes after a used flow.
func rateLimitedCheckoutServer(t *testing.T) *httptest.Server {
	t.Helper()
	lim := newLimiter(200, 200)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"access_token":"tok-abc"}}`))
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		if !lim.allow() {
			// Retry-After: 0 keeps the retry loop from sleeping, so a short
			// test hold still drives enough iterations to exhaust the burst
			// and hold steady past it.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if rand.Float64() < 0.005 {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"data":{"id":"ord-1"}}`))
	})
	return httptest.NewServer(mux)
}

// checkoutUseStressFlow is the authenticated_checkout_use shape (tests/flows/
// authenticated_checkout_use.flow.yaml): login behind a `use` step, then a
// call that retries 429/503 honoring Retry-After.
func checkoutUseStressFlow() ir.Flow {
	return ir.Flow{Name: "authenticated_checkout_use", Steps: []ir.Step{
		{
			ID: "auth", Type: ir.StepUse,
			Use: &ir.UseSpec{
				Path: "login.flow.yaml",
				Flow: loginFlow(),
				With: map[string]string{"email": "a@b.com", "password": "pw"},
			},
		},
		{
			ID:   "create_order",
			Type: ir.StepCall,
			Call: &ir.CallSpec{
				Method: "POST", URL: "/orders",
				Headers: map[string]string{"Authorization": "Bearer {{ auth.token }}"},
			},
			Retry: &ir.RetryPolicy{
				OnStatus: []int{429, 503}, Backoff: ir.BackoffHonorRetryAfter, MaxAttempts: 5,
			},
			Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
		},
	}}
}

// TestUsedFlowCheckoutHoldsUnderStress is #105's acceptance: the rewritten
// checkout flow — login behind a `use` step — runs in stress mode against a
// rate-limited target with throttle_rate and error_rate kept apart, exactly as
// a flat flow does, and still folds correctly for the flame/waterfall views.
func TestUsedFlowCheckoutHoldsUnderStress(t *testing.T) {
	srv := rateLimitedCheckoutServer(t)
	defer srv.Close()

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeStress, 40, 500*time.Millisecond),
		Flows:    []ir.Flow{checkoutUseStressFlow()},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.ThrottleRate() == 0 {
		t.Fatal("want throttling: 40 VUs against a ~200/s limit with retries should hit it")
	}
	if res.Aborted {
		t.Fatal("stress: a real error floor must not abort the run")
	}
	// The stub's own ~0.5% random 500s are real failures and do count against
	// error_rate; the point is that the much larger throttle_rate does not
	// inflate it (ADR 0006).
	if res.ErrorRate() > 0.05 {
		t.Fatalf("error_rate = %.3f, want it near the stub's ~0.5%% real failure floor — throttles must not count against it", res.ErrorRate())
	}

	// At least one retained trace shows the used flow's step nested under the
	// use step, and that nesting still folds to real frames.
	var sawNesting bool
	for _, tr := range res.Traces {
		if tr == nil || len(tr.Children) == 0 {
			continue
		}
		auth := tr.Children[0]
		if auth.Name == "auth" && len(auth.Children) == 1 && auth.Children[0].Name == "login" {
			sawNesting = true
			break
		}
	}
	if !sawNesting {
		t.Error("no retained trace shows login nested under auth — expected every iteration to nest the same way")
	}

	frames := report.FlameFrames(res.Folded)
	var sawLoginFrame bool
	for _, f := range frames {
		if f.Name == "login" && f.Kind == report.KindStep {
			sawLoginFrame = true
			break
		}
	}
	if !sawLoginFrame {
		t.Error("folded flame data has no login step frame — nested use steps must fold like any other step")
	}
}
