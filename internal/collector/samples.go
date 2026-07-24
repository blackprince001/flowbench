package collector

import (
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

// keptSamplesCeiling bounds the per-flow-run tier. Unlike the series it is not
// an aggregate — one record per flow-run — so a long run has to be sampled. The
// policy mirrors trace capture (ADR 0007): everything interesting is kept
// whole, successes are thinned, and what was dropped is reported rather than
// silently truncated.
const keptSamplesCeiling = 10_000

// FlowRun is one flow-run as the results server shows it: enough to place it in
// time, rank it by latency, and colour it by outcome. Seq is its ordinal in the
// run, so a sampled set still says which flow-run each record was.
type FlowRun struct {
	Seq       int           `json:"seq"`
	Flow      string        `json:"flow"`
	At        time.Duration `json:"at"`
	Latency   time.Duration `json:"latency"`
	Service   time.Duration `json:"service"`
	Outcome   span.Outcome  `json:"outcome"`
	Throttled bool          `json:"throttled,omitempty"`
}

// Samples is the per-flow-run tier plus the accounting a reader needs to trust
// it: how many flow-runs there were, how many are here, and what was thinned.
type Samples struct {
	Total    int       `json:"total"`
	Kept     int       `json:"kept"`
	EveryNth int       `json:"every_nth"` // success sampling stride; 1 means all
	Runs     []FlowRun `json:"runs"`
}

// Complete reports whether every flow-run is present.
func (s Samples) Complete() bool { return s.Kept == s.Total }

// BuildSamples keeps every failed, throttled and skipped flow-run, then fills
// the remaining budget with an evenly spaced sample of the successes — evenly
// rather than head-first so a thinned run still covers its whole duration.
func BuildSamples(res *executor.Result) Samples {
	if res == nil || len(res.Samples) == 0 {
		return Samples{EveryNth: 1}
	}

	notable := 0
	for _, s := range res.Samples {
		if isNotable(s) {
			notable++
		}
	}

	budget := keptSamplesCeiling - notable
	successes := len(res.Samples) - notable
	stride := 1
	if budget < successes {
		if budget <= 0 {
			stride = successes + 1 // no room left; notable records only
		} else {
			stride = (successes + budget - 1) / budget
		}
	}

	out := Samples{Total: len(res.Samples), EveryNth: stride}
	seen := 0
	for i, s := range res.Samples {
		keep := isNotable(s)
		if !keep {
			keep = seen%stride == 0
			seen++
		}
		if !keep || len(out.Runs) >= keptSamplesCeiling {
			continue
		}
		out.Runs = append(out.Runs, FlowRun{
			Seq:       i,
			Flow:      s.Flow,
			At:        s.Actual,
			Latency:   s.Latency(),
			Service:   s.Service,
			Outcome:   s.Outcome,
			Throttled: s.Throttled,
		})
	}
	out.Kept = len(out.Runs)
	return out
}

// isNotable marks the flow-runs never thinned away: anything that did not
// simply succeed, plus every throttle (ADR 0006 — a throttle is a finding even
// when the mode does not count it as an error).
func isNotable(s executor.Sample) bool {
	return s.Outcome != span.OutcomeOK || s.Throttled
}
