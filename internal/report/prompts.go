package report

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/blackprince001/flowbench/internal/span"
)

// The prompt view captures completions compared across
// variants within a run, and against the same observation in a baseline run.
//
// Diff-and-assert only. Nothing here scores a completion, and nothing calls a
// model to judge one — the view shows exactly what changed and where, and what
// a difference *means* stays with the engineer reading it.
//
// One rule governs the whole page: **the prompt is part of the comparison.** An
// output diff under a changed prompt is not evidence of drift, it is the change
// the author made, so a differing prompt hash leads rather than being a
// footnote under the completions.

// Observation is one recorded prompt/completion pair, as the Python-driven
// producer wrote it (a `prompt` span's payload). The engine never writes one:
// FlowBench does not make LLM calls.
type Observation struct {
	Path       string // span path: step.observation, what folding keys on
	Step       string
	Name       string // classify@concise
	Base       string // classify — the identity two variants share
	Variant    string
	Prompt     string
	Completion string
	Hash       string
	Usage      *span.Usage
	Outcome    span.Outcome
	Trace      int
}

// ShortHash is the prompt identity at reading length. The full hash is a
// 64-character sha256 nobody compares by eye; twelve characters distinguish
// the prompts in a run while staying a fact rather than a paraphrase.
func (o Observation) ShortHash() string {
	if len(o.Hash) <= 12 {
		return o.Hash
	}
	return o.Hash[:12]
}

// PromptText and CompletionText are the panel forms: JSON re-indented so it
// can be read, anything else exactly as it was recorded.
func (o Observation) PromptText() string     { return PrettyOne(o.Prompt) }
func (o Observation) CompletionText() string { return PrettyOne(o.Completion) }

// ObsVariant is one variant of an observation as this run recorded it, across
// every iteration that reached it.
type ObsVariant struct {
	Name     string
	Variant  string // empty when the author labelled none
	Path     string
	Count    int // iterations that recorded it
	Distinct int // distinct completions among them
	Hashes   int // distinct prompt hashes among them
	Sample   Observation
	Href     string
	Selected bool
}

// ObsGroup is one observation, with the variants recorded under it. The group
// is the unit the page compares within: two variants of `classify` are two
// versions of one question, which is exactly what a diff is for.
type ObsGroup struct {
	Base     string
	Step     string
	Variants []ObsVariant
	Count    int
	Href     string
	Selected bool
}

// Observations collects every prompt observation in the kept traces, grouped by
// identity and then by variant, in the order the flow records them.
//
// A payload carrying a prompt hash is the marker: the engine writes no
// observation payloads, and an author's own call span carries none, so there is
// nothing to disambiguate against.
func Observations(traces []*span.Span) []ObsGroup {
	var flat []Observation
	for i, t := range traces {
		if t == nil {
			continue
		}
		collectObservations(t, "", i, &flat)
	}

	order := make([]string, 0)
	byBase := map[string][]Observation{}
	for _, o := range flat {
		if _, seen := byBase[o.Base]; !seen {
			order = append(order, o.Base)
		}
		byBase[o.Base] = append(byBase[o.Base], o)
	}

	out := make([]ObsGroup, 0, len(order))
	for _, base := range order {
		obs := byBase[base]
		g := ObsGroup{Base: base, Step: obs[0].Step, Count: len(obs)}

		variantOrder := make([]string, 0)
		byVariant := map[string][]Observation{}
		for _, o := range obs {
			if _, seen := byVariant[o.Name]; !seen {
				variantOrder = append(variantOrder, o.Name)
			}
			byVariant[o.Name] = append(byVariant[o.Name], o)
		}
		for _, name := range variantOrder {
			vs := byVariant[name]
			g.Variants = append(g.Variants, ObsVariant{
				Name:     name,
				Variant:  vs[0].Variant,
				Path:     vs[0].Path,
				Count:    len(vs),
				Distinct: distinct(vs, func(o Observation) string { return o.Completion }),
				Hashes:   distinct(vs, func(o Observation) string { return o.Hash }),
				Sample:   vs[0],
			})
		}
		out = append(out, g)
	}
	return out
}

func collectObservations(sp *span.Span, parent string, trace int, out *[]Observation) {
	path := sp.Name
	if parent != "" {
		path = parent + "." + sp.Name
	}
	if p := sp.Payload; p != nil && p.PromptHash != "" {
		name := sp.Name
		base, variant := name, p.Variant
		if variant != "" {
			base = strings.TrimSuffix(name, "@"+variant)
		}
		*out = append(*out, Observation{
			Path: path, Step: parent, Name: name, Base: base, Variant: variant,
			Prompt: p.Prompt, Completion: p.Completion, Hash: p.PromptHash,
			Usage: p.Usage, Outcome: sp.Outcome, Trace: trace,
		})
	}
	for _, c := range sp.Children {
		collectObservations(c, path, trace, out)
	}
}

func distinct[T any](items []T, key func(T) string) int {
	seen := map[string]bool{}
	for _, it := range items {
		seen[key(it)] = true
	}
	return len(seen)
}

// PromptSide is one half of a comparison, named for what it is rather than for
// which column it sits in — "the concise variant" or "the baseline run".
type PromptSide struct {
	Label string
	Sub   string
	Obs   Observation
}

// DiffBlock is one rendered difference: the rows, what they add up to, and
// the layout to draw them in. Mode rides on the block rather than being read
// from the page inside the template, so the diff partial takes one value and
// html/template needs no map-building helper to call it.
type DiffBlock struct {
	Rows []DiffRow
	Stat DiffStat
	Mode string // split | inline
}

// Facts describes the side being inspected in the rail: its identity first,
// since "which prompt produced this" is the question the diff raises.
func (s PromptSide) Facts() []Fact {
	out := []Fact{
		{Label: "prompt hash", Value: s.Obs.ShortHash()},
		{Label: "span path", Value: s.Obs.Path},
		{Label: "outcome", Value: string(s.Obs.Outcome), Tone: string(s.Obs.Outcome)},
	}
	if u := s.Obs.Usage; u != nil {
		out = append(out,
			Fact{Label: "prompt tokens", Value: fmt.Sprint(u.PromptTokens)},
			Fact{Label: "completion tokens", Value: fmt.Sprint(u.CompletionTokens)})
	}
	return out
}

// PromptCompare is the diff itself: two sides, whether the prompt behind them
// changed, and the completions' difference at whichever granularity applies.
type PromptCompare struct {
	Kind  string // variant | baseline
	Left  PromptSide
	Right PromptSide

	PromptChanged bool
	Prompt        DiffBlock
	Output        DiffBlock

	Structural    []JSONChange
	IsJSON        bool
	UsageDeltas   []Tile
	VariantOpts   []PickOption
	BaselineNote  string
	ComparingNote string
}

// SetMode switches both blocks between side-by-side and inline.
func (c *PromptCompare) SetMode(mode string) {
	if c == nil {
		return
	}
	c.Prompt.Mode, c.Output.Mode = mode, mode
}

// PickOption is one selectable side in the variant picker.
type PickOption struct {
	Label    string
	Sub      string
	Href     string
	Selected bool
}

// PromptsPage is the run's observations and the one comparison being read.
type PromptsPage struct {
	Shell
	RunHead

	Groups     []ObsGroup
	Selected   *ObsGroup
	Compare    *PromptCompare
	Mode       string // split | inline
	SplitHref  string
	InlineHref string

	Cur       CompareRef
	Baseline  CompareRef
	Baselines []BaselineOption
	Note      string
}

// PromptQuery is the page's URL state: which observation, which two sides, how
// to render. Selection is a URL here as everywhere else in the report, so a
// diff worth talking about is a link someone can paste into a review.
type PromptQuery struct {
	Obs  string
	A    string
	B    string
	With string
	Mode string
	Base string // the page's own URL
}

// Href builds a link to this page with one field replaced.
func (q PromptQuery) Href(field, value string) string {
	v := url.Values{}
	set := func(k, cur string) {
		if field == k {
			cur = value
		}
		if cur != "" {
			v.Set(k, cur)
		}
	}
	set("obs", q.Obs)
	set("a", q.A)
	set("b", q.B)
	set("with", q.With)
	set("mode", q.Mode)
	if len(v) == 0 {
		return q.Base
	}
	return q.Base + "?" + v.Encode()
}

// SelectObservation resolves ?obs= to a group, defaulting to the first one the
// flow records — the page always has something to show rather than an empty
// state a reader has to click out of.
func SelectObservation(groups []ObsGroup, want string, q PromptQuery) ([]ObsGroup, *ObsGroup) {
	if len(groups) == 0 {
		return groups, nil
	}
	pick := 0
	for i := range groups {
		if groups[i].Base == want {
			pick = i
		}
	}
	for i := range groups {
		groups[i].Href = q.Href("obs", groups[i].Base)
		groups[i].Selected = i == pick
	}
	return groups, &groups[pick]
}

// CompareVariants diffs two variants of one observation inside a single run.
func CompareVariants(g *ObsGroup, a, b string, q PromptQuery) *PromptCompare {
	if g == nil || len(g.Variants) < 2 {
		return nil
	}
	left := pickVariant(g, a, 0)
	right := pickVariant(g, b, 1)
	if left == right {
		right = (left + 1) % len(g.Variants)
	}

	lv, rv := g.Variants[left], g.Variants[right]
	c := newCompare("variant",
		PromptSide{Label: sideLabel(lv), Sub: fmt.Sprintf("%d recorded", lv.Count), Obs: lv.Sample},
		PromptSide{Label: sideLabel(rv), Sub: fmt.Sprintf("%d recorded", rv.Count), Obs: rv.Sample})
	c.ComparingNote = "two variants of one observation, within this run"

	for i, v := range g.Variants {
		c.VariantOpts = append(c.VariantOpts,
			PickOption{Label: sideLabel(v), Sub: fmt.Sprintf("%d recorded", v.Count), Href: q.Href("b", v.Name), Selected: i == right})
	}
	return c
}

// CompareBaseline diffs one observation against the same observation in an
// earlier run. This is the question a prompt edit actually asks: did the output
// move, and was the prompt what moved it.
func CompareBaseline(cur *ObsGroup, baseGroups []ObsGroup, variant string, curWhen, baseWhen string) *PromptCompare {
	if cur == nil || len(cur.Variants) == 0 {
		return nil
	}
	idx := pickVariant(cur, variant, 0)
	mine := cur.Variants[idx]

	var theirs *ObsVariant
	for i := range baseGroups {
		if baseGroups[i].Base != cur.Base {
			continue
		}
		for j := range baseGroups[i].Variants {
			if baseGroups[i].Variants[j].Name == mine.Name {
				theirs = &baseGroups[i].Variants[j]
			}
		}
	}
	if theirs == nil {
		c := newCompare("baseline", PromptSide{}, PromptSide{Label: sideLabel(mine), Obs: mine.Sample})
		c.BaselineNote = fmt.Sprintf("the baseline run recorded no %q — a new observation, or a renamed one, which breaks the comparison the same way a renamed step breaks folding", mine.Name)
		return c
	}

	c := newCompare("baseline",
		PromptSide{Label: "baseline", Sub: baseWhen, Obs: theirs.Sample},
		PromptSide{Label: "this run", Sub: curWhen, Obs: mine.Sample})
	c.ComparingNote = fmt.Sprintf("%s, this run against the baseline", sideLabel(mine))
	return c
}

func newCompare(kind string, left, right PromptSide) *PromptCompare {
	c := &PromptCompare{Kind: kind, Left: left, Right: right}
	c.PromptChanged = left.Obs.Hash != right.Obs.Hash
	if c.PromptChanged {
		lp, rp := Pretty(left.Obs.Prompt, right.Obs.Prompt)
		c.Prompt.Rows, c.Prompt.Stat = Diff(lp, rp)
	}
	lc, rc := Pretty(left.Obs.Completion, right.Obs.Completion)
	c.Output.Rows, c.Output.Stat = Diff(lc, rc)
	c.Structural, c.IsJSON = JSONDiff(left.Obs.Completion, right.Obs.Completion)
	c.UsageDeltas = usageDeltas(left.Obs.Usage, right.Obs.Usage)
	return c
}

func pickVariant(g *ObsGroup, want string, fallback int) int {
	for i, v := range g.Variants {
		if v.Name == want || (want != "" && v.Variant == want) {
			return i
		}
	}
	if fallback < len(g.Variants) {
		return fallback
	}
	return 0
}

func sideLabel(v ObsVariant) string {
	if v.Variant == "" {
		return v.Name
	}
	return v.Variant
}

// usageDeltas reports the token counts the provider charged for, right against
// left. Counts, not rates: a completion that grew by 40 tokens says so, and a
// percentage of a two-run sample would be arithmetic dressed as a trend.
func usageDeltas(base, cur *span.Usage) []Tile {
	if base == nil && cur == nil {
		return nil
	}
	if base == nil || cur == nil {
		// One side reported usage and the other did not — a real difference in
		// what was recorded, not a zero.
		return []Tile{{Label: "tokens", Value: "not comparable", Sub: "only one side recorded usage"}}
	}
	return []Tile{
		countDelta("prompt tokens", cur.PromptTokens, base.PromptTokens),
		countDelta("completion tokens", cur.CompletionTokens, base.CompletionTokens),
		countDelta("total tokens", cur.TotalTokens, base.TotalTokens),
	}
}

// countDelta tones a rising token count as a failure only in the sense the rest
// of the compare page uses the word: more is the direction worth noticing,
// because it is what a bill and a latency budget both feel.
func countDelta(label string, cur, base int) Tile {
	t := Tile{Label: label}
	switch d := cur - base; {
	case d > 0:
		t.Tone = "failed"
		t.Value = fmt.Sprintf("▲ +%d", d)
		t.Sub = fmt.Sprintf("more — %d → %d", base, cur)
	case d < 0:
		t.Tone = "ok"
		t.Value = fmt.Sprintf("▼ −%d", -d)
		t.Sub = fmt.Sprintf("fewer — %d → %d", base, cur)
	default:
		t.Value = "no change"
		t.Sub = fmt.Sprint(cur)
	}
	return t
}

// PromptTabCount is how many observations a run recorded, for the tab strip: a
// run with none does not show the tab at all, since prompt observation is
// Python-surface-only and most runs will never have one.
func PromptTabCount(traces []*span.Span) int {
	n := 0
	for _, g := range Observations(traces) {
		n += len(g.Variants)
	}
	return n
}

// SortGroupsForDisplay keeps flow order but puts multi-variant observations
// first — those are the ones with a comparison to make inside this run.
func SortGroupsForDisplay(groups []ObsGroup) []ObsGroup {
	sort.SliceStable(groups, func(i, j int) bool {
		return len(groups[i].Variants) > 1 && len(groups[j].Variants) <= 1
	})
	return groups
}
