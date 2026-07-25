package report_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/report"
	"github.com/blackprince001/flowbench/internal/span"
)

// The fixtures below are shaped exactly as the executor records a kept trace:
// the step marked failed, the reason noted on the span the failure is about,
// and the call's status on the step that made it.

func flowRoot(at time.Duration, outcome span.Outcome) *span.Span {
	root := span.New("flow:checkout", at)
	root.Duration = 40 * time.Millisecond
	root.Outcome = outcome
	return root
}

// refusedTrace is a call the target answered and refused. The assertion on
// status is what noticed; the step carries what came back.
func refusedTrace(at time.Duration, status int) *span.Span {
	root := flowRoot(at, span.OutcomeFailed)
	step := root.Child("checkout", 0)
	step.Duration = 38 * time.Millisecond
	step.Outcome = span.OutcomeFailed
	step.Payload = &span.Payload{Method: "POST", Status: status}
	a := step.Child("assert_status", 37*time.Millisecond)
	a.Outcome = span.OutcomeFailed
	a.Payload = &span.Payload{Failure: fmt.Sprintf("status: want 200, got %d", status)}
	return root
}

// assertedTrace is a call the target answered correctly at the protocol level
// and wrongly in its body.
func assertedTrace(at time.Duration) *span.Span {
	root := flowRoot(at, span.OutcomeFailed)
	step := root.Child("checkout", 0)
	step.Duration = 38 * time.Millisecond
	step.Outcome = span.OutcomeFailed
	step.Payload = &span.Payload{Method: "POST", Status: 200}
	a := step.Child("assert_body_paid", 37*time.Millisecond)
	a.Outcome = span.OutcomeFailed
	a.Payload = &span.Payload{Failure: `body $.paid: want true, got false`}
	return root
}

// missingTrace is a call whose response was fine but did not carry the value
// the flow needed next.
func missingTrace(at time.Duration) *span.Span {
	root := flowRoot(at, span.OutcomeFailed)
	step := root.Child("login", 0)
	step.Duration = 38 * time.Millisecond
	step.Outcome = span.OutcomeFailed
	step.Payload = &span.Payload{Method: "POST", Status: 200}
	x := step.Child("token", 37*time.Millisecond)
	x.Outcome = span.OutcomeFailed
	x.Payload = &span.Payload{Failure: `extract "token" found nothing`}
	return root
}

// transportTrace is a call that got no answer at all: the leg fails, and the
// reason is recorded on the step around it.
func transportTrace(at time.Duration, detail string) *span.Span {
	root := flowRoot(at, span.OutcomeFailed)
	step := root.Child("checkout", 0)
	step.Duration = 38 * time.Millisecond
	step.Outcome = span.OutcomeFailed
	step.Payload = &span.Payload{Method: "POST", Failure: detail}
	leg := step.Child("http_call", time.Millisecond)
	leg.Duration = 37 * time.Millisecond
	leg.Outcome = span.OutcomeFailed
	return root
}

// throttledTrace is a throttle the mode also counts as an error: the flow-run
// failed, and the step it failed in is throttled all the same (ADR 0006).
func throttledTrace(at time.Duration) *span.Span {
	root := flowRoot(at, span.OutcomeFailed)
	step := root.Child("checkout", 0)
	step.Duration = 12 * time.Millisecond
	step.Outcome = span.OutcomeThrottled
	step.Payload = &span.Payload{Method: "POST", Status: 429, RetryAfter: "1", Failure: "throttled: HTTP 429"}
	return root
}

func groupsOf(t *testing.T, traces []*span.Span) map[string]report.FailureGroup {
	t.Helper()
	out := map[string]report.FailureGroup{}
	for _, g := range report.FailureGroups(traces, "/failures", "/waterfall") {
		out[g.Key] = g
	}
	return out
}

// Issue #38: a mixed-failure run separates into one group per step and cause,
// each counting the iterations behind it.
func TestFailureGroupsSplitByStepAndCause(t *testing.T) {
	traces := []*span.Span{
		refusedTrace(0, 503), refusedTrace(time.Second, 503), refusedTrace(2*time.Second, 404),
		assertedTrace(3 * time.Second),
		missingTrace(4 * time.Second),
		transportTrace(5*time.Second, "call failed: POST http://svc/pay: context deadline exceeded"),
		transportTrace(6*time.Second, "call failed: POST http://svc/pay: dial tcp 127.0.0.1:8080: connect: connection refused"),
		throttledTrace(7 * time.Second),
	}

	groups := groupsOf(t, traces)
	for key, want := range map[string]struct {
		cause report.Cause
		count int
	}{
		"checkout · status · HTTP 503":               {report.CauseStatus, 2},
		"checkout · status · HTTP 404":               {report.CauseStatus, 1},
		"checkout · assertion · assert_body_paid":    {report.CauseAssertion, 1},
		"login · extraction · token":                 {report.CauseExtraction, 1},
		"checkout · timeout · deadline exceeded":     {report.CauseTimeout, 1},
		"checkout · connection · connection refused": {report.CauseConnection, 1},
		"checkout · throttled · HTTP 429":            {report.CauseThrottled, 1},
	} {
		g, ok := groups[key]
		if !ok {
			t.Errorf("no group %q; got %v", key, keys(groups))
			continue
		}
		if g.Cause != want.cause {
			t.Errorf("%s: cause %q, want %q", key, g.Cause, want.cause)
		}
		if g.Count != want.count {
			t.Errorf("%s: %d iterations, want %d", key, g.Count, want.count)
		}
	}
	if len(groups) != 7 {
		t.Errorf("want 7 groups, got %d: %v", len(groups), keys(groups))
	}
}

// The acceptance's second half: a throttle is its own group whatever else is
// true of it. This run counts throttles as errors, so the flow-run failed — and
// the finding is still a throttle, never folded into a generic error group.
func TestThrottledIsAlwaysItsOwnGroup(t *testing.T) {
	traces := []*span.Span{
		throttledTrace(0), throttledTrace(time.Second),
		refusedTrace(2*time.Second, 500),
	}

	throttles := 0
	for _, g := range report.FailureGroups(traces, "/failures", "/waterfall") {
		if g.Cause == report.CauseThrottled {
			throttles += g.Count
			if g.Tone != "throttled" {
				t.Errorf("a throttle group must not borrow the failure tone, got %q", g.Tone)
			}
			continue
		}
		for _, r := range g.Runs {
			if r.Outcome == span.OutcomeThrottled {
				t.Errorf("throttled iteration inside %s group %q", g.Cause, g.Key)
			}
		}
	}
	if throttles != 2 {
		t.Errorf("want both throttles grouped as throttles, got %d", throttles)
	}
}

// The status the target answered with is the finding, even though an assertion
// is what noticed it — and it is blamed on the step, not on the assertion span,
// so one step's failures do not scatter across every assertion it makes.
func TestStatusOutranksTheAssertionThatNoticedIt(t *testing.T) {
	groups := report.FailureGroups([]*span.Span{refusedTrace(0, 503)}, "/failures", "/waterfall")
	if len(groups) != 1 {
		t.Fatalf("want one group, got %v", groups)
	}
	g := groups[0]
	if g.Step != "checkout" || g.Cause != report.CauseStatus || g.Label != "HTTP 503" {
		t.Errorf("got %s / %s / %s, want checkout / status / HTTP 503", g.Step, g.Cause, g.Label)
	}
	// The link still points at the assertion span, so the waterfall opens on
	// the row that failed rather than on the step containing it.
	if got := g.Runs[0].Span; got != "0.0.0" {
		t.Errorf("span path %q, want the assertion child", got)
	}
	if want := "/waterfall?trace=0&span=0.0.0"; g.Runs[0].Href != want {
		t.Errorf("href %q, want %q", g.Runs[0].Href, want)
	}
}

// A retried call fails once per attempt in the trace. That is one iteration
// failing, not three, and the count has to say so.
func TestRetriedAttemptsCountOnce(t *testing.T) {
	root := flowRoot(0, span.OutcomeFailed)
	step := root.Child("checkout", 0)
	step.Duration = 38 * time.Millisecond
	step.Outcome = span.OutcomeFailed
	step.Payload = &span.Payload{Failure: "call failed: POST http://svc/pay: connection reset by peer"}
	for i := 1; i <= 3; i++ {
		attempt := step.Child(fmt.Sprintf("attempt %d", i), time.Duration(i)*time.Millisecond)
		attempt.Outcome = span.OutcomeFailed
		leg := attempt.Child("http_call", time.Duration(i)*time.Millisecond)
		leg.Outcome = span.OutcomeFailed
		step.Child("backoff", time.Duration(i)*2*time.Millisecond)
	}

	groups := report.FailureGroups([]*span.Span{root}, "/failures", "/waterfall")
	if len(groups) != 1 {
		t.Fatalf("want one group, got %v", groups)
	}
	if groups[0].Count != 1 {
		t.Errorf("three attempts of one call counted %d times", groups[0].Count)
	}
	if groups[0].Cause != report.CauseConnection {
		t.Errorf("cause %q, want connection", groups[0].Cause)
	}
}

// Groups are ordered by how much of the failure they are, so the top row is
// what to fix first.
func TestFailureGroupsLeadWithTheLargest(t *testing.T) {
	traces := []*span.Span{
		assertedTrace(0),
		refusedTrace(time.Second, 500), refusedTrace(2*time.Second, 500), refusedTrace(3*time.Second, 500),
	}
	groups := report.FailureGroups(traces, "/failures", "/waterfall")
	if groups[0].Label != "HTTP 500" || groups[0].Count != 3 {
		t.Fatalf("largest group should lead, got %+v", groups[0])
	}
}

func TestSelectGroupDegradesOnAStaleKey(t *testing.T) {
	groups := report.FailureGroups([]*span.Span{refusedTrace(0, 500)}, "/failures", "/waterfall")
	sel, groups := report.SelectGroup(groups, "checkout · status · HTTP 418")
	if sel != nil {
		t.Errorf("a stale group key selected %+v", sel)
	}
	if sel, _ := report.SelectGroup(groups, groups[0].Key); sel == nil || !sel.Selected {
		t.Error("a live group key should select its group")
	}
}

func TestFailingTracesCountsOnlyWhatWentWrong(t *testing.T) {
	ok := flowRoot(0, span.OutcomeOK)
	ok.Child("checkout", 0)
	traces := []*span.Span{ok, refusedTrace(time.Second, 500), throttledTrace(2 * time.Second), nil}
	if got := report.FailingTraces(traces); got != 2 {
		t.Errorf("counted %d failing traces, want 2", got)
	}
}

func keys(m map[string]report.FailureGroup) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
