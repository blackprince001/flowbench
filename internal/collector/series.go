package collector

import (
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

// Raw flow-run samples are unbounded — a 10k-VU soak produces millions — so the
// persisted series is bucketed, bounded by point count the way folded spans are
// bounded by span-path count (ADR 0007). Percentiles are computed per bucket at
// save time because bucket counts alone cannot reconstruct them later.
const maxSeriesPoints = 300

// bucketLadder holds widths a time axis reads cleanly at; the narrowest one
// that keeps the run under maxSeriesPoints wins.
var bucketLadder = []time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
	time.Hour,
}

// Series is a run's outcomes and latencies over time — the third persisted
// tier, alongside folded aggregates and sampled raw traces. It is what the
// dashboard's throughput, error-rate, throttle-rate and percentile charts read.
type Series struct {
	Bucket time.Duration `json:"bucket"`
	Points []SeriesPoint `json:"points"`
}

// SeriesPoint is one bucket, stamped with its start offset from the run start.
// FlowRuns over Bucket gives throughput. Empty buckets are still emitted so the
// grid stays uniform — a throughput gap is itself a finding — and carry zero
// percentiles, which consumers should skip rather than plot.
//
// OK, Failed and Skipped partition the bucket by outcome. Throttled is counted
// apart from all three because it is a property of the flow-run, not an outcome
// (ADR 0006): in load/stress/soak a throttled run's outcome is `throttled`, but
// in integration/system the same run also counts as failed, so the four
// counters can sum past FlowRuns by exactly that overlap.
type SeriesPoint struct {
	At        time.Duration `json:"at"`
	FlowRuns  int           `json:"flow_runs"`
	OK        int           `json:"ok"`
	Failed    int           `json:"failed"`
	Skipped   int           `json:"skipped"`
	Throttled int           `json:"throttled"`
	P50       time.Duration `json:"p50"`
	P95       time.Duration `json:"p95"`
	P99       time.Duration `json:"p99"`
}

// BuildSeries buckets a run's samples by dispatch time (when a VU actually
// began the run, so the chart shows achieved rather than intended load) and
// summarizes each bucket.
func BuildSeries(res *executor.Result) Series {
	if res == nil || len(res.Samples) == 0 {
		return Series{}
	}

	end := res.Duration
	for _, s := range res.Samples {
		if s.Actual > end {
			end = s.Actual
		}
	}
	width := bucketWidth(end)
	n := int(end/width) + 1

	buckets := make([][]executor.Sample, n)
	for _, s := range res.Samples {
		i := int(s.Actual / width)
		if i < 0 {
			i = 0
		} else if i >= n {
			i = n - 1
		}
		buckets[i] = append(buckets[i], s)
	}

	points := make([]SeriesPoint, n)
	for i, b := range buckets {
		points[i] = summarize(time.Duration(i)*width, b)
	}
	return Series{Bucket: width, Points: points}
}

func summarize(at time.Duration, b []executor.Sample) SeriesPoint {
	p := SeriesPoint{At: at, FlowRuns: len(b)}
	for _, s := range b {
		switch s.Outcome {
		case span.OutcomeFailed:
			p.Failed++
		case span.OutcomeSkipped:
			p.Skipped++
		case span.OutcomeOK:
			p.OK++
		}
		if s.Throttled {
			p.Throttled++
		}
	}
	if len(b) > 0 {
		p.P50 = executor.Percentile(b, 0.50)
		p.P95 = executor.Percentile(b, 0.95)
		p.P99 = executor.Percentile(b, 0.99)
	}
	return p
}

func bucketWidth(total time.Duration) time.Duration {
	for _, w := range bucketLadder {
		if total/w <= maxSeriesPoints {
			return w
		}
	}
	return bucketLadder[len(bucketLadder)-1]
}
