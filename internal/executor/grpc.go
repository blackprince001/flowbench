package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// runGRPC executes one unary call.
//
// It does not share the HTTP `request` path — gRPC has its own client, so
// there is no *adapters.Response to hand it — but it makes the same decisions
// in the same order, and that order is the contract: capture, then classify a
// throttle, then everything else. A step that got no answer is a failed call;
// a step that got a status is a step with a status, which the flow's own
// assertions judge exactly as they judge an HTTP one.
func (r *Runner) runGRPC(ctx context.Context, st *ir.Step, scope *Scope, anchor time.Time, it *Iteration) (*span.Span, bool, error) {
	if r.Protos == nil {
		return nil, false, fmt.Errorf("step %q is a grpc step but the runner has no proto registry", st.ID)
	}
	if r.GRPC == nil {
		return nil, false, fmt.Errorf("step %q is a grpc step but the runner has no grpc channels", st.ID)
	}

	method, err := r.Protos.Method(ctx, st.GRPC)
	if err != nil {
		return nil, false, fmt.Errorf("step %q: %w", st.ID, err)
	}
	req, err := adapters.BuildGRPCRequest(st.GRPC, scope.Resolve)
	if err != nil {
		return nil, false, fmt.Errorf("step %q: %w", st.ID, err)
	}

	address := r.grpcAddress(req.URL)
	if address == "" {
		return nil, false, fmt.Errorf("step %q names no url and the run has no base address to call", st.ID)
	}
	if r.Allow != nil {
		ok, err := r.Allow(address)
		if err != nil || !ok {
			sp := span.New(st.ID, time.Since(anchor))
			detail := fmt.Sprintf("host allow-list: %s is not an allowed target", address)
			if err != nil {
				detail = fmt.Sprintf("host allow-list check failed for %s: %v", address, err)
			}
			return sp, r.record(it, sp, sp, st, scope, detail), nil
		}
	}
	// The address plus the method is the whole identity of the call, and it is
	// literally what goes on the wire — so it is what auth signs, what the
	// allow-list cleared, and what a captured trace shows.
	req.URL = address + method.Path

	resp, sp, err := r.executeGRPC(ctx, st, &adapters.GRPCCall{Method: method, Request: req}, scope, anchor)

	if !captureDisabled(st) {
		var body []byte
		status, retryAfter := 0, ""
		if resp != nil {
			body, status, retryAfter = resp.Body, int(resp.Code), resp.RetryAfter
			if resp.Code != codes.OK {
				// The name, not the number: a reader who sees `8` in a trace has
				// to go looking, and one who sees it next to an HTTP status will
				// read it as HTTP 8.
				sp.SetStatusText(resp.StatusText())
			}
		}
		sp.SetRaw(req.Body, body)
		sp.SetCall(req.Method, req.URL, status, retryAfter)
	}

	// A throttle is classified before anything else is decided about the call,
	// including whether it reached the service at all (ADR 0006). It is the one
	// outcome that outranks the transport's own verdict, so an author who maps
	// a code their gateway sheds load with gets a throttle rather than a
	// failure.
	if resp != nil && isGRPCThrottled(resp.Code, st.Throttle) {
		detail := fmt.Sprintf("throttled: gRPC %s", resp.StatusText())
		if resp.Message != "" {
			detail += ": " + resp.Message
		}
		return sp, r.recordThrottle(it, sp, st, scope, detail), nil
	}
	if err != nil {
		return sp, r.record(it, sp, sp, st, scope, fmt.Sprintf("call failed: %v", err)), nil
	}

	return sp, r.evaluate(it, sp, st, scope, grpcTarget{resp: resp, latency: sp.Duration}, anchor), nil
}

// executeGRPC runs the call through the shared retry loop. The status a retry
// policy reads is the gRPC code, so `on_status: [14]` retries UNAVAILABLE the
// way `on_status: [503]` retries a bad gateway.
func (r *Runner) executeGRPC(ctx context.Context, st *ir.Step, call *adapters.GRPCCall, scope *Scope, anchor time.Time) (*adapters.GRPCResponse, *span.Span, error) {
	var resp *adapters.GRPCResponse
	try := func(ctx context.Context, name string) (*span.Span, int, string, error) {
		if err := r.applyAuth(ctx, st, call.Request, scope); err != nil {
			resp = nil
			return failedSpan(name, anchor), 0, "", err
		}
		var sp *span.Span
		var err error
		resp, sp, err = r.GRPC.Invoke(ctx, name, call, anchor)
		if resp == nil {
			return sp, 0, "", err
		}
		return sp, int(resp.Code), resp.RetryAfter, err
	}
	sp, err := r.withRetry(ctx, st, anchor, try)
	return resp, sp, err
}

// grpcAddress resolves the step's address. A gRPC step has no path — the
// method is the path — so a step that names no url is calling the target
// itself, and the base URL is the whole address.
func (r *Runner) grpcAddress(stepURL string) string {
	if stepURL != "" {
		return stepURL
	}
	return strings.TrimRight(r.BaseURL, "/")
}

// isGRPCThrottled reports whether a status code is a throttle.
//
// RESOURCE_EXHAUSTED is gRPC's 429 and is always one; a step's throttle block
// can name further codes its own gateway sheds load with. The HTTP rule is
// deliberately not reused: 429 is not a gRPC code, and code 8 is not HTTP 8.
func isGRPCThrottled(code codes.Code, spec *ir.ThrottleSpec) bool {
	if code == codes.ResourceExhausted {
		return true
	}
	if spec != nil {
		for _, s := range spec.Statuses {
			if codes.Code(s) == code {
				return true
			}
		}
	}
	return false
}

// grpcTarget adapts a gRPC response to eval.Target, so extraction and
// assertions are the same code they are for HTTP. The status is the numeric
// code — 0 is OK — because every status in the IR is an int, and the response
// message arrives as JSON, so JSONPath reads it unchanged.
type grpcTarget struct {
	resp    *adapters.GRPCResponse
	latency time.Duration
}

func (t grpcTarget) Status() int { return int(t.resp.Code) }
func (t grpcTarget) Header(name string) (string, bool) {
	if _, ok := t.resp.Headers[http.CanonicalHeaderKey(name)]; !ok {
		return "", false
	}
	return t.resp.Headers.Get(name), true
}
func (t grpcTarget) Body() []byte           { return t.resp.Body }
func (t grpcTarget) Latency() time.Duration { return t.latency }
