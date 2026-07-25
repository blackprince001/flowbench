package report

import (
	"fmt"
	"strings"

	"github.com/blackprince001/flowbench/internal/span"
)

// The detail panels all answer "what am I looking at" with the same shape: a
// short list of label/value pairs, ordered most-diagnostic first.

// share formats a percentage without rounding a small-but-real value down to a
// flat 0%, which would read as "none of the time is here".
func share(v float64) string {
	switch {
	case v <= 0:
		return "0%"
	case v < 0.1:
		return "<0.1%"
	case v < 10:
		return fmt.Sprintf("%.1f%%", v)
	default:
		return fmt.Sprintf("%.0f%%", v)
	}
}

// Facts describes an inspected flame frame. Self time leads the aggregate view
// because a wide frame whose time is all in its children is not the problem —
// the child is.
func (d FrameDetail) Facts() []Fact {
	return []Fact{
		{Label: "total time", Value: humanDur(d.Total)},
		{Label: "self time", Value: fmt.Sprintf("%s · %s of total", humanDur(d.Self), share(d.SelfShare))},
		{Label: "share of run", Value: share(d.Width)},
		{Label: "calls folded", Value: fmt.Sprint(d.Count)},
		{Label: "mean per call", Value: humanDur(d.PerCall)},
		{Label: "span path", Value: d.Path},
	}
}

// Facts describes an inspected waterfall span. When the span is a call, what the
// target actually answered leads: the outcome says `failed`, but only the status
// says whether that was a 500, a 404 or a 403.
func (r Row) Facts() []Fact {
	out := []Fact{{Label: "outcome", Value: string(r.Outcome), Tone: string(r.Outcome)}}

	if p := r.Payload; p != nil {
		if p.Status != 0 {
			out = append(out, Fact{Label: "status", Value: fmt.Sprint(p.Status), Tone: statusTone(p.Status)})
		}
		if p.Method != "" || p.URL != "" {
			out = append(out, Fact{Label: "request", Value: strings.TrimSpace(p.Method + " " + p.URL)})
		}
		if p.RetryAfter != "" {
			out = append(out, Fact{Label: "retry-after", Value: p.RetryAfter, Tone: "throttled"})
		}
	}

	out = append(out,
		Fact{Label: "start", Value: humanDur(r.Start)},
		Fact{Label: "duration", Value: humanDur(r.Duration)},
		Fact{Label: "self time", Value: humanDur(r.Self)},
		Fact{Label: "kind", Value: string(r.Kind)},
	)

	if p := r.Payload; p != nil && (p.ReqBytes > 0 || p.RespBytes > 0) {
		out = append(out, Fact{Label: "bytes", Value: fmt.Sprintf("%d sent · %d received", p.ReqBytes, p.RespBytes)})
	}
	return out
}

// statusTone colours a status by class, so a 429 reads throttled rather than
// merely red (ADR 0006).
func statusTone(status int) string {
	switch {
	case status == 429:
		return "throttled"
	case status >= 500:
		return "failed"
	case status >= 400:
		return "failed"
	case status >= 200 && status < 300:
		return "ok"
	default:
		return ""
	}
}

// Facts describes an inspected flow-run. Queued time is called out separately
// because latency counts from when the run was due: a large queue figure is the
// generator stalling, not the target slowing down.
func (d RunDetail) Facts() []Fact {
	outcome := string(d.Outcome)
	tone := outcome
	if d.Throttled {
		outcome, tone = "throttled", "throttled"
		if d.Outcome == span.OutcomeFailed {
			outcome = "throttled · counted as an error in this mode"
		}
	}
	return []Fact{
		{Label: "outcome", Value: outcome, Tone: tone},
		{Label: "started at", Value: d.At},
		{Label: "latency", Value: d.Latency},
		{Label: "service time", Value: d.Service},
		{Label: "queued", Value: d.Queued},
		{Label: "flow", Value: d.Flow},
	}
}

// Facts describes a selected failure group. The cause leads because it is what
// the group is: every iteration below it failed this way, in this step.
func (g FailureGroup) Facts() []Fact {
	out := []Fact{
		{Label: "cause", Value: string(g.Cause), Tone: g.Tone},
		{Label: "step", Value: g.Step},
	}
	if g.Label != "" {
		out = append(out, Fact{Label: "detail", Value: g.Label})
	}
	return append(out, Fact{Label: "iterations", Value: fmt.Sprint(g.Count), Tone: g.Tone})
}

// Facts describes an inspected series bucket.
func (d BucketDetail) Facts() []Fact {
	out := []Fact{
		{Label: "throughput", Value: d.RPS},
		{Label: "flow-runs", Value: fmt.Sprint(d.FlowRuns)},
		{Label: "ok", Value: fmt.Sprint(d.OK), Tone: "ok"},
	}
	if d.Throttled > 0 {
		out = append(out, Fact{Label: "throttled", Value: fmt.Sprint(d.Throttled), Tone: "throttled"})
	}
	if d.Failed > 0 {
		out = append(out, Fact{Label: "failed", Value: fmt.Sprint(d.Failed), Tone: "failed"})
	}
	if d.Skipped > 0 {
		out = append(out, Fact{Label: "skipped", Value: fmt.Sprint(d.Skipped), Tone: "skipped"})
	}
	return append(out,
		Fact{Label: "p50", Value: d.P50},
		Fact{Label: "p95", Value: d.P95},
		Fact{Label: "p99", Value: d.P99},
	)
}
