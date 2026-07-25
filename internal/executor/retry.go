package executor

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
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

// executeCall runs a call step, applying its retry policy when set. Without a
// policy it is a single request whose span is the step itself; with one it
// wraps each attempt and each backoff wait in a child span under the step, and
// the step's duration is the time-to-success including backoff — so retries add
// to measured latency rather than hiding it.
//
// Credentials are attached per attempt rather than once for the step, so a
// retried request carries a fresh HMAC signature and timestamp — and a token
// that refreshed mid-backoff — instead of replaying the first attempt's.
func (r *Runner) executeCall(ctx context.Context, st *ir.Step, req *adapters.Request, scope *Scope, anchor time.Time) (*adapters.Response, *span.Span, error) {
	if st.Retry == nil {
		if err := r.applyAuth(ctx, st, req, scope); err != nil {
			return nil, failedSpan(st.ID, anchor), err
		}
		return r.Session.Do(ctx, st.ID, req, anchor)
	}

	p := st.Retry
	attempts := max(p.MaxAttempts, 1)

	step := span.New(st.ID, time.Since(anchor))
	var resp *adapters.Response
	var err error
	for attempt := 1; ; attempt++ {
		name := fmt.Sprintf("attempt %d", attempt)
		if err = r.applyAuth(ctx, st, req, scope); err != nil {
			resp = nil
			step.Children = append(step.Children, failedSpan(name, anchor))
			break
		}

		var aSp *span.Span
		resp, aSp, err = r.Session.Do(ctx, name, req, anchor)
		step.Children = append(step.Children, aSp)

		if err != nil || attempt >= attempts || !retryable(p, resp.Status) {
			break
		}
		if !r.backoff(ctx, step, backoffDelay(p, attempt, resp), anchor) {
			break // context cancelled mid-wait
		}
	}

	step.Duration = time.Since(anchor) - step.Start
	if err != nil {
		step.Outcome = span.OutcomeFailed
	}
	return resp, step, err
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
func backoffDelay(p *ir.RetryPolicy, attempt int, resp *adapters.Response) time.Duration {
	switch p.Backoff {
	case ir.BackoffHonorRetryAfter:
		if d, ok := retryAfter(resp); ok {
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

// retryAfter reads a Retry-After header as delta-seconds or an HTTP date.
func retryAfter(resp *adapters.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	v := resp.Headers.Get("Retry-After")
	if v == "" {
		return 0, false
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
