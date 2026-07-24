package report

import (
	"fmt"
	"sort"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
)

// ComparePage is one run set beside a baseline: the metric deltas, the gates
// that changed verdict, the step that regressed most, and both flame graphs. A
// baseline is chosen — the previous run of the same scenario by default — so a
// change is reviewed like a diff rather than eyeballed (PRD 10.7).
type ComparePage struct {
	Shell
	RunHead // the run being examined (this run)

	Cur       CompareRef
	Baseline  CompareRef
	Baselines []BaselineOption // the other runs of this scenario, to pick between
	Note      string           // set when there is no baseline; the rest is then blank

	Deltas     []Tile
	Flips      []Flip
	Regressed  *Regression
	FramesCur  []Frame
	FramesBase []Frame
	CurTotal   string
	BaseTotal  string
}

// CompareRef identifies one side of the comparison.
type CompareRef struct {
	ID        string
	When      string
	Verdict   Verdict
	FlameHref string
}

// BaselineOption is one selectable baseline in the picker.
type BaselineOption struct {
	ID       string
	When     string
	Href     string
	Selected bool
}

// DurDelta compares a latency percentile against the baseline's. Slower is the
// regression the view exists to surface, so it takes the failure tone; faster
// takes the ok tone. The glyph, the sign and the word all carry the direction
// so it never rides on colour alone (ADR 0006).
func DurDelta(label string, cur, base time.Duration) Tile {
	t := Tile{Label: label}
	switch d := cur - base; {
	case d > 0:
		t.Tone = "failed"
		t.Value = "▲ +" + humanDur(d)
		t.Sub = fmt.Sprintf("slower — %s → %s", humanDur(base), humanDur(cur))
	case d < 0:
		t.Tone = "ok"
		t.Value = "▼ −" + humanDur(-d)
		t.Sub = fmt.Sprintf("faster — %s → %s", humanDur(base), humanDur(cur))
	default:
		t.Value = "no change"
		t.Sub = humanDur(cur)
	}
	return t
}

// RateDelta compares an error or throttle rate (a 0..1 fraction). worseTone lets
// a rising throttle rate take the throttle amber rather than the failure red,
// since a throttle is a signal of its own, not a failure (ADR 0006).
func RateDelta(label string, cur, base float64, worseTone string) Tile {
	t := Tile{Label: label}
	switch d := cur - base; {
	case d > rateEpsilon:
		t.Tone = worseTone
		t.Value = "▲ +" + ratePct(d)
		t.Sub = fmt.Sprintf("more — %s → %s", ratePct(base), ratePct(cur))
	case d < -rateEpsilon:
		t.Tone = "ok"
		t.Value = "▼ −" + ratePct(-d)
		t.Sub = fmt.Sprintf("fewer — %s → %s", ratePct(base), ratePct(cur))
	default:
		t.Value = "no change"
		t.Sub = ratePct(cur)
	}
	return t
}

// rateEpsilon keeps a change below a hundredth of a percent from reading as a
// regression when it is really just float noise between two runs.
const rateEpsilon = 5e-5

func ratePct(v float64) string { return fmt.Sprintf("%.2f%%", v*100) }

// Flip is one threshold compared between the two runs, keyed by its expression.
// A gate that passed in the baseline and fails now is the headline regression;
// the reverse is a fix. Gates present on only one side are reported rather than
// silently dropped, so a threshold added or removed between runs is visible.
type Flip struct {
	Expr   string
	Detail string // the current run's measured detail, or the baseline's if only there
	Kind   string // regressed | fixed | added | removed | unchanged
	Tone   string
}

var flipRank = map[string]int{"regressed": 0, "added": 1, "removed": 2, "fixed": 3, "unchanged": 4}

// ThresholdFlips aligns two runs' evaluated thresholds by expression, ordering
// the regressions first so the worst news reads before anything else.
func ThresholdFlips(cur, base []collector.Outcome) []Flip {
	byExpr := func(os []collector.Outcome) map[string]collector.Outcome {
		m := make(map[string]collector.Outcome, len(os))
		for _, o := range os {
			m[o.Expr] = o
		}
		return m
	}
	baseByExpr := byExpr(base)
	seen := make(map[string]bool, len(cur))

	out := make([]Flip, 0, len(cur))
	for _, o := range cur {
		seen[o.Expr] = true
		bo, ok := baseByExpr[o.Expr]
		f := Flip{Expr: o.Expr, Detail: o.Detail}
		switch {
		case !ok:
			f.Kind = "added"
		case o.Pass == bo.Pass:
			f.Kind = "unchanged"
		case !o.Pass: // passed in the baseline, fails now
			f.Kind, f.Tone = "regressed", "failed"
		default: // failed in the baseline, passes now
			f.Kind, f.Tone = "fixed", "ok"
		}
		out = append(out, f)
	}
	for _, o := range base {
		if !seen[o.Expr] {
			out = append(out, Flip{Expr: o.Expr, Detail: o.Detail, Kind: "removed"})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return flipRank[out[i].Kind] < flipRank[out[j].Kind] })
	return out
}

// Regression names the authored step whose total time grew most between the
// baseline and this run — the acceptance question "which step regressed". A leaf
// phase that slowed shows up as its step's growth, which is the unit an engineer
// reasons about, so the step is what to point at.
type Regression struct {
	Step  string
	Path  string
	Delta string // e.g. "+180ms"
}

// MarkRegressedStep aligns the two runs' frames by span path, finds the step
// (KindStep) whose total grew most, marks that frame in the current run's frames
// (is-regressed) and returns the finding — or nil when no step got slower. The
// frames slice is mutated in place, the same way SelectFrame marks a selection.
func MarkRegressedStep(cur, base []Frame) *Regression {
	baseTotal := make(map[string]time.Duration, len(base))
	for _, f := range base {
		baseTotal[f.Path] = f.Total
	}

	worst, grew := -1, time.Duration(0)
	for i := range cur {
		if cur[i].Kind != KindStep {
			continue
		}
		// A step absent from the baseline has a zero total there, so a brand-new
		// step counts as grown by its whole total — a regression from nothing.
		if d := cur[i].Total - baseTotal[cur[i].Path]; d > grew {
			worst, grew = i, d
		}
	}
	if worst < 0 {
		return nil
	}
	cur[worst].Regressed = true
	return &Regression{Step: cur[worst].Name, Path: cur[worst].Path, Delta: "+" + humanDur(grew)}
}
