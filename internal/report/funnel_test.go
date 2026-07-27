package report_test

import (
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/report"
	"github.com/blackprince001/flowbench/internal/span"
)

// trace builds one flow trace with the named steps, in order.
func flowTrace(flow string, steps ...string) *span.Span {
	root := span.New("flow:"+flow, 0)
	for _, s := range steps {
		root.Child(s, 0)
	}
	return root
}

// foldRuns folds n copies of a trace whose steps stop after `reached` of them,
// which is what an abort_flow failure produces.
func foldRuns(f *span.Folded, flow string, n int, steps []string, reached int) {
	for i := 0; i < n; i++ {
		f.Add(flowTrace(flow, steps[:reached]...))
	}
}

// The order comes from a trace and the counts from the fold, because neither
// tier has both: FoldNode.Children is a map, and a funnel drawn in map order is
// not a funnel.
func TestFunnelTakesOrderFromTheTraceAndCountsFromTheFold(t *testing.T) {
	steps := []string{"login", "browse", "checkout"}
	f := span.NewFolded()
	foldRuns(f, "shop", 100, steps, 3)

	funnels := report.Funnels(f, []*span.Span{flowTrace("shop", steps...)})
	if len(funnels) != 1 {
		t.Fatalf("want one funnel, got %d", len(funnels))
	}
	got := funnels[0]
	if got.Flow != "shop" {
		t.Errorf("flow = %q, want shop (the flow: prefix is the fold's, not the author's)", got.Flow)
	}
	var names []string
	for _, s := range got.Stages {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "login,browse,checkout" {
		t.Errorf("stages = %v, want the flow's own order", names)
	}
	for _, s := range got.Stages {
		if s.Count != 100 {
			t.Errorf("stage %q count = %d, want 100", s.Name, s.Count)
		}
	}
}

// The narrowing is the point: a step that aborts the flow leaves the steps
// after it with fewer flow-runs, and that is the drop the graph exists to show.
func TestFunnelNarrowsWhereFlowRunsStop(t *testing.T) {
	steps := []string{"login", "browse", "checkout"}
	f := span.NewFolded()
	foldRuns(f, "shop", 700, steps, 3) // reached the end
	foldRuns(f, "shop", 250, steps, 2) // stopped after browse
	foldRuns(f, "shop", 50, steps, 1)  // stopped after login

	got := report.Funnels(f, []*span.Span{flowTrace("shop", steps...)})[0]
	want := []int64{1000, 950, 700}
	for i, s := range got.Stages {
		if s.Count != want[i] {
			t.Errorf("stage %q count = %d, want %d", s.Name, s.Count, want[i])
		}
	}
	if got.Stages[2].Share != "70.0%" {
		t.Errorf("last share = %q, want 70.0%%", got.Stages[2].Share)
	}
	if got.Stages[0].Drop != "" {
		t.Errorf("the first stage has nothing to drop from, got %q", got.Stages[0].Drop)
	}
	if !strings.Contains(got.Stages[2].Drop, "250") {
		t.Errorf("stage 3 drop = %q, want it to name the 250 lost at that step", got.Stages[2].Drop)
	}
	if !strings.Contains(got.Note, "300 stopped") {
		t.Errorf("note = %q, want the total attrition", got.Note)
	}

	// Every band is three closed layers — two halos and a core — because that
	// is what makes the ribbon read as flowing rather than as a filled polygon.
	if len(got.Bands) != 3 {
		t.Fatalf("want one band per stage, got %d", len(got.Bands))
	}
	for i, b := range got.Bands {
		for name, d := range map[string]string{"core": b.Core, "halo-in": b.HaloIn, "halo-out": b.HaloOut} {
			if !strings.HasPrefix(d, "M") || !strings.HasSuffix(d, "Z") {
				t.Errorf("band %d %s is not a closed path: %.40s", i, name, d)
			}
		}
	}
}

// A flat funnel is a finding — no step ever aborted the flow — so it says that
// rather than leaving a reader to infer it from equal widths.
func TestFlatFunnelSaysNothingStopped(t *testing.T) {
	steps := []string{"connect", "subscribe"}
	f := span.NewFolded()
	foldRuns(f, "feed", 805, steps, 2)

	got := report.Funnels(f, []*span.Span{flowTrace("feed", steps...)})[0]
	if !strings.Contains(got.Note, "no flow-run stopped early") {
		t.Errorf("note = %q", got.Note)
	}
}

// A stage holding a fraction of a percent must still be visible: "the last step
// is missing" is the opposite of what the graph should say. The count is
// printed on it, so the number stays exact where the thickness is only nearly.
func TestATinyStageStaysVisible(t *testing.T) {
	steps := []string{"a", "b"}
	f := span.NewFolded()
	foldRuns(f, "flow", 1996, steps, 1)
	foldRuns(f, "flow", 4, steps, 2) // 4 of 2000 — 0.2%

	got := report.Funnels(f, []*span.Span{flowTrace("flow", steps...)})[0]
	if got.Stages[1].Share != "0.2%" {
		t.Fatalf("share = %q, want 0.2%%", got.Stages[1].Share)
	}
	// The core must not have collapsed onto the centre line, and the halo has to
	// stand clear of it — a hairline stage is only visible because of the glow
	// around it.
	if strings.Contains(got.Bands[1].Core, "30.00 30.00") {
		t.Errorf("the thin stage collapsed to a line: %s", got.Bands[1].Core)
	}
	if got.Bands[1].HaloOut == got.Bands[1].Core {
		t.Error("the halo is the same shape as the core, so it adds nothing")
	}
}

// A step no flow-run ever reached has no fold node at all, because no span was
// ever created for it. Dropping it would turn the most interesting funnel there
// is — nobody got past step one — into one that looks like the flow ends there.
func TestAStepNobodyReachedIsAStageAtZero(t *testing.T) {
	steps := []string{"login", "browse", "checkout"}
	f := span.NewFolded()
	foldRuns(f, "shop", 40, steps, 1) // every flow-run stopped after login

	// One flow-run got through earlier in the run, so a kept trace still knows
	// the full step order.
	got := report.Funnels(f, []*span.Span{flowTrace("shop", steps...)})[0]
	if len(got.Stages) != 3 {
		t.Fatalf("want all 3 stages, got %d", len(got.Stages))
	}
	if got.Stages[1].Count != 0 || got.Stages[2].Count != 0 {
		t.Errorf("unreached stages = %d, %d, want 0, 0", got.Stages[1].Count, got.Stages[2].Count)
	}
	if got.Stages[2].Share != "0.0%" {
		t.Errorf("share = %q, want 0.0%%", got.Stages[2].Share)
	}
	if !strings.Contains(got.Note, "40 stopped") {
		t.Errorf("note = %q, want it to say every flow-run stopped", got.Note)
	}
}

// Without a kept trace nothing knows the step order, and drawing the fold's map
// order would invent one.
func TestNoTraceMeansNoFunnel(t *testing.T) {
	f := span.NewFolded()
	foldRuns(f, "shop", 10, []string{"a", "b"}, 2)
	if got := report.Funnels(f, nil); len(got) != 0 {
		t.Errorf("want no funnel without a trace, got %d", len(got))
	}
}

// A funnel of one stage is a number, and the summary tiles already say it.
func TestSingleStepFlowIsNotAFunnel(t *testing.T) {
	f := span.NewFolded()
	foldRuns(f, "ping", 10, []string{"only"}, 1)
	if got := report.Funnels(f, []*span.Span{flowTrace("ping", "only")}); len(got) != 0 {
		t.Errorf("want no funnel for a one-step flow, got %d", len(got))
	}
}

// --- streamgraph ---------------------------------------------------------

func outcomeSeries(points ...collector.SeriesPoint) collector.Series {
	return collector.Series{Bucket: time.Second, Points: points}
}

// The bands stack, so each one's top edge is the one below it's bottom: that is
// what keeps a stacked graph flush instead of leaving hairline gaps.
func TestStreamStacksOkThrottledFailed(t *testing.T) {
	s := outcomeSeries(
		collector.SeriesPoint{At: 0, FlowRuns: 100, OK: 100},
		collector.SeriesPoint{At: time.Second, FlowRuns: 100, OK: 50, Throttled: 40, Failed: 10},
	)
	got := report.StreamOf(report.Strip(s))
	if got.Empty {
		t.Fatal("stream should not be empty")
	}
	var tones []string
	for _, b := range got.Bands {
		tones = append(tones, b.Tone)
	}
	if strings.Join(tones, ",") != "ok,throttled,failed" {
		t.Errorf("bands = %v, want ok,throttled,failed in stacking order", tones)
	}
}

// An outcome that never happened gets no band, so a clean run is one ribbon
// rather than three with two of them empty.
func TestStreamOmitsOutcomesThatNeverHappened(t *testing.T) {
	s := outcomeSeries(collector.SeriesPoint{At: 0, FlowRuns: 10, OK: 10})
	got := report.StreamOf(report.Strip(s))
	if len(got.Bands) != 1 || got.Bands[0].Tone != "ok" {
		t.Fatalf("bands = %+v, want just ok", got.Bands)
	}
}

// The strip's links, tooltips and selection survive the swap: the graph changed,
// the interaction did not.
func TestStreamKeepsTheBucketHitAreas(t *testing.T) {
	s := outcomeSeries(
		collector.SeriesPoint{At: 0, FlowRuns: 10, OK: 10},
		collector.SeriesPoint{At: time.Second, FlowRuns: 10, OK: 10},
		collector.SeriesPoint{At: 2 * time.Second, FlowRuns: 10, OK: 10},
	)
	cells := report.StripLinks(report.Strip(s), "/p/x/runs/1", 1)
	got := report.StreamOf(cells)

	if len(got.Cells) != 3 {
		t.Fatalf("want one hit area per bucket, got %d", len(got.Cells))
	}
	if got.Cells[1].Href == "" || !got.Cells[1].Selected {
		t.Errorf("bucket 1 should be linked and selected, got %+v", got.Cells[1])
	}
	if got.Cells[0].Title == "" {
		t.Error("a hit area with no tooltip loses the numbers the strip carried")
	}
	// The hit areas must tile the full width, or a click lands on nothing.
	var covered float64
	for _, c := range got.Cells {
		covered += c.W
	}
	if covered < 99.9 || covered > 100.1 {
		t.Errorf("hit areas cover %.2f of the width, want 100", covered)
	}
}

// A band too thin to hold its own label does not get one; the tallies row above
// already carries the number, and a pill overflowing its band would point at
// the wrong colour.
func TestThinBandsGetNoInlineLabel(t *testing.T) {
	s := outcomeSeries(collector.SeriesPoint{At: 0, FlowRuns: 1000, OK: 995, Failed: 5})
	got := report.StreamOf(report.Strip(s))
	for _, m := range got.Marks {
		if m.Tone == "failed" {
			t.Errorf("0.5%% band should carry no inline label, got %q", m.Text)
		}
	}
	if len(got.Marks) != 1 || got.Marks[0].Tone != "ok" {
		t.Errorf("marks = %+v, want just the ok band labelled", got.Marks)
	}
}

func TestEmptySeriesIsEmpty(t *testing.T) {
	if got := report.StreamOf(nil); !got.Empty {
		t.Error("a series with no points should render nothing")
	}
}
