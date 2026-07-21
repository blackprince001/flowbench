package collector

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

// TestEvaluateTrendsLatencyCreep is the issue #17 acceptance: a synthetic soak
// whose latency climbs over the run is flagged by trend evaluation.
func TestEvaluateTrendsLatencyCreep(t *testing.T) {
	const dur = 10 * time.Second
	var samples []executor.Sample
	for i := 0; i < 200; i++ {
		at := time.Duration(i) * dur / 200
		latency := 10 * time.Millisecond
		if at >= dur/2 {
			latency = 60 * time.Millisecond // second half creeps up
		}
		samples = append(samples, sample(latency, span.OutcomeOK, false, at))
	}
	res := &executor.Result{Samples: samples, Duration: dur}

	flags := EvaluateTrends(res)
	if !hasExpr(flags, "p95(latency) trend") {
		t.Fatalf("latency creep not flagged; got %+v", flags)
	}
}

func TestEvaluateTrendsFlatSoakIsClean(t *testing.T) {
	const dur = 10 * time.Second
	var samples []executor.Sample
	for i := 0; i < 200; i++ {
		at := time.Duration(i) * dur / 200
		samples = append(samples, sample(20*time.Millisecond, span.OutcomeOK, false, at))
	}
	res := &executor.Result{Samples: samples, Duration: dur}

	if flags := EvaluateTrends(res); len(flags) != 0 {
		t.Fatalf("a flat soak should raise no trend flags, got %+v", flags)
	}
}

func TestEvaluateTrendsErrorDrift(t *testing.T) {
	const dur = 10 * time.Second
	var samples []executor.Sample
	for i := 0; i < 200; i++ {
		at := time.Duration(i) * dur / 200
		out := span.OutcomeOK
		if at >= dur/2 && i%4 == 0 { // ~25% errors in the second half, ~0 in the first
			out = span.OutcomeFailed
		}
		samples = append(samples, sample(20*time.Millisecond, out, false, at))
	}
	res := &executor.Result{Samples: samples, Duration: dur}

	if !hasExpr(EvaluateTrends(res), "error_rate trend") {
		t.Fatal("error-rate drift not flagged")
	}
}

func hasExpr(outcomes []Outcome, expr string) bool {
	for _, o := range outcomes {
		if o.Expr == expr {
			return true
		}
	}
	return false
}
