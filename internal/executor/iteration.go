package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/eval"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// Runner executes a single flow iteration: it chains steps, extracting values
// into the scope and injecting them into later requests, and records the span
// tree. The M2 VU pool drives many of these concurrently.
type Runner struct {
	Session *adapters.Session
	BaseURL string // prepended to relative call URLs
	Mode    ir.Mode
	Allow   func(rawURL string) (bool, error)
}

// Failure is one recorded assertion or extraction failure within an iteration.
type Failure struct {
	StepID string
	Detail string
}

// Iteration is the result of running one flow pass.
type Iteration struct {
	Spans     []*span.Span
	Outcome   span.Outcome
	Failures  []Failure
	Throttled bool // any step saw a throttle (feeds throttle_rate regardless of mode)
	Aborted   bool // an abort_run failure asks the whole run to stop
}

// RunFlow runs flow's steps in order against the runner's session, mutating
// scope as values are extracted. A transport or setup error stops the flow
// and is returned; assertion failures are recorded per the step's on_failure.
func (r *Runner) RunFlow(ctx context.Context, flow ir.Flow, scope *Scope) (*Iteration, error) {
	anchor := time.Now()
	it := &Iteration{Outcome: span.OutcomeOK}
	for i := range flow.Steps {
		st := &flow.Steps[i]
		sp, cont, err := r.runStep(ctx, st, scope, anchor, it)
		if sp != nil {
			it.Spans = append(it.Spans, sp)
			it.Outcome = worst(it.Outcome, sp.Outcome)
		}
		if err != nil {
			return it, err
		}
		if !cont {
			break
		}
	}
	return it, nil
}

func (r *Runner) runStep(ctx context.Context, st *ir.Step, scope *Scope, anchor time.Time, it *Iteration) (*span.Span, bool, error) {
	switch st.Type {
	case ir.StepCall:
		return r.runCall(ctx, st, scope, anchor, it)
	case ir.StepWait:
		sp := span.New(st.ID, time.Since(anchor))
		time.Sleep(time.Duration(st.Wait.Duration))
		sp.Duration = time.Since(anchor) - sp.Start
		return sp, true, nil
	default:
		return nil, false, fmt.Errorf("step %q: type %q is not executable in the M1 slice", st.ID, st.Type)
	}
}

func (r *Runner) runCall(ctx context.Context, st *ir.Step, scope *Scope, anchor time.Time, it *Iteration) (*span.Span, bool, error) {
	req, err := adapters.BuildRequest(st.Call, scope.Resolve)
	if err != nil {
		return nil, false, fmt.Errorf("step %q: %w", st.ID, err)
	}
	req.URL = r.resolveURL(req.URL)

	if r.Allow != nil {
		ok, err := r.Allow(req.URL)
		if err != nil || !ok {
			sp := span.New(st.ID, time.Since(anchor))
			detail := fmt.Sprintf("host allow-list: %s is not an allowed target", req.URL)
			if err != nil {
				detail = fmt.Sprintf("host allow-list check failed for %s: %v", req.URL, err)
			}
			return sp, r.record(it, sp, sp, st, scope, detail), nil
		}
	}

	resp, sp, err := r.executeCall(ctx, st, req, anchor)
	if !captureDisabled(st) {
		var respBody []byte
		status, retryAfter := 0, ""
		if resp != nil {
			respBody = resp.Body
			status = resp.Status
			// On a throttle this is the server saying how long it wanted to be
			// left alone — the one header worth keeping unconditionally.
			retryAfter = resp.Headers.Get("Retry-After")
		}
		sp.SetRaw(req.Body, respBody)
		sp.SetCall(req.Method, req.URL, status, retryAfter)
	}
	if err != nil {
		cont := r.record(it, sp, sp, st, scope, fmt.Sprintf("call failed: %v", err))
		return sp, cont, nil
	}

	// Classify the response before extraction or assertions: a throttle is its
	// own outcome, not an assertion failure, and there is nothing to extract
	// from it.
	if isThrottled(resp.Status, st.Throttle) {
		return sp, r.recordThrottle(it, sp, st, scope, resp.Status), nil
	}

	tgt := target{resp: resp, latency: sp.Duration}

	for _, ex := range st.Extract {
		v, found, err := eval.Extract(ex, tgt)
		child := sp.Child(ex.Var, time.Since(anchor))
		if err != nil || !found {
			child.Outcome = span.OutcomeFailed
			detail := fmt.Sprintf("extract %q found nothing", ex.Var)
			if err != nil {
				detail = fmt.Sprintf("extract %q: %v", ex.Var, err)
			}
			return sp, r.record(it, sp, child, st, scope, detail), nil
		}
		scope.Set(ex.Var, v)
	}

	cont := true
	for _, a := range st.Assert {
		res, err := eval.Assert(a, tgt, scope.Lookup)
		child := sp.Child(assertName(a), time.Since(anchor))
		if err != nil {
			child.Outcome = span.OutcomeFailed
			cont = r.record(it, sp, child, st, scope, fmt.Sprintf("assert %s: %v", assertName(a), err))
			break
		}
		if !res.Pass {
			child.Outcome = span.OutcomeFailed
			cont = r.record(it, sp, child, st, scope, res.Detail)
			if !cont {
				break
			}
		}
	}
	return sp, cont, nil
}

func (r *Runner) resolveURL(u string) string {
	if r.BaseURL == "" || strings.Contains(u, "://") {
		return u
	}
	return strings.TrimRight(r.BaseURL, "/") + "/" + strings.TrimLeft(u, "/")
}

// record marks the step failed, appends the (redacted) failure, and returns
// whether the flow should continue given the step's effective on_failure
// action. Details pass through the scope's secrets so an env-sourced value
// echoed into a response can never reach a stored failure.
//
// at is the span the failure is about, which is not always the step: an
// assertion or extraction failure belongs to its own child span, so a kept
// trace points at the span that failed rather than the one around it.
func (r *Runner) record(it *Iteration, sp, at *span.Span, st *ir.Step, sc *Scope, detail string) bool {
	detail = sc.Secrets().Redact(detail)
	sp.Outcome = span.OutcomeFailed
	at.SetFailure(detail)
	it.Failures = append(it.Failures, Failure{StepID: st.ID, Detail: detail})
	switch effectiveAction(st.OnFailure, r.Mode) {
	case ir.FailureAbortRun:
		it.Aborted = true
		return false
	case ir.FailureAbortFlow:
		return false
	default:
		return true
	}
}

// recordThrottle classifies a throttled response. The step span is always
// marked throttled and the iteration flagged so it feeds throttle_rate in every
// mode. Whether it also counts as a failure — recorded and subject to
// on_failure — follows the mode default unless the step overrides it.
func (r *Runner) recordThrottle(it *Iteration, sp *span.Span, st *ir.Step, sc *Scope, status int) bool {
	it.Throttled = true
	sp.Outcome = span.OutcomeThrottled
	if !throttleIsError(st.Throttle, r.Mode) {
		return true // data: classified, not a failure — keep going
	}
	detail := sc.Secrets().Redact(fmt.Sprintf("throttled: HTTP %d", status))
	sp.SetFailure(detail)
	it.Failures = append(it.Failures, Failure{StepID: st.ID, Detail: detail})
	switch effectiveAction(st.OnFailure, r.Mode) {
	case ir.FailureAbortRun:
		it.Aborted = true
		return false
	case ir.FailureAbortFlow:
		return false
	default:
		return true
	}
}

// effectiveAction resolves the step's on_failure, defaulting by mode: loud
// (abort the flow) in integration/system, recorded data in load/stress/soak.
func effectiveAction(action ir.FailureAction, mode ir.Mode) ir.FailureAction {
	if action != "" {
		return action
	}
	switch mode {
	case ir.ModeIntegration, ir.ModeSystem:
		return ir.FailureAbortFlow
	default:
		return ir.FailureRecord
	}
}

// captureDisabled reports whether a step opts out of payload capture.
func captureDisabled(st *ir.Step) bool {
	return st.Capture != nil && st.Capture.Payloads == ir.CaptureNever
}

func worst(a, b span.Outcome) span.Outcome {
	if a == span.OutcomeFailed || b == span.OutcomeFailed {
		return span.OutcomeFailed
	}
	if a == span.OutcomeThrottled || b == span.OutcomeThrottled {
		return span.OutcomeThrottled
	}
	return a
}

// assertName is a span-safe structural name for an assertion.
func assertName(a ir.Assertion) string {
	switch a.Source {
	case ir.AssertHeader:
		return "assert_header_" + sanitize(a.Key)
	case ir.AssertBody:
		return "assert_body_" + sanitize(a.Key)
	case ir.AssertVar:
		return "assert_var_" + sanitize(a.Key)
	default:
		return "assert_" + string(a.Source)
	}
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

// target adapts an adapters.Response to eval.Target.
type target struct {
	resp    *adapters.Response
	latency time.Duration
}

func (t target) Status() int { return t.resp.Status }
func (t target) Header(name string) (string, bool) {
	if _, ok := t.resp.Headers[http.CanonicalHeaderKey(name)]; !ok {
		return "", false
	}
	return t.resp.Headers.Get(name), true
}
func (t target) Body() []byte           { return t.resp.Body }
func (t target) Latency() time.Duration { return t.latency }
