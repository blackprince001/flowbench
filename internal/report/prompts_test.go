package report_test

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/report"
	"github.com/blackprince001/flowbench/internal/span"
)

// observed builds a trace shaped the way the Python-driven producer writes one:
// a flow root, a step, and one observation span per variant carrying the pair.
func observed(pairs ...*span.Span) *span.Span {
	root := span.New("flow:triage", 0)
	step := root.Child("classify", 0)
	step.Children = append(step.Children, pairs...)
	return root
}

func observation(name, variant, prompt, completion, hash string) *span.Span {
	sp := span.New(name, 0)
	sp.Duration = 20 * time.Millisecond
	sp.Payload = &span.Payload{
		Prompt: prompt, Completion: completion, PromptHash: hash, Variant: variant,
		Usage: &span.Usage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14},
	}
	return sp
}

func query() report.PromptQuery {
	return report.PromptQuery{Base: "/p/x/runs/1/prompts", Mode: "split"}
}

func TestObservationsGroupVariantsUnderOneIdentity(t *testing.T) {
	traces := []*span.Span{
		observed(
			observation("classify@concise", "concise", "P1", "refund", "aaa"),
			observation("classify@verbose", "verbose", "P2", "a refund, because…", "bbb"),
		),
		observed(
			observation("classify@concise", "concise", "P1", "refund", "aaa"),
			observation("classify@verbose", "verbose", "P2", "a refund, since…", "bbb"),
		),
	}

	groups := report.Observations(traces)
	if len(groups) != 1 {
		t.Fatalf("two variants of one prompt are one group, got %d", len(groups))
	}
	g := groups[0]
	if g.Base != "classify" || g.Step != "flow:triage.classify" {
		t.Errorf("group identity = %q in %q", g.Base, g.Step)
	}
	if len(g.Variants) != 2 {
		t.Fatalf("want two variants, got %d", len(g.Variants))
	}
	concise, verbose := g.Variants[0], g.Variants[1]
	if concise.Count != 2 || concise.Distinct != 1 {
		t.Errorf("concise recorded twice, identically: %+v", concise)
	}
	// The verbose variant answered differently each iteration — worth knowing
	// before trusting a single sample of it.
	if verbose.Distinct != 2 {
		t.Errorf("verbose completions differ across iterations: %+v", verbose)
	}
}

func TestObservationsIgnoreOrdinaryCallSpans(t *testing.T) {
	root := span.New("flow:checkout", 0)
	step := root.Child("pay", 0)
	call := step.Child("POST /v1/pay", 0)
	call.Payload = &span.Payload{Method: "POST", Status: 200, Response: `{"ok":true}`}

	if groups := report.Observations([]*span.Span{root}); len(groups) != 0 {
		t.Fatalf("a call span is not an observation: %+v", groups)
	}
}

func TestVariantComparisonDiffsTwoVersionsOfOnePrompt(t *testing.T) {
	traces := []*span.Span{observed(
		observation("classify@concise", "concise", "Answer in one word.", "refund", "aaa"),
		observation("classify@verbose", "verbose", "Answer at length.", "refund, because the card was charged twice", "bbb"),
	)}
	groups, selected := report.SelectObservation(report.Observations(traces), "", query())
	if selected == nil {
		t.Fatal("the first observation should be selected by default")
	}
	if !groups[0].Selected {
		t.Error("the selected group should be marked for the list")
	}

	c := report.CompareVariants(selected, "", "", query())
	if c == nil {
		t.Fatal("two variants are comparable")
	}
	if !c.PromptChanged {
		t.Error("different prompts must be flagged: the outputs are not like for like")
	}
	if c.Output.Stat.Identical {
		t.Error("the completions differ")
	}
	if len(c.VariantOpts) != 2 {
		t.Errorf("both variants should be offered as the other side, got %d", len(c.VariantOpts))
	}
}

func TestBaselineComparisonSeparatesAPromptEditFromModelDrift(t *testing.T) {
	cur := report.Observations([]*span.Span{observed(
		observation("classify@concise", "concise", "Answer in one word, lowercase.", "refund", "edited"),
		observation("classify@verbose", "verbose", "Answer at length.", "a refund, because…", "same"),
	)})
	base := report.Observations([]*span.Span{observed(
		observation("classify@concise", "concise", "Answer in one word.", "refund_request", "original"),
		observation("classify@verbose", "verbose", "Answer at length.", "a refund, because…", "same"),
	)})

	edited := report.CompareBaseline(&cur[0], base, "classify@concise", "now", "before")
	if !edited.PromptChanged {
		t.Error("the concise prompt was edited between the runs")
	}
	if edited.Output.Stat.Identical {
		t.Error("its completion moved with it")
	}

	// The other variant is the control: same prompt, same output. This is the
	// acceptance's "unchanged-prompt rerun shows an empty diff".
	steady := report.CompareBaseline(&cur[0], base, "classify@verbose", "now", "before")
	if steady.PromptChanged {
		t.Error("the verbose prompt did not change")
	}
	if !steady.Output.Stat.Identical {
		t.Errorf("an unchanged prompt with an unchanged answer is an empty diff, got %+v", steady.Output.Stat)
	}
}

func TestBaselineMissingTheObservationSaysSoRatherThanDiffingNothing(t *testing.T) {
	cur := report.Observations([]*span.Span{observed(
		observation("classify@terse", "terse", "P", "refund", "h1"),
	)})
	base := report.Observations([]*span.Span{observed(
		observation("classify@concise", "concise", "P", "refund", "h1"),
	)})

	c := report.CompareBaseline(&cur[0], base, "", "now", "before")
	if c.BaselineNote == "" {
		t.Fatal("a renamed variant has no counterpart; the view must say that")
	}
}

func TestUsageDeltasReportTokenMovement(t *testing.T) {
	cheap := observation("classify", "", "P", "short", "h")
	cheap.Payload.Usage = &span.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
	dear := observation("classify", "", "P", "much longer answer", "h")
	dear.Payload.Usage = &span.Usage{PromptTokens: 10, CompletionTokens: 40, TotalTokens: 50}

	cur := report.Observations([]*span.Span{observed(dear)})
	base := report.Observations([]*span.Span{observed(cheap)})
	c := report.CompareBaseline(&cur[0], base, "", "now", "before")

	var found bool
	for _, tile := range c.UsageDeltas {
		if tile.Label == "completion tokens" {
			found = true
			if tile.Value != "▲ +38" || tile.Tone != "failed" {
				t.Errorf("completion tokens tile = %+v", tile)
			}
		}
	}
	if !found {
		t.Error("token usage should be reported beside the diff")
	}
}

func TestPromptTabCountsEveryVariant(t *testing.T) {
	traces := []*span.Span{observed(
		observation("classify@a", "a", "P", "x", "h1"),
		observation("classify@b", "b", "P", "y", "h2"),
	)}
	if n := report.PromptTabCount(traces); n != 2 {
		t.Errorf("tab count = %d, want one per variant", n)
	}
	if n := report.PromptTabCount(nil); n != 0 {
		t.Errorf("a run with no observations has no tab, got %d", n)
	}
}

func TestQueryLinksKeepTheRestOfTheSelection(t *testing.T) {
	q := report.PromptQuery{Base: "/p/x/runs/1/prompts", Obs: "classify", Mode: "inline"}
	got := q.Href("with", "run-2")
	want := "/p/x/runs/1/prompts?mode=inline&obs=classify&with=run-2"
	if got != want {
		t.Errorf("Href = %q, want %q", got, want)
	}
}
