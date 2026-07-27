package report

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/span"
)

// The dashboard's charts are server-rendered SVG: one stylesheet, geometry in a
// fixed viewBox so the browser reflows on resize with nothing re-laid out. There
// is no client rendering step and no JS on this page (ADR 0014) — the same
// posture as the flame graph, one dimension simpler.

// chartW/chartH are the SVG user-space extent every line is scaled into. y grows
// downward. chartTop/chartBottom hold a little headroom so a line at the maximum
// (or minimum) never hugs the frame or collides with the axis labels.
const (
	chartW      = 100.0
	chartH      = 40.0
	chartTop    = 4.0
	chartBottom = 4.0
)

// ChartPoint is one plotted sample: X is 0..1 along the shared time axis (so
// lanes sampled on different grids still line up), Y is the raw value.
type ChartPoint struct {
	X, Y float64
}

// ChartSeries is one input line. Last is the latest value, preformatted for the
// legend, so a reader gets the current number without reading the axis.
type ChartSeries struct {
	Label  string
	Tone   string // a kind or status tone (net/logic/retry/step/flow, ok/failed/throttled)
	Points []ChartPoint
	Last   string
}

// ChartLine is a laid-out line: Points is the polyline in viewBox coordinates
// and Area closes that line down to the baseline for the dithered fill beneath
// it. Both are plain coordinate text, so they interpolate into the SVG attribute
// without tripping the escaper.
//
// Min/Mean/Max are the line's own summary, formatted in the chart's unit. A
// small multiple has no room for them; the expanded view is where a reader asks
// "how bad did it get, and was that typical".
type ChartLine struct {
	Label  string
	Tone   string
	Points string
	Area   string
	Last   string
	Min    string
	Mean   string
	Max    string
}

// AxisTick is one labelled position on the time axis, 0..1 across the run.
type AxisTick struct {
	At    float64
	Label string
}

// AgentOverlay is the "Target resources" card: once a target-metrics agent
// (issue #32) contributed a sample to the run, CPU and memory charts
// comparing target against generator; until then, a note pointing the
// reader at agent_addr.
type AgentOverlay struct {
	Attached bool
	Note     string      // set only when !Attached
	Charts   []LineChart // set only when Attached: CPU, then memory
}

// LineChart is one titled chart of one or more lines over a shared axis. Key
// addresses it in a URL, so a chart can be opened on its own; Href is that link
// and Expanded says this is the opened one.
type LineChart struct {
	Key   string
	Title string
	Lines []ChartLine
	YTop  string // the y-axis maximum, labelled
	YMid  string // half of it — the gridline the plot already draws
	XEnd  string // the time span, the x-axis end label
	Empty bool

	Href     string
	Expanded bool

	// The extended view's additions: a labelled time axis, and where in the run
	// the highest point of any line fell.
	Ticks  []AxisTick
	PeakX  float64
	Peak   string
	PeakAt string
}

// LineChartOf scales every series to one shared y-maximum — so lines in the same
// chart are visually comparable — and lays each out as a polyline. unit formats
// the axis labels and the per-line summaries from raw values; an all-empty input
// renders an explicit empty state rather than a flat line at zero.
func LineChartOf(key, title string, series []ChartSeries, unit func(float64) string, span time.Duration) LineChart {
	max, any := 0.0, false
	peakX := 0.0
	for _, s := range series {
		for _, p := range s.Points {
			if !any || p.Y > max {
				peakX = p.X
			}
			any = true
			if p.Y > max {
				max = p.Y
			}
		}
	}
	lc := LineChart{Key: key, Title: title, XEnd: humanDur(span)}
	if !any {
		lc.Empty = true
		return lc
	}
	if max <= 0 {
		max = 1
	}
	lc.YTop, lc.YMid = unit(max), unit(max/2)
	lc.PeakX, lc.Peak = peakX*chartW, unit(max)
	lc.PeakAt = humanDur(time.Duration(peakX * float64(span)))
	lc.Ticks = axisTicks(span)

	for _, s := range series {
		var b strings.Builder
		var firstX, lastX float64
		lo, hi, sum := 0.0, 0.0, 0.0
		for i, p := range s.Points {
			if i > 0 {
				b.WriteByte(' ')
			}
			x := p.X * chartW
			y := chartTop + (1-p.Y/max)*(chartH-chartTop-chartBottom)
			fmt.Fprintf(&b, "%.2f,%.2f", x, y)
			if i == 0 {
				firstX, lo, hi = x, p.Y, p.Y
			}
			lastX = x
			lo, hi, sum = math.Min(lo, p.Y), math.Max(hi, p.Y), sum+p.Y
		}
		cl := ChartLine{Label: s.Label, Tone: s.Tone, Points: b.String(), Last: s.Last}
		if len(s.Points) > 0 {
			// The filled area is the line carried down to the baseline and back,
			// so the dither sits under the line.
			cl.Area = fmt.Sprintf("%s %.2f,%.2f %.2f,%.2f", b.String(), lastX, chartH, firstX, chartH)
			cl.Min, cl.Max = unit(lo), unit(hi)
			cl.Mean = unit(sum / float64(len(s.Points)))
		}
		lc.Lines = append(lc.Lines, cl)
	}
	return lc
}

// axisTicks labels the time axis at quarters of the run — enough to read when a
// spike happened without crowding the axis at any width.
func axisTicks(span time.Duration) []AxisTick {
	out := make([]AxisTick, 0, 5)
	for i := range 5 {
		at := float64(i) / 4
		out = append(out, AxisTick{At: at * 100, Label: humanDur(time.Duration(at * float64(span)))})
	}
	return out
}

// LinkCharts gives each chart the URL that opens it on its own.
func LinkCharts(charts []LineChart, base string) []LineChart {
	for i := range charts {
		charts[i].Href = base + "?chart=" + url.QueryEscape(charts[i].Key)
	}
	return charts
}

// SelectChart marks the chart at key as expanded and returns it. An unknown key
// expands nothing, so a stale link degrades to the grid of small multiples.
func SelectChart(charts []LineChart, key string) (*LineChart, []LineChart) {
	if key == "" {
		return nil, charts
	}
	for i := range charts {
		if charts[i].Key == key {
			charts[i].Expanded = true
			sel := charts[i]
			return &sel, charts
		}
	}
	return nil, charts
}

// timeFrac positions a bucket on the 0..1 axis by its start time, so an empty
// bucket keeps its place and lanes on different sampling grids share the axis.
func timeFrac(at, span time.Duration) float64 {
	if span <= 0 {
		return 0
	}
	return float64(at) / float64(span)
}

func ratePctText(v float64) string { return fmt.Sprintf("%.2f%%", v*100) }

// ThroughputChart plots achieved throughput (flow-runs per second) per bucket.
func ThroughputChart(s collector.Series) LineChart {
	span := SeriesSpan(s)
	pts := make([]ChartPoint, 0, len(s.Points))
	var last float64
	for _, p := range s.Points {
		rps := 0.0
		if s.Bucket > 0 {
			rps = float64(p.FlowRuns) / s.Bucket.Seconds()
		}
		pts = append(pts, ChartPoint{X: timeFrac(p.At, span), Y: rps})
		last = rps
	}
	return LineChartOf("throughput", "Throughput", []ChartSeries{{
		Label: "req/s", Tone: "kind-net", Points: pts, Last: fmt.Sprintf("%.0f/s", last),
	}}, func(v float64) string { return fmt.Sprintf("%.0f/s", v) }, span)
}

// LatencyChart plots p50/p95/p99 over time. Empty buckets carry zero percentiles
// (series.go), which would dive the line to zero, so they are skipped — the line
// bridges the gap instead of misreporting a fast window.
func LatencyChart(s collector.Series) LineChart {
	span := SeriesSpan(s)
	p50 := ChartSeries{Label: "p50", Tone: "kind-retry"}
	p95 := ChartSeries{Label: "p95", Tone: "kind-net"}
	p99 := ChartSeries{Label: "p99", Tone: "kind-logic"}
	var l50, l95, l99 time.Duration
	for _, p := range s.Points {
		if p.FlowRuns == 0 {
			continue
		}
		x := timeFrac(p.At, span)
		p50.Points = append(p50.Points, ChartPoint{X: x, Y: float64(p.P50)})
		p95.Points = append(p95.Points, ChartPoint{X: x, Y: float64(p.P95)})
		p99.Points = append(p99.Points, ChartPoint{X: x, Y: float64(p.P99)})
		l50, l95, l99 = p.P50, p.P95, p.P99
	}
	p50.Last, p95.Last, p99.Last = humanDur(l50), humanDur(l95), humanDur(l99)
	return LineChartOf("latency", "Latency", []ChartSeries{p50, p95, p99},
		func(v float64) string { return humanDur(time.Duration(v)) }, span)
}

// RatesChart plots error rate and throttle rate over time. Throttle keeps its own
// tone and never the failure red (ADR 0006).
func RatesChart(s collector.Series) LineChart {
	span := SeriesSpan(s)
	errs := ChartSeries{Label: "error", Tone: "failed"}
	thr := ChartSeries{Label: "throttled", Tone: "throttled"}
	var le, lt float64
	for _, p := range s.Points {
		x := timeFrac(p.At, span)
		e, t := 0.0, 0.0
		if p.FlowRuns > 0 {
			e = float64(p.Failed) / float64(p.FlowRuns)
			t = float64(p.Throttled) / float64(p.FlowRuns)
		}
		errs.Points = append(errs.Points, ChartPoint{X: x, Y: e})
		thr.Points = append(thr.Points, ChartPoint{X: x, Y: t})
		le, lt = e, t
	}
	errs.Last, thr.Last = ratePctText(le), ratePctText(lt)
	return LineChartOf("rates", "Outcome rates", []ChartSeries{errs, thr}, ratePctText, span)
}

// StepRow is one authored step in the per-step table.
type StepRow struct {
	Name  string
	Total string
	Count int64
	Mean  string
	Share string
}

// StepRows summarizes the authored steps (KindStep frames) from a run's folded
// aggregate, biggest first — the table beside the charts that says which step
// owns the time. Total is inclusive; Share is of the whole run.
func StepRows(f *span.Folded) []StepRow {
	frames := FlameFrames(f)
	var runTotal time.Duration
	for _, fr := range frames {
		if fr.Depth == 0 {
			runTotal += fr.Total
		}
	}

	steps := make([]Frame, 0)
	for _, fr := range frames {
		if fr.Kind == KindStep {
			steps = append(steps, fr)
		}
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Total > steps[j].Total })

	out := make([]StepRow, 0, len(steps))
	for _, fr := range steps {
		mean := time.Duration(0)
		if fr.Count > 0 {
			mean = fr.Total / time.Duration(fr.Count)
		}
		share := 0.0
		if runTotal > 0 {
			share = float64(fr.Total) / float64(runTotal) * 100
		}
		out = append(out, StepRow{
			Name:  fr.Name,
			Total: humanDur(fr.Total),
			Count: fr.Count,
			Mean:  humanDur(mean),
			Share: fmt.Sprintf("%.1f%%", share),
		})
	}
	return out
}

// TrendSection is the soak drift view: the run's second half measured against its
// first, plus any persisted trend findings. It is computed from the bucketed
// series (not raw samples), so it tracks EvaluateTrends without reproducing it
// exactly — enough to see creep, with the flags carrying the authoritative call.
type TrendSection struct {
	Tiles []Tile   // p95 / error / throttle: first half → second half
	Flags []string // persisted drift findings (Outcome.Detail), if any
	Note  string
}

// TrendFrom splits a run's series at its midpoint and reports how p95, error rate
// and throttle rate drifted from the first half to the second, reusing the
// comparison tiles. It returns nil for a run too short to have two halves.
func TrendFrom(s collector.Series, thresholds []collector.Outcome) *TrendSection {
	span := SeriesSpan(s)
	if span <= 0 || len(s.Points) < 2 {
		return nil
	}
	mid := span / 2

	var early, late half
	for _, p := range s.Points {
		if p.FlowRuns == 0 {
			continue
		}
		if p.At < mid {
			early.add(p)
		} else {
			late.add(p)
		}
	}
	if early.runs == 0 || late.runs == 0 {
		return nil
	}

	ts := &TrendSection{Tiles: []Tile{
		DurDelta("p95 latency", late.p95(), early.p95()),
		RateDelta("error rate", late.errRate(), early.errRate(), "failed"),
		RateDelta("throttle rate", late.throttleRate(), early.throttleRate(), "throttled"),
	}}
	for _, o := range thresholds {
		if strings.Contains(o.Expr, "trend") {
			ts.Flags = append(ts.Flags, o.Detail)
		}
	}
	if len(ts.Flags) == 0 {
		ts.Note = "no drift beyond the soak thresholds — a clean soak is flat"
	}
	return ts
}

// half accumulates one side of the midpoint split, flow-run-weighted so a busier
// window counts for more than a sparse one.
type half struct {
	runs   int
	failed int
	thr    int
	p95w   float64
}

func (h *half) add(p collector.SeriesPoint) {
	h.runs += p.FlowRuns
	h.failed += p.Failed
	h.thr += p.Throttled
	h.p95w += float64(p.P95) * float64(p.FlowRuns)
}

func (h half) p95() time.Duration {
	if h.runs == 0 {
		return 0
	}
	return time.Duration(h.p95w / float64(h.runs))
}
func (h half) errRate() float64 {
	if h.runs == 0 {
		return 0
	}
	return float64(h.failed) / float64(h.runs)
}
func (h half) throttleRate() float64 {
	if h.runs == 0 {
		return 0
	}
	return float64(h.thr) / float64(h.runs)
}
