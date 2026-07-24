package report_test

import (
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/report"
	"github.com/blackprince001/flowbench/internal/span"
)

// slowTrace is trace() with the checkout step inflated — a stand-in for a run
// where that step regressed against a baseline.
func slowTrace() *span.Span {
	root := span.New("flow:checkout", 0)
	root.Duration = 200 * time.Millisecond

	step := span.New("checkout", 0)
	step.Duration = 190 * time.Millisecond
	root.Children = []*span.Span{step}

	call := step.Child("http_call", 5*time.Millisecond)
	call.Duration = 180 * time.Millisecond
	call.Child("ttfb", 10*time.Millisecond).Duration = 170 * time.Millisecond
	return root
}

func TestDurDeltaTellsDirectionWithoutColourAlone(t *testing.T) {
	slower := report.DurDelta("p95", 812*time.Millisecond, 800*time.Millisecond)
	if slower.Tone != "failed" {
		t.Errorf("a slower percentile is a regression, want failed tone, got %q", slower.Tone)
	}
	if !strings.Contains(slower.Value, "▲") || !strings.Contains(slower.Value, "+") {
		t.Errorf("the increase must carry a glyph and a sign, got %q", slower.Value)
	}

	faster := report.DurDelta("p95", 700*time.Millisecond, 800*time.Millisecond)
	if faster.Tone != "ok" || !strings.Contains(faster.Value, "▼") {
		t.Errorf("a faster percentile should read as an ok-toned decrease, got %+v", faster)
	}

	same := report.DurDelta("p95", 800*time.Millisecond, 800*time.Millisecond)
	if same.Tone != "" || !strings.Contains(same.Value, "no change") {
		t.Errorf("an unchanged percentile should be neutral, got %+v", same)
	}
}

// A rising throttle rate is a regression, but throttled owns its colour and
// never borrows the failure red (ADR 0006).
func TestRateDeltaKeepsThrottleOutOfFailureRed(t *testing.T) {
	throttle := report.RateDelta("throttle rate", 0.40, 0.10, "throttled")
	if throttle.Tone != "throttled" {
		t.Errorf("a rising throttle rate keeps the throttle tone, got %q", throttle.Tone)
	}
	if !strings.Contains(throttle.Value, "▲") {
		t.Errorf("the rise must carry a direction glyph, got %q", throttle.Value)
	}

	errs := report.RateDelta("error rate", 0.05, 0.01, "failed")
	if errs.Tone != "failed" {
		t.Errorf("a rising error rate is a failure, got %q", errs.Tone)
	}

	better := report.RateDelta("error rate", 0.00, 0.05, "failed")
	if better.Tone != "ok" || !strings.Contains(better.Value, "▼") {
		t.Errorf("a falling error rate should read as an ok-toned drop, got %+v", better)
	}
}

func TestThresholdFlipsClassifiesAndOrders(t *testing.T) {
	base := []collector.Outcome{
		{Expr: "p95(latency) < 800ms", Pass: true},
		{Expr: "error_rate < 1%", Pass: false},
		{Expr: "throttle_rate < 5%", Pass: true},
	}
	cur := []collector.Outcome{
		{Expr: "p95(latency) < 800ms", Pass: false}, // passed, now fails
		{Expr: "error_rate < 1%", Pass: true},       // failed, now passes
		{Expr: "max(latency) < 2s", Pass: true},     // only in this run
	}

	flips := report.ThresholdFlips(cur, base)
	kind := map[string]string{}
	for _, f := range flips {
		kind[f.Expr] = f.Kind
	}
	for expr, want := range map[string]string{
		"p95(latency) < 800ms": "regressed",
		"error_rate < 1%":      "fixed",
		"max(latency) < 2s":    "added",
		"throttle_rate < 5%":   "removed", // only in the baseline
	} {
		if kind[expr] != want {
			t.Errorf("%q classified %q, want %q", expr, kind[expr], want)
		}
	}
	// The regression is the headline, so it sorts first.
	if flips[0].Kind != "regressed" || flips[0].Tone != "failed" {
		t.Errorf("regressions should lead and be failure-toned, got %+v", flips[0])
	}
}

func TestMarkRegressedStepFindsTheSlowedStep(t *testing.T) {
	base := span.NewFolded()
	base.Add(trace(0)) // checkout step ~90ms
	cur := span.NewFolded()
	cur.Add(slowTrace()) // checkout step ~190ms

	curFrames := report.FlameFrames(cur)
	reg := report.MarkRegressedStep(curFrames, report.FlameFrames(base))
	if reg == nil || reg.Step != "checkout" {
		t.Fatalf("the inflated step should be flagged, got %+v", reg)
	}
	if !strings.HasPrefix(reg.Delta, "+") {
		t.Errorf("the growth should be a signed positive delta, got %q", reg.Delta)
	}

	// The frame is marked in place, the way SelectFrame marks a selection.
	marked := false
	for _, f := range curFrames {
		if f.Path == reg.Path && f.Regressed {
			marked = true
		}
	}
	if !marked {
		t.Error("the regressed frame should be marked in the current run's frames")
	}

	// A run compared against itself has regressed nothing.
	if got := report.MarkRegressedStep(report.FlameFrames(base), report.FlameFrames(base)); got != nil {
		t.Errorf("nothing regressed against itself, got %+v", got)
	}
}

func TestRenderCompareShowsDeltasAndTheRegressedStep(t *testing.T) {
	base := span.NewFolded()
	base.Add(trace(0))
	cur := span.NewFolded()
	cur.Add(slowTrace())

	framesCur := report.FlameFrames(cur)
	reg := report.MarkRegressedStep(framesCur, report.FlameFrames(base))

	var sb strings.Builder
	err := report.RenderCompare(&sb, report.ComparePage{
		Shell:    shell(),
		RunHead:  runHead(),
		Cur:      report.CompareRef{ID: "20260724T140000Z", When: "2026-07-24 14:00", Verdict: report.Verdict{Tone: "failed", Label: "breached"}, FlameHref: "/runs/a/flame"},
		Baseline: report.CompareRef{ID: "20260724T130000Z", When: "2026-07-24 13:00", Verdict: report.Verdict{Tone: "ok", Label: "passed"}, FlameHref: "/runs/b/flame"},
		Baselines: []report.BaselineOption{
			{ID: "20260724T130000Z", When: "07-24 13:00", Href: "/runs/a/compare?with=20260724T130000Z", Selected: true},
		},
		Deltas:     []report.Tile{report.DurDelta("p95", 190*time.Millisecond, 90*time.Millisecond)},
		Flips:      []report.Flip{{Expr: "p95(latency) < 100ms", Kind: "regressed", Tone: "failed", Detail: "p95 = 190ms, want < 100ms"}},
		Regressed:  reg,
		FramesCur:  framesCur,
		FramesBase: report.FlameFrames(base),
		CurTotal:   "200ms",
		BaseTotal:  "100ms",
	})
	if err != nil {
		t.Fatalf("RenderCompare: %v", err)
	}

	check(t, sb.String(),
		"Regression comparison",
		"20260724T130000Z",                // the baseline is identified
		"biggest regression",              // the finding is stated in words
		"checkout",                        // and names the step
		"frame k-step is-regressed",       // the frame is marked
		`class="chip o-failed">regressed`, // the flip is named and toned
		"▲",                               // deltas carry a direction glyph, not colour alone
	)
}

func TestRenderCompareEmptyStateHidesTheDiff(t *testing.T) {
	var sb strings.Builder
	err := report.RenderCompare(&sb, report.ComparePage{
		Shell:   shell(),
		RunHead: runHead(),
		Cur:     report.CompareRef{ID: "x", When: "now", Verdict: report.Verdict{Tone: "ok", Label: "clean"}},
		Note:    "no earlier run of this scenario to compare against",
	})
	if err != nil {
		t.Fatalf("RenderCompare: %v", err)
	}
	out := sb.String()
	check(t, out, "no earlier run of this scenario")
	if strings.Contains(out, "Metric deltas") {
		t.Error("the empty state must not render the delta section")
	}
}
