package executor

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// defaultBackoff paces fixed/exponential retries whose policy sets no
// base_delay, and honor_retry_after responses that carry no Retry-After, so a
// retry loop never becomes a hot loop hammering the target.
const defaultBackoff = 100 * time.Millisecond

// maxBackoff caps a single computed backoff so an exponential policy with a
// large attempt count cannot wait absurdly long (or overflow).
const maxBackoff = 2 * time.Minute

// attempt is one try of a step: it attaches credentials and performs the call,
// returning the span it produced plus the two things the retry policy reads —
// the status and, for honor_retry_after, what the target asked for.
//
// It is a function rather than a method so the loop below is shared by every
// protocol that is call-shaped. An HTTP status and a gRPC status code are
// different vocabularies but the same decision: try again, or stop.
type attempt func(ctx context.Context, name string) (sp *span.Span, status int, retryAfter string, err error)

// withRetry runs a step, applying its retry policy when set. Without a policy
// it is a single try whose span is the step itself; with one it wraps each try
// and each backoff wait in a child span under the step, and the step's duration
// is the time-to-success including backoff — so retries add to measured latency
// rather than hiding it.
//
// Credentials are attached per try rather than once for the step (it is inside
// try that applyAuth is called), so a retried request carries a fresh HMAC
// signature and timestamp — and a token that refreshed mid-backoff — instead of
// replaying the first try's.
func (r *Runner) withRetry(ctx context.Context, st *ir.Step, anchor time.Time, try attempt) (*span.Span, error) {
	if st.Retry == nil {
		sp, _, _, err := try(ctx, st.ID)
		return sp, err
	}

	p := st.Retry
	attempts := max(p.MaxAttempts, 1)

	step := span.New(st.ID, time.Since(anchor))
	var err error
	for n := 1; ; n++ {
		name := fmt.Sprintf("attempt %d", n)
		var sp *span.Span
		var status int
		var retryAfter string
		sp, status, retryAfter, err = try(ctx, name)
		step.Children = append(step.Children, sp)

		// A failed try can still be retryable, and gRPC is why: it reports
		// "nothing was listening" as UNAVAILABLE, which is both an error and
		// the code a retry policy most wants to name. What is never retryable
		// is a try that produced no status at all — a refused connection, a
		// credential that would not resolve — because a policy lists statuses
		// and there is none to match.
		if n >= attempts || !retryable(p, status) {
			break
		}
		if !r.backoff(ctx, step, backoffDelay(p, n, retryAfter), anchor) {
			break // context cancelled mid-wait
		}
	}

	step.Duration = time.Since(anchor) - step.Start
	if err != nil {
		step.Outcome = span.OutcomeFailed
	}
	return step, err
}

// executeCall runs an HTTP-shaped step through the retry loop.
func (r *Runner) executeCall(ctx context.Context, st *ir.Step, req *adapters.Request, scope *Scope, anchor time.Time) (*adapters.Response, *span.Span, error) {
	var resp *adapters.Response
	try := func(ctx context.Context, name string) (*span.Span, int, string, error) {
		if err := r.applyAuth(ctx, st, req, scope); err != nil {
			resp = nil
			return failedSpan(name, anchor), 0, "", err
		}
		var sp *span.Span
		var err error
		resp, sp, err = r.Session.Do(ctx, name, req, anchor)
		if resp == nil {
			return sp, 0, "", err
		}
		return sp, resp.Status, resp.Headers.Get("Retry-After"), err
	}
	sp, err := r.withRetry(ctx, st, anchor, try)
	return resp, sp, err
}

// backoff sleeps for d, recorded as a child span, and reports whether the wait
// completed (false if ctx was cancelled).
func (r *Runner) backoff(ctx context.Context, step *span.Span, d time.Duration, anchor time.Time) bool {
	if d <= 0 {
		return true
	}
	w := step.Child("backoff", time.Since(anchor))
	ok := sleepCtx(ctx, d)
	w.Duration = time.Since(anchor) - w.Start
	return ok
}

// retryable reports whether a status is in the policy's on_status list.
func retryable(p *ir.RetryPolicy, status int) bool {
	return slices.Contains(p.OnStatus, status)
}

// backoffDelay computes the wait before the next attempt.
func backoffDelay(p *ir.RetryPolicy, attempt int, retryAfter string) time.Duration {
	switch p.Backoff {
	case ir.BackoffHonorRetryAfter:
		if d, ok := retryAfterDelay(retryAfter); ok {
			return clampBackoff(d)
		}
		return baseDelay(p)
	case ir.BackoffExponential:
		shift := min(attempt-1, 16)
		return clampBackoff(baseDelay(p) << shift)
	default: // fixed
		return baseDelay(p)
	}
}

func baseDelay(p *ir.RetryPolicy) time.Duration {
	if p.BaseDelay > 0 {
		return time.Duration(p.BaseDelay)
	}
	return defaultBackoff
}

func clampBackoff(d time.Duration) time.Duration {
	if d < 0 || d > maxBackoff {
		return maxBackoff
	}
	return d
}

// retryAfterDelay reads a Retry-After value as delta-seconds, an HTTP date, or
// the millisecond form gRPC's pushback header uses.
func retryAfterDelay(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if ms, ok := strings.CutSuffix(v, "ms"); ok {
		if n, err := strconv.Atoi(ms); err == nil {
			return time.Duration(max(n, 0)) * time.Millisecond, true
		}
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := max(time.Until(t), 0)
		return d, true
	}
	return 0, false
}

// sleepCtx waits for d or ctx cancellation, reporting true if d elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
