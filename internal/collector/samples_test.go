package collector_test

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

func TestBuildSamplesKeepsEverythingWhenItFits(t *testing.T) {
	var samples []executor.Sample
	for i := range 500 {
		s := executor.Sample{Actual: time.Duration(i) * time.Millisecond, Service: time.Millisecond, Outcome: span.OutcomeOK}
		if i%50 == 0 {
			s.Outcome, s.Throttled = span.OutcomeThrottled, true
		}
		samples = append(samples, s)
	}

	got := collector.BuildSamples(&executor.Result{Duration: time.Second, Samples: samples})
	if !got.Complete() || got.Kept != 500 || got.EveryNth != 1 {
		t.Fatalf("a small run should keep every flow-run, got %+v", struct{ Total, Kept, EveryNth int }{got.Total, got.Kept, got.EveryNth})
	}
	// Seq must address the original flow-run, so a sampled set stays honest.
	for i, r := range got.Runs {
		if r.Seq != i {
			t.Fatalf("run %d carries seq %d", i, r.Seq)
		}
	}
}

// Over the ceiling, failures and throttles survive whole and successes thin.
func TestBuildSamplesThinsSuccessesNotFindings(t *testing.T) {
	var samples []executor.Sample
	failures, throttles := 0, 0
	for i := range 60_000 {
		s := executor.Sample{Actual: time.Duration(i) * time.Millisecond, Service: time.Millisecond, Outcome: span.OutcomeOK}
		switch {
		case i%1000 == 0:
			s.Outcome = span.OutcomeFailed
			failures++
		case i%997 == 0:
			s.Outcome, s.Throttled = span.OutcomeThrottled, true
			throttles++
		}
		samples = append(samples, s)
	}

	got := collector.BuildSamples(&executor.Result{Duration: time.Minute, Samples: samples})
	if got.Complete() {
		t.Fatal("a 60k-flow-run run must be reported as thinned")
	}
	if got.Kept > 10_000 {
		t.Errorf("ceiling breached: kept %d", got.Kept)
	}
	if got.EveryNth < 2 {
		t.Errorf("successes should be thinned, stride was %d", got.EveryNth)
	}

	keptFail, keptThrottle := 0, 0
	for _, r := range got.Runs {
		if r.Outcome == span.OutcomeFailed {
			keptFail++
		}
		if r.Throttled {
			keptThrottle++
		}
	}
	if keptFail != failures || keptThrottle != throttles {
		t.Errorf("findings were thinned: kept %d/%d failures, %d/%d throttles", keptFail, failures, keptThrottle, throttles)
	}

	// Thinning must not bias toward the start of the run.
	last := got.Runs[len(got.Runs)-1]
	if last.At < 55*time.Second {
		t.Errorf("sample stops at %s, well short of the run's end", last.At)
	}
}

func TestBuildSamplesEmpty(t *testing.T) {
	if got := collector.BuildSamples(nil); got.Kept != 0 || got.EveryNth != 1 {
		t.Errorf("nil result must not panic, got %+v", got)
	}
	if got := collector.BuildSamples(&executor.Result{}); got.Kept != 0 {
		t.Errorf("a run with no samples keeps nothing, got %+v", got)
	}
}
