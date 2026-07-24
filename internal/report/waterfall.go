package report

import (
	"fmt"
	"sort"
	"time"

	"github.com/blackprince001/flowbench/internal/span"
)

// Row is one span in a trace, laid out in causal order. Left and Width are
// percentages of the iteration's wall time; unlike a flame graph the bars are
// placed by when they happened, not by how much they cost.
//
// Path addresses the row by child index rather than by name, because within one
// trace siblings can repeat a name — two attempts of the same step, two
// identical assertions — and a selection has to mean exactly one of them.
type Row struct {
	Name     string
	Path     string
	Kind     Kind
	Depth    int
	Left     float64
	Width    float64
	Start    time.Duration
	Duration time.Duration
	Self     time.Duration
	Outcome  span.Outcome
	Payload  *span.Payload
	Selected bool
}

// WaterfallRows walks one trace tree depth-first in start order. Retry attempts
// and their backoff waits appear as ordinary nested rows, which is the point:
// every attempt is visible rather than collapsed into the step that retried.
//
// The two tiers do not share a time origin: a flow root is stamped with its
// offset into the run, while the steps beneath it are stamped from the
// iteration's own anchor. Folding never reads Start so it never noticed, but a
// waterfall does — so the root is pinned to the left edge and its descendants
// are placed by their iteration-relative offsets.
func WaterfallRows(root *span.Span) []Row {
	if root == nil {
		return nil
	}
	total := root.Duration
	if total <= 0 {
		total = 1
	}

	out := []Row{{
		Name:     root.Name,
		Path:     "0",
		Kind:     classify(root.Name, 0),
		Width:    100,
		Duration: root.Duration,
		Self:     root.SelfTime(),
		Outcome:  root.Outcome,
		Payload:  root.Payload,
	}}
	for i, c := range byStart(root.Children) {
		walk(&out, c, fmt.Sprintf("0.%d", i), 1, total)
	}
	return out
}

func walk(out *[]Row, s *span.Span, path string, depth int, total time.Duration) {
	left := clamp(percent(s.Start, total))
	*out = append(*out, Row{
		Name:     s.Name,
		Path:     path,
		Kind:     classify(s.Name, depth),
		Depth:    depth,
		Left:     left,
		Width:    clamp(percent(s.Duration, total)+left) - left,
		Start:    s.Start,
		Duration: s.Duration,
		Self:     s.SelfTime(),
		Outcome:  s.Outcome,
		Payload:  s.Payload,
	})
	for i, c := range byStart(s.Children) {
		walk(out, c, fmt.Sprintf("%s.%d", path, i), depth+1, total)
	}
}

// SelectRow marks the span at path as selected and returns it. An unknown path
// selects nothing, so a stale link degrades to the plain waterfall.
func SelectRow(rows []Row, path string) (*Row, []Row) {
	if path == "" {
		return nil, rows
	}
	for i := range rows {
		if rows[i].Path == path {
			rows[i].Selected = true
			sel := rows[i]
			return &sel, rows
		}
	}
	return nil, rows
}

// TraceSummary is one entry in the trace picker. Kept traces are a sample, so
// the picker has to say what each one is before it is worth opening.
type TraceSummary struct {
	Index    int
	Name     string
	Duration time.Duration
	Spans    int
	Outcome  span.Outcome
	Selected bool
}

// Summarize describes each kept trace by its worst outcome, so the failures in
// a sample are findable without opening them one at a time.
func Summarize(traces []*span.Span, selected int) []TraceSummary {
	out := make([]TraceSummary, 0, len(traces))
	for i, t := range traces {
		if t == nil {
			continue
		}
		out = append(out, TraceSummary{
			Index:    i,
			Name:     t.Name,
			Duration: t.Duration,
			Spans:    countSpans(t),
			Outcome:  worstOutcome(t),
			Selected: i == selected,
		})
	}
	return out
}

func countSpans(s *span.Span) int {
	n := 1
	for _, c := range s.Children {
		n += countSpans(c)
	}
	return n
}

// TraceAt returns the trace at index, or the worst one when the index is out of
// range — a link to a trace that is no longer there still lands somewhere real.
func TraceAt(traces []*span.Span, i int) (*span.Span, int) {
	if i >= 0 && i < len(traces) && traces[i] != nil {
		return traces[i], i
	}
	best := PickTrace(traces)
	for j, t := range traces {
		if t == best {
			return best, j
		}
	}
	return best, -1
}

func byStart(spans []*span.Span) []*span.Span {
	kids := append([]*span.Span(nil), spans...)
	sort.SliceStable(kids, func(i, j int) bool { return kids[i].Start < kids[j].Start })
	return kids
}

// clamp keeps a bar inside the track: a span can outlive the root it hangs
// under when a step is still unwinding as the iteration is recorded.
func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// PickTrace chooses the trace worth opening first: a failure if the run had
// one, otherwise a throttle, otherwise the slowest — the order in which a
// reader actually wants to see them.
func PickTrace(traces []*span.Span) *span.Span {
	var failed, throttled, slowest *span.Span
	for _, t := range traces {
		if t == nil {
			continue
		}
		switch worstOutcome(t) {
		case span.OutcomeFailed:
			if failed == nil {
				failed = t
			}
		case span.OutcomeThrottled:
			if throttled == nil {
				throttled = t
			}
		}
		if slowest == nil || t.Duration > slowest.Duration {
			slowest = t
		}
	}
	switch {
	case failed != nil:
		return failed
	case throttled != nil:
		return throttled
	default:
		return slowest
	}
}

// worstOutcome reports the most serious outcome anywhere in the tree. Throttled
// never collapses into failed (ADR 0006), so the two are ranked, not merged.
func worstOutcome(s *span.Span) span.Outcome {
	worst := s.Outcome
	for _, c := range s.Children {
		if rank(worstOutcome(c)) > rank(worst) {
			worst = worstOutcome(c)
		}
	}
	return worst
}

func rank(o span.Outcome) int {
	switch o {
	case span.OutcomeFailed:
		return 3
	case span.OutcomeThrottled:
		return 2
	case span.OutcomeSkipped:
		return 1
	default:
		return 0
	}
}
