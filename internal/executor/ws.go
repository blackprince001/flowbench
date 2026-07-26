package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/eval"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// Structural span names a ws step's work folds under. Fixed strings, like every
// other span name, so this run's flame graph is comparable to the last one's.
const (
	wsOpenSpan    = "ws_open"
	wsSendSpan    = "ws_send"
	wsReceiveSpan = "ws_receive"
)

// Bounds on what a timed-out receive reports back. A feed can push thousands of
// frames while a step waits for one that never comes; a few of them explain the
// mismatch, and the rest would only bloat the run store.
const (
	maxReportedFrames = 3
	maxReportedFrame  = 120
)

// wsSession is an open session and, once something ends it, why.
//
// A WebSocket read that times out cannot be resumed: the peer may have been
// mid-frame, so the connection is torn down rather than left in a state nobody
// can reason about. The session is therefore gone, and a later step naming it
// should say what ended it rather than fail on a closed socket.
type wsSession struct {
	conn *adapters.WSSession
	dead string
}

// runWS executes one step's worth of work on a WebSocket session: open, send,
// receive, in that order, each optional.
//
// This is the first step type that is not call-shaped, and two consequences run
// through everything below. A session outlives the step — it is registered on
// the iteration and closed when the iteration ends, so a later step picks it up
// where this one left it. And a frame is not a response: nothing correlates it
// to what this step sent, so the step says which frame it is waiting for, and
// the ones that arrive meanwhile are skipped rather than failed on.
func (r *Runner) runWS(ctx context.Context, st *ir.Step, scope *Scope, anchor time.Time, it *Iteration) (*span.Span, bool, error) {
	spec := st.WS
	sp := span.New(st.ID, time.Since(anchor))
	defer func() { sp.Duration = time.Since(anchor) - sp.Start }()

	sess := it.ws[spec.Session]
	if spec.Opens() {
		var cont bool
		var err error
		if sess, cont, err = r.openWS(ctx, st, sp, scope, anchor, it); sess == nil || err != nil {
			return sp, cont, err
		}
	}
	switch {
	case sess == nil:
		// The validator resolves the session graph before the run, so reaching
		// here means hand-written IR rather than an authored flow.
		return sp, false, fmt.Errorf("step %q: %s was never opened", st.ID, sessionLabel(spec))
	case sess.dead != "":
		return sp, r.record(it, sp, sp, st, scope,
			fmt.Sprintf("%s is no longer open: %s", sessionLabel(spec), sess.dead)), nil
	}
	if !captureDisabled(st) {
		sp.SetCall("WS", sess.conn.URL(), 0, "")
	}

	var sent []byte
	if len(spec.Send) > 0 {
		frame, err := adapters.BuildWSFrame(spec, scope.Resolve)
		if err != nil {
			return sp, false, fmt.Errorf("step %q: %w", st.ID, err)
		}
		sent = frame
		child := sp.Child(wsSendSpan, time.Since(anchor))
		err = sess.conn.Send(ctx, frame)
		child.Duration = time.Since(anchor) - child.Start
		if !captureDisabled(st) {
			sp.SetRaw(frame, nil)
		}
		if err != nil {
			child.Outcome = span.OutcomeFailed
			return sp, r.recordWSError(it, sp, child, st, scope, sess, err), nil
		}
	}

	if spec.Receive == nil {
		return sp, true, nil
	}

	child := sp.Child(wsReceiveSpan, time.Since(anchor))
	frame, skipped, err := r.awaitFrame(ctx, sess.conn, spec.Receive, scope)
	child.Duration = time.Since(anchor) - child.Start
	if !captureDisabled(st) {
		sp.SetRaw(sent, frame.Payload)
	}
	if err != nil {
		child.Outcome = span.OutcomeFailed
		var timedOut *frameTimeout
		if errors.As(err, &timedOut) {
			detail := timedOut.detail(spec.Receive, skipped)
			sess.dead = detail
			return sp, r.record(it, sp, child, st, scope, detail), nil
		}
		return sp, r.recordWSError(it, sp, child, st, scope, sess, err), nil
	}

	return sp, r.evaluate(it, sp, st, scope, wsTarget{frame: frame, latency: child.Duration}, anchor), nil
}

// openWS dials the session and registers it on the iteration. The handshake is
// an HTTP request, so it passes the same allow-list gate, carries the same
// auth, and classifies the same way a call step's request does.
func (r *Runner) openWS(ctx context.Context, st *ir.Step, sp *span.Span, scope *Scope, anchor time.Time, it *Iteration) (*wsSession, bool, error) {
	spec := st.WS
	req, err := adapters.BuildWSOpen(spec, scope.Resolve)
	if err != nil {
		return nil, false, fmt.Errorf("step %q: %w", st.ID, err)
	}
	req.URL = r.resolveURL(req.URL)

	if r.Allow != nil {
		ok, err := r.Allow(req.URL)
		if err != nil || !ok {
			child := sp.Child(wsOpenSpan, time.Since(anchor))
			child.Outcome = span.OutcomeFailed
			detail := fmt.Sprintf("host allow-list: %s is not an allowed target", req.URL)
			if err != nil {
				detail = fmt.Sprintf("host allow-list check failed for %s: %v", req.URL, err)
			}
			it.markSession(spec.Session, &wsSession{dead: detail})
			return nil, r.record(it, sp, child, st, scope, detail), nil
		}
	}
	if err := r.applyAuth(ctx, st, req, scope); err != nil {
		return nil, false, err
	}

	conn, resp, child, err := r.Session.DialWS(ctx, wsOpenSpan, req, spec.Subprotocols, anchor)
	sp.Children = append(sp.Children, child)
	if !captureDisabled(st) && resp != nil {
		child.SetCall(req.Method, req.URL, resp.Status, resp.Headers.Get("Retry-After"))
	}
	if err != nil {
		// A refusal before the upgrade is an ordinary HTTP response, and a 429
		// among them is the same throttle it would be anywhere else — a server
		// declining to carry one more connection.
		if resp != nil && isThrottled(resp.Status, st.Throttle) {
			detail := fmt.Sprintf("throttled: handshake answered HTTP %d", resp.Status)
			it.markSession(spec.Session, &wsSession{dead: detail})
			return nil, r.recordThrottle(it, sp, st, scope, detail), nil
		}
		detail := handshakeFailure(err, resp)
		it.markSession(spec.Session, &wsSession{dead: detail})
		return nil, r.record(it, sp, child, st, scope, detail), nil
	}

	sess := &wsSession{conn: conn}
	it.markSession(spec.Session, sess)
	return sess, true, nil
}

// awaitFrame waits for the frame the step is about, skipping the ones it is
// not. The timeout bounds the whole wait rather than each read, so a heartbeat
// every second cannot keep a step alive indefinitely waiting for a message that
// is never coming.
func (r *Runner) awaitFrame(ctx context.Context, conn *adapters.WSSession, rec *ir.WSReceive, scope *Scope) (adapters.Frame, [][]byte, error) {
	timeout := time.Duration(rec.Timeout)
	if timeout <= 0 {
		timeout = adapters.DefaultReceiveTimeout
	}
	deadline := time.Now().Add(timeout)

	var skipped [][]byte
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return adapters.Frame{}, skipped, &frameTimeout{waited: timeout}
		}
		frame, err := conn.Receive(ctx, remaining)
		if err != nil {
			// Our own read deadline expiring is this step's verdict — the frame
			// did not arrive — not the transport's. A peer hanging up is the
			// peer's verdict and keeps its close code; a cancelled run is the
			// run's, and reads as one.
			_, closed := adapters.AsWSCloseError(err)
			if !closed && ctx.Err() == nil && timedOut(err, deadline) {
				return adapters.Frame{}, skipped, &frameTimeout{waited: timeout}
			}
			return adapters.Frame{}, skipped, err
		}

		ok, err := frameMatches(rec.Match, frame, scope)
		if err != nil {
			return adapters.Frame{}, skipped, err
		}
		if ok {
			return frame, skipped, nil
		}
		if len(skipped) < maxReportedFrames {
			skipped = append(skipped, frame.Payload)
		}
	}
}

// timedOut reports whether an error is this step's own read deadline expiring.
//
// A deadline that lands on an idle connection surfaces as
// context.DeadlineExceeded, but one that lands mid-message surfaces as the
// closed connection instead: a WebSocket read cannot be abandoned partway and
// resumed, so the library tears the connection down. Both are the same event,
// and the deadline is what says so.
func timedOut(err error, deadline time.Time) bool {
	return errors.Is(err, context.DeadlineExceeded) || !time.Now().Before(deadline)
}

// frameMatches reports whether a frame is the one the step is waiting for.
// Every condition must hold; a condition that simply does not apply to this
// frame — a path it has no field for — is a mismatch, not an error.
func frameMatches(match []ir.Assertion, frame adapters.Frame, scope *Scope) (bool, error) {
	tgt := wsTarget{frame: frame}
	for _, m := range match {
		res, err := eval.Assert(m, tgt, scope.Lookup)
		if err != nil {
			return false, fmt.Errorf("ws match %s: %w", assertName(m), err)
		}
		if !res.Pass {
			return false, nil
		}
	}
	return true, nil
}

// frameTimeout is the step waiting long enough and giving up.
type frameTimeout struct{ waited time.Duration }

func (t *frameTimeout) Error() string {
	return fmt.Sprintf("no frame arrived within %s", t.waited)
}

// detail explains the timeout in terms of what the step was waiting for and
// what turned up instead. Without the frames it skipped, a mismatched path
// reads as a target that went quiet.
func (t *frameTimeout) detail(rec *ir.WSReceive, skipped [][]byte) string {
	if len(rec.Match) == 0 {
		return t.Error()
	}
	conditions := make([]string, 0, len(rec.Match))
	for _, m := range rec.Match {
		conditions = append(conditions, describeMatch(m))
	}
	detail := fmt.Sprintf("no frame matching %s arrived within %s",
		strings.Join(conditions, " and "), t.waited)
	if len(skipped) == 0 {
		return detail + " (no frames arrived at all)"
	}
	seen := make([]string, 0, len(skipped))
	for _, s := range skipped {
		seen = append(seen, truncateFrame(s))
	}
	return fmt.Sprintf("%s; skipped %s", detail, strings.Join(seen, ", "))
}

// matchOps renders an operator the way the flow author wrote it. A failure
// that reports `$.type == "settlement"` is one someone can compare against the
// flow file; one that reports a span name is not.
var matchOps = map[ir.AssertionOp]string{
	ir.OpEq: "==", ir.OpNe: "!=",
	ir.OpLt: "<", ir.OpLte: "<=", ir.OpGt: ">", ir.OpGte: ">=",
}

func describeMatch(a ir.Assertion) string {
	subject := string(a.Source)
	switch a.Source {
	case ir.AssertBody, ir.AssertVar:
		subject = a.Key
	case ir.AssertHeader:
		subject = "header." + a.Key
	}
	switch a.Op {
	case ir.OpExists:
		return subject + " != null"
	case ir.OpNotExists:
		return subject + " == null"
	}
	op := string(a.Op)
	if symbol, ok := matchOps[a.Op]; ok {
		op = symbol
	}
	return fmt.Sprintf("%s %s %s", subject, op, a.Value)
}

func truncateFrame(b []byte) string {
	if len(b) > maxReportedFrame {
		return string(b[:maxReportedFrame]) + "…"
	}
	return string(b)
}

// recordWSError classifies a failure on an open session and retires it — one
// broken read or write leaves a session nothing later can trust.
//
// The peer ending the conversation is its own thing, distinct from the
// transport breaking: close code 1013 is RFC 6455's "try again later", a
// throttle in every sense HTTP 429 is, and the rest are failures that name the
// code so a reader knows whether the server crashed or hung up politely.
func (r *Runner) recordWSError(it *Iteration, sp, at *span.Span, st *ir.Step, scope *Scope, sess *wsSession, err error) bool {
	if closed, ok := adapters.AsWSCloseError(err); ok {
		sess.dead = closed.Error()
		if closed.Code == adapters.StatusTryAgainLater {
			return r.recordThrottle(it, sp, st, scope, "throttled: "+closed.Error())
		}
		return r.record(it, sp, at, st, scope, closed.Error())
	}
	sess.dead = err.Error()
	return r.record(it, sp, at, st, scope, err.Error())
}

// handshakeFailure explains an upgrade that never happened. The status is what
// a reader needs: without it a 401 and a refused connection are the same "dial
// failed".
func handshakeFailure(err error, resp *adapters.Response) string {
	if resp != nil && resp.Status != 0 {
		return fmt.Sprintf("ws handshake refused: HTTP %d", resp.Status)
	}
	return err.Error()
}

func sessionLabel(spec *ir.WSSpec) string {
	if spec.Session == "" {
		return "the flow's ws session"
	}
	return fmt.Sprintf("ws session %q", spec.Session)
}

// wsTarget adapts a received frame to eval.Target. A frame carries a payload
// and nothing else: the status and headers belong to the handshake, which the
// engine classified before any of this ran, so the validator refuses assertions
// on them rather than answering with a misleading zero.
type wsTarget struct {
	frame   adapters.Frame
	latency time.Duration
}

func (t wsTarget) Status() int                  { return 0 }
func (t wsTarget) Header(string) (string, bool) { return "", false }
func (t wsTarget) Body() []byte                 { return t.frame.Payload }
func (t wsTarget) Latency() time.Duration       { return t.latency }
