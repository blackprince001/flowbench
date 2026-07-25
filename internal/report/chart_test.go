package report_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/report"
)

const ms = time.Millisecond

func intFmt(v float64) string { return fmt.Sprintf("%.0f", v) }

// A run's series: two live buckets around one empty one, so the empty-bucket and
// scaling behaviour both get exercised.
func seriesFixture() collector.Series {
	return collector.Series{Bucket: time.Second, Points: []collector.SeriesPoint{
		{At: 0, FlowRuns: 10, OK: 9, Failed: 1, Throttled: 2, P50: 5 * ms, P95: 20 * ms, P99: 30 * ms},
		{At: time.Second, FlowRuns: 0}, // an idle window — no throughput, no percentiles
		{At: 2 * time.Second, FlowRuns: 20, OK: 15, Failed: 5, Throttled: 0, P50: 6 * ms, P95: 40 * ms, P99: 60 * ms},
	}}
}

func TestLineChartScalesToMax(t *testing.T) {
	lc := report.LineChartOf("t", "T", []report.ChartSeries{{
		Label:  "a",
		Tone:   "net",
		Points: []report.ChartPoint{{X: 0, Y: 0}, {X: 0.5, Y: 5}, {X: 1, Y: 10}},
		Last:   "10",
	}}, intFmt, 5*time.Second)

	if lc.Empty {
		t.Fatal("a series with points is not empty")
	}
	if lc.YTop != "10" {
		t.Errorf("y-axis max label = %q, want 10", lc.YTop)
	}
	// max is 10, with 4 units of headroom top and bottom (usable 4..36): Y=0 maps
	// near the bottom (y=36), Y=10 near the top (y=4), the midpoint at 20.
	pts := lc.Lines[0].Points
	for _, want := range []string{"0.00,36.00", "50.00,20.00", "100.00,4.00"} {
		if !strings.Contains(pts, want) {
			t.Errorf("polyline %q missing point %q", pts, want)
		}
	}
}

func TestLineChartEmptyState(t *testing.T) {
	lc := report.LineChartOf("t", "T", []report.ChartSeries{{Label: "a"}}, intFmt, 0)
	if !lc.Empty || len(lc.Lines) != 0 {
		t.Errorf("a series with no points should render an empty state, got %+v", lc)
	}
}

func TestThroughputAndRateAndLatencyCharts(t *testing.T) {
	s := seriesFixture()

	rps := report.ThroughputChart(s)
	if rps.Empty || rps.Lines[0].Last != "20/s" { // last live bucket: 20 flow-runs / 1s
		t.Errorf("throughput last = %q, want 20/s", rps.Lines[0].Last)
	}

	// Latency skips the empty middle bucket, so p95 has two points, not three.
	lat := report.LatencyChart(s)
	var p95 report.ChartLine
	for _, l := range lat.Lines {
		if l.Label == "p95" {
			p95 = l
		}
	}
	// Two live buckets -> two "x,y" pairs -> exactly one separating space.
	if strings.Count(p95.Points, " ") != 1 {
		t.Errorf("p95 should plot two points (empty bucket skipped), got %q", p95.Points)
	}
	if !strings.Contains(p95.Last, "ms") {
		t.Errorf("p95 last should be a duration, got %q", p95.Last)
	}

	rates := report.RatesChart(s)
	var errs report.ChartLine
	for _, l := range rates.Lines {
		if l.Label == "error" {
			errs = l
		}
	}
	if errs.Tone != "failed" {
		t.Errorf("error line should take the failure tone, got %q", errs.Tone)
	}
	if errs.Last != "25.00%" { // last bucket: 5 failed / 20
		t.Errorf("error rate last = %q, want 25.00%%", errs.Last)
	}
}

func TestStepRowsFromFolded(t *testing.T) {
	rows := report.StepRows(folded()) // folds trace(0) x3: one "checkout" step
	if len(rows) != 1 {
		t.Fatalf("want one authored step, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Name != "checkout" {
		t.Errorf("step name = %q, want checkout", r.Name)
	}
	if r.Count != 3 {
		t.Errorf("step folded %d calls, want 3", r.Count)
	}
	if !strings.HasSuffix(r.Share, "%") {
		t.Errorf("share should be a percentage, got %q", r.Share)
	}
}

func TestTrendFromReportsDrift(t *testing.T) {
	s := collector.Series{Bucket: time.Second, Points: []collector.SeriesPoint{
		{At: 0, FlowRuns: 10, P95: 10 * ms},
		{At: 1 * time.Second, FlowRuns: 10, P95: 12 * ms},
		{At: 2 * time.Second, FlowRuns: 10, P95: 30 * ms, Failed: 2},
		{At: 3 * time.Second, FlowRuns: 10, P95: 35 * ms, Failed: 3},
	}}
	ts := report.TrendFrom(s, []collector.Outcome{
		{Expr: "p95(latency) trend", Detail: "p95 latency crept 11ms → 32ms over the run"},
		{Expr: "error_rate < 1%", Pass: true}, // not a trend flag; must be ignored
	})
	if ts == nil {
		t.Fatal("a run with two halves should produce a trend")
	}
	if len(ts.Tiles) != 3 || ts.Tiles[0].Tone != "failed" {
		t.Errorf("p95 drifted up, so the first tile should be failure-toned: %+v", ts.Tiles)
	}
	if len(ts.Flags) != 1 || !strings.Contains(ts.Flags[0], "crept") {
		t.Errorf("only the persisted trend finding should be surfaced, got %+v", ts.Flags)
	}
}

func TestTrendFromNilWhenTooShort(t *testing.T) {
	if got := report.TrendFrom(collector.Series{Bucket: time.Second, Points: []collector.SeriesPoint{{At: 0, FlowRuns: 5, P95: ms}}}, nil); got != nil {
		t.Errorf("a single-bucket run has no two halves to compare, got %+v", got)
	}
}

// The run page carries what the dashboard used to: overview and time-series are
// one page now, so the numbers and the shape that produced them read together.
func TestRenderRunPageCharts(t *testing.T) {
	var sb strings.Builder
	err := report.RenderRun(&sb, report.RunPage{
		Shell:   shell(),
		RunHead: runHead(),
		Charts:  report.LinkCharts([]report.LineChart{report.ThroughputChart(seriesFixture()), report.LatencyChart(seriesFixture()), report.RatesChart(seriesFixture())}, "/run"),
		Steps:   report.StepRows(folded()),
		Gates:   []report.Gate{{Expr: "error_rate < 2%", Pass: true, Tone: "ok", Detail: "error_rate = 1%, want < 2%"}},
		Agent:   "no agent attached — target CPU/memory overlay lands with #32",
	})
	if err != nil {
		t.Fatalf("RenderRun: %v", err)
	}
	check(t, sb.String(),
		"Over the run",
		"<polyline",                      // the line geometry rendered
		"<polygon",                       // and the area under it
		"dither-mask",                    // filled through the shared dither mask
		`var(--kind-net)`,                // tones resolve to real tokens (not the grey fallback)
		"chart-in",                       // the entrance animation shipped in the inlined CSS
		"prefers-reduced-motion: reduce", // guarded for reduced motion
		"req/s",                          // throughput legend
		"chart-hit",                      // the whole chart is the target, not its title
		"Steps", "checkout",              // per-step table
		"error_rate", "want", // thresholds
		"target CPU/memory overlay lands with #32", // the deferred agent slot
	)
}
