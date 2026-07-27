package server

import (
	"fmt"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/report"
)

// The target-resource overlay (issue #37) needs internal/executor and
// internal/agent, which internal/report deliberately never imports (chart.go
// stays on collector/span alone) — so, like vuChart, these builders live
// here and return plain report.LineChart values built from report's own
// exported primitives.

// agentSpan is the shared time axis for both overlay charts: the generator's
// metrics grid and the agent's poll grid rarely finish at the same instant,
// so the span is the latest instant either reported — the same rule vuChart
// already applies when extending past the bucketed series' own span.
func agentSpan(series collector.Series, metrics []executor.MetricSample, agentSeries []agent.PolledSample) time.Duration {
	sp := report.SeriesSpan(series)
	for _, m := range metrics {
		if m.At > sp {
			sp = m.At
		}
	}
	for _, a := range agentSeries {
		if a.At > sp {
			sp = a.At
		}
	}
	return sp
}

// deltaRate turns a cumulative counter sampled at ats into a per-second rate
// between adjacent samples — the "diff two samples, divide by elapsed wall
// time" convention agent.Sample and executor.MetricSample's own doc comments
// describe for CPUSeconds. The first sample has nothing to diff against, so
// it is dropped rather than plotted as a false zero.
func deltaRate(ats []time.Duration, cum []float64, span time.Duration) (pts []report.ChartPoint, last float64) {
	for i := 1; i < len(ats); i++ {
		dt := (ats[i] - ats[i-1]).Seconds()
		if dt <= 0 {
			continue
		}
		rate := (cum[i] - cum[i-1]) / dt
		x := 0.0
		if span > 0 {
			x = float64(ats[i]) / float64(span)
		}
		pts = append(pts, report.ChartPoint{X: x, Y: rate})
		last = rate
	}
	return pts, last
}

// targetCPUChart plots target and generator CPU as cores burned (Δcpu_seconds
// / Δwall_seconds), not a 0..1 percentage — the generator side has no stored
// core count to normalize against, so cores-as-rate is the only unit both
// sides can share honestly.
func targetCPUChart(sp time.Duration, metrics []executor.MetricSample, agentSeries []agent.PolledSample) report.LineChart {
	genAt := make([]time.Duration, len(metrics))
	genCPU := make([]float64, len(metrics))
	for i, m := range metrics {
		genAt[i], genCPU[i] = m.At, m.CPUSeconds
	}
	genPts, genLast := deltaRate(genAt, genCPU, sp)

	tgtAt := make([]time.Duration, len(agentSeries))
	tgtCPU := make([]float64, len(agentSeries))
	for i, a := range agentSeries {
		tgtAt[i], tgtCPU[i] = a.At, a.CPUSeconds
	}
	tgtPts, tgtLast := deltaRate(tgtAt, tgtCPU, sp)

	return report.LineChartOf("target-cpu", "CPU: target vs generator", []report.ChartSeries{
		{Label: "target", Tone: "kind-net", Points: tgtPts, Last: fmt.Sprintf("%.2f cores", tgtLast)},
		{Label: "generator", Tone: "kind-step", Points: genPts, Last: fmt.Sprintf("%.2f cores", genLast)},
	}, func(v float64) string { return fmt.Sprintf("%.1f cores", v) }, sp)
}

// targetMemChart plots target RSS against generator heap. Both are
// point-in-time byte counts already, no diffing — but they measure different
// things (host RSS-ish vs Go heap), so the labels say so rather than
// implying one number.
func targetMemChart(sp time.Duration, metrics []executor.MetricSample, agentSeries []agent.PolledSample) report.LineChart {
	frac := func(at time.Duration) float64 {
		if sp <= 0 {
			return 0
		}
		return float64(at) / float64(sp)
	}
	genPts := make([]report.ChartPoint, 0, len(metrics))
	var genLast uint64
	for _, m := range metrics {
		genPts = append(genPts, report.ChartPoint{X: frac(m.At), Y: float64(m.HeapAlloc)})
		genLast = m.HeapAlloc
	}
	tgtPts := make([]report.ChartPoint, 0, len(agentSeries))
	var tgtLast uint64
	for _, a := range agentSeries {
		tgtPts = append(tgtPts, report.ChartPoint{X: frac(a.At), Y: float64(a.MemUsedBytes)})
		tgtLast = a.MemUsedBytes
	}
	return report.LineChartOf("target-mem", "Memory: target RSS vs generator heap", []report.ChartSeries{
		{Label: "target RSS", Tone: "kind-net", Points: tgtPts, Last: humanBytes(float64(tgtLast))},
		{Label: "generator heap", Tone: "kind-step", Points: genPts, Last: humanBytes(float64(genLast))},
	}, humanBytes, sp)
}

// humanBytes formats a byte count at whichever binary unit keeps it readable.
func humanBytes(v float64) string {
	units := [...]string{"B", "KiB", "MiB", "GiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
