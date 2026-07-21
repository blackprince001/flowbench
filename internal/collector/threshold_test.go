package collector

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

func sample(latency time.Duration, outcome span.Outcome, throttled bool, at time.Duration) executor.Sample {
	return executor.Sample{Intended: at, Actual: at, Service: latency, Outcome: outcome, Throttled: throttled}
}

func TestParseThreshold(t *testing.T) {
	ok := []string{
		"p95(latency) < 800ms",
		"p99(latency) <= 1s",
		"error_rate < 1%",
		"throttle_rate <= 5%",
		"avg(latency) > 10ms",
		"error_rate < 0.01",
	}
	for _, e := range ok {
		if _, err := ParseThreshold(e); err != nil {
			t.Errorf("ParseThreshold(%q) errored: %v", e, err)
		}
	}

	bad := []string{
		"p95(latency) 800ms",  // no operator
		"rps(latency) < 5",    // unknown aggregation
		"p95(status) < 5",     // latency only
		"cpu < 5",             // unknown metric
		"p95(latency) < fast", // bad duration
		"error_rate < abc",    // bad number
		"p150(latency) < 1s",  // percentile out of range
	}
	for _, e := range bad {
		if _, err := ParseThreshold(e); err == nil {
			t.Errorf("ParseThreshold(%q) should have errored", e)
		}
	}
}

func TestEvaluate(t *testing.T) {
	// 100 flow-runs: p95 latency ~100ms, 5% failed, 10% throttled.
	var samples []executor.Sample
	for i := 0; i < 100; i++ {
		out := span.OutcomeOK
		throttled := false
		switch {
		case i < 5:
			out = span.OutcomeFailed
		case i < 15:
			out, throttled = span.OutcomeThrottled, true
		}
		samples = append(samples, sample(100*time.Millisecond, out, throttled, 0))
	}
	res := &executor.Result{Samples: samples}

	tests := []struct {
		expr string
		pass bool
	}{
		{"p95(latency) < 200ms", true},
		{"p95(latency) < 50ms", false},
		{"error_rate < 1%", false}, // 5% > 1%
		{"error_rate < 10%", true}, // 5% < 10%
		{"throttle_rate <= 10%", true},
		{"throttle_rate < 5%", false}, // 10% !< 5%
	}
	for _, tt := range tests {
		th, err := ParseThreshold(tt.expr)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.expr, err)
		}
		out := Evaluate([]Threshold{th}, res)
		if out[0].Pass != tt.pass {
			t.Errorf("Evaluate(%q).Pass = %v, want %v (%s)", tt.expr, out[0].Pass, tt.pass, out[0].Detail)
		}
	}
}
