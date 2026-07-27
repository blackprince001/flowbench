package server

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/executor"
)

func TestDeltaRateComputesCoresFromCumulativeDeltas(t *testing.T) {
	ats := []time.Duration{0, time.Second, 2 * time.Second}
	cum := []float64{10.0, 13.5, 15.5} // +3.5 cores over the first second, +2.0 over the second
	pts, last := deltaRate(ats, cum, 2*time.Second)
	if len(pts) != 2 {
		t.Fatalf("the first sample has nothing to diff against, want 2 points, got %d", len(pts))
	}
	if pts[0].Y != 3.5 || pts[1].Y != 2.0 {
		t.Errorf("wrong rates: %+v", pts)
	}
	if last != 2.0 {
		t.Errorf("last = %v, want 2.0", last)
	}
}

func TestTargetCPUChartLabelsTargetAndGeneratorSeparately(t *testing.T) {
	metrics := []executor.MetricSample{
		{At: 0, CPUSeconds: 1.0},
		{At: time.Second, CPUSeconds: 1.4}, // generator: 0.4 cores/s
	}
	agentSeries := []agent.PolledSample{
		{At: 0, Sample: agent.Sample{CPUSeconds: 10.0}},
		{At: time.Second, Sample: agent.Sample{CPUSeconds: 13.5}}, // target: 3.5 cores/s
	}
	lc := targetCPUChart(time.Second, metrics, agentSeries)
	if len(lc.Lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(lc.Lines))
	}
	target, generator := lc.Lines[0], lc.Lines[1]
	if target.Label != "target" || generator.Label != "generator" {
		t.Fatalf("unexpected line labels: %+v", lc.Lines)
	}
	if target.Last != "3.50 cores" {
		t.Errorf("target last = %q, want %q", target.Last, "3.50 cores")
	}
	if generator.Last != "0.40 cores" {
		t.Errorf("generator last = %q, want %q", generator.Last, "0.40 cores")
	}
	if target.Tone == generator.Tone {
		t.Error("target and generator must use distinguishable tones")
	}
}

func TestHumanBytesFormatsAcrossUnits(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{3 << 30, "3.0 GiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAgentSpanExtendsPastSeriesForLatePolls(t *testing.T) {
	agentSeries := []agent.PolledSample{{At: 5 * time.Second}}
	sp := agentSpan(collector.Series{}, nil, agentSeries)
	if sp != 5*time.Second {
		t.Errorf("span = %v, want 5s", sp)
	}
}
