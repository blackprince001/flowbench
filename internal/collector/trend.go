package collector

import (
	"fmt"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
)

// Soak posture: rather than a point-in-time gate, compare the run's second half
// to its first and flag a metric that is drifting the wrong way — the creep and
// leaks a long run is meant to surface. The limits are deliberately modest; a
// clean soak should be flat.
const (
	latencyCreepLimit = 0.10 // p95 growth, first half → second half
	rateDriftLimit    = 0.05 // absolute rise in error_rate / throttle_rate (5 points)
)

// EvaluateTrends splits a run at its midpoint and returns one Outcome per
// metric that drifted beyond its limit — p95 latency creep, error-rate drift,
// throttle-rate drift. It returns only flags; a flat soak yields nothing.
func EvaluateTrends(res *executor.Result) []Outcome {
	early, late := splitAtMidpoint(res.Samples, time.Duration(res.Duration))
	if len(early) == 0 || len(late) == 0 {
		return nil
	}

	var out []Outcome

	e95 := executor.Percentile(early, 0.95)
	l95 := executor.Percentile(late, 0.95)
	if e95 > 0 && float64(l95) > float64(e95)*(1+latencyCreepLimit) {
		out = append(out, Outcome{
			Expr:   "p95(latency) trend",
			Detail: fmt.Sprintf("p95 latency crept %s → %s over the run (>%.0f%%)", e95.Round(time.Microsecond), l95.Round(time.Microsecond), latencyCreepLimit*100),
		})
	}

	if d := errorRate(late) - errorRate(early); d > rateDriftLimit {
		out = append(out, Outcome{
			Expr:   "error_rate trend",
			Detail: fmt.Sprintf("error_rate drifted +%.2f points (%.2f%% → %.2f%%)", d*100, errorRate(early)*100, errorRate(late)*100),
		})
	}

	if d := throttleRate(late) - throttleRate(early); d > rateDriftLimit {
		out = append(out, Outcome{
			Expr:   "throttle_rate trend",
			Detail: fmt.Sprintf("throttle_rate drifted +%.2f points (%.2f%% → %.2f%%)", d*100, throttleRate(early)*100, throttleRate(late)*100),
		})
	}

	return out
}

// splitAtMidpoint partitions samples by dispatch time into the run's first and
// second halves.
func splitAtMidpoint(samples []executor.Sample, total time.Duration) (early, late []executor.Sample) {
	mid := total / 2
	for _, s := range samples {
		if s.Actual < mid {
			early = append(early, s)
		} else {
			late = append(late, s)
		}
	}
	return early, late
}
