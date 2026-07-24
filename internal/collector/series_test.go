package collector_test

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

func TestBuildSeriesBucketsByDispatchTime(t *testing.T) {
	res := &executor.Result{
		Duration: 3 * time.Second,
		Samples: []executor.Sample{
			{Actual: 100 * time.Millisecond, Service: 10 * time.Millisecond, Outcome: span.OutcomeOK},
			{Actual: 900 * time.Millisecond, Service: 20 * time.Millisecond, Outcome: span.OutcomeOK},
			{Actual: 1200 * time.Millisecond, Service: 30 * time.Millisecond, Outcome: span.OutcomeFailed},
			{Actual: 2500 * time.Millisecond, Service: 40 * time.Millisecond, Outcome: span.OutcomeOK, Throttled: true},
		},
	}

	s := collector.BuildSeries(res)
	if s.Bucket != 100*time.Millisecond {
		t.Fatalf("a 3s run should bucket at the finest width, got %s", s.Bucket)
	}
	if len(s.Points) != 31 {
		t.Fatalf("want a uniform 31-point grid over 3s at 100ms, got %d", len(s.Points))
	}

	total := 0
	for _, p := range s.Points {
		total += p.FlowRuns
	}
	if total != len(res.Samples) {
		t.Errorf("bucketing lost samples: %d of %d", total, len(res.Samples))
	}

	if got := s.Points[1]; got.FlowRuns != 1 || got.At != 100*time.Millisecond {
		t.Errorf("sample at 100ms belongs in bucket 1, got %+v", got)
	}
	if got := s.Points[12]; got.Failed != 1 {
		t.Errorf("failure at 1200ms belongs in bucket 12, got %+v", got)
	}
	if got := s.Points[25]; got.Throttled != 1 || got.Failed != 0 {
		t.Errorf("throttle must count apart from failures, got %+v", got)
	}
	if got := s.Points[5]; got.FlowRuns != 0 || got.P95 != 0 {
		t.Errorf("empty buckets stay in the grid with zero percentiles, got %+v", got)
	}
}

// A long run must not grow the series without bound — the ladder widens the
// bucket instead.
func TestBuildSeriesStaysBounded(t *testing.T) {
	var samples []executor.Sample
	for i := range 20_000 {
		samples = append(samples, executor.Sample{
			Actual:  time.Duration(i) * 200 * time.Millisecond, // ~67 minutes
			Service: time.Millisecond,
			Outcome: span.OutcomeOK,
		})
	}
	res := &executor.Result{Duration: 4000 * time.Second, Samples: samples}

	s := collector.BuildSeries(res)
	if len(s.Points) > 300 {
		t.Fatalf("series unbounded: %d points at bucket %s", len(s.Points), s.Bucket)
	}
	if s.Bucket < 10*time.Second {
		t.Errorf("a 67-minute run should widen its bucket, got %s", s.Bucket)
	}

	total := 0
	for _, p := range s.Points {
		total += p.FlowRuns
	}
	if total != len(samples) {
		t.Errorf("bucketing lost samples: %d of %d", total, len(samples))
	}
}

func TestBuildSeriesEmptyRun(t *testing.T) {
	if got := collector.BuildSeries(&executor.Result{}); len(got.Points) != 0 {
		t.Errorf("a run with no samples has no series, got %+v", got)
	}
	if got := collector.BuildSeries(nil); len(got.Points) != 0 {
		t.Errorf("nil result must not panic, got %+v", got)
	}
}

// Percentiles are per bucket, so a slow window is visible where a whole-run
// percentile would average it away.
func TestBuildSeriesPercentilesArePerBucket(t *testing.T) {
	var samples []executor.Sample
	for i := range 50 {
		samples = append(samples, executor.Sample{Actual: 100 * time.Millisecond, Service: time.Duration(i) * time.Millisecond, Outcome: span.OutcomeOK})
	}
	for i := range 50 {
		samples = append(samples, executor.Sample{Actual: 500 * time.Millisecond, Service: time.Duration(1000+i) * time.Millisecond, Outcome: span.OutcomeOK})
	}
	res := &executor.Result{Duration: time.Second, Samples: samples}

	s := collector.BuildSeries(res)
	fast, slow := s.Points[1].P95, s.Points[5].P95
	if fast >= slow/10 {
		t.Errorf("per-bucket percentiles should separate the slow window: fast=%s slow=%s", fast, slow)
	}
}
