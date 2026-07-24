package report

import (
	"fmt"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/span"
)

// RunCell is one flow-run rendered as a single square in the outcome grid. A
// throttled flow-run is drawn throttled even when its mode also counts it as an
// error, because that is the distinction the grid exists to show (ADR 0006).
type RunCell struct {
	Seq      int
	Tone     string
	Title    string
	Href     string
	Selected bool
}

// RunFilter is one tab above the grid.
type RunFilter struct {
	Key      string
	Label    string
	Count    int
	Href     string
	Selected bool
}

// RunDetail is an inspected flow-run.
type RunDetail struct {
	Seq       int
	Flow      string
	At        string
	Latency   string
	Service   string
	Queued    string
	Outcome   span.Outcome
	Throttled bool
}

// gridCap bounds how many squares one page draws. The stored tier allows more
// than a grid can usefully show, and every square costs markup, so the view
// stops here and says how many it left out — the filter tabs are the way to see
// the rest.
const gridCap = 4000

// Cells turns the per-flow-run tier into grid squares, keeping only those
// matching filter ("" for all). Ordering is the run's own order, so reading the
// grid left to right is reading the run in time. The second return is how many
// matching flow-runs were left undrawn.
func Cells(s collector.Samples, filter string, selected int, base string) ([]RunCell, int) {
	out := make([]RunCell, 0, min(len(s.Runs), gridCap))
	omitted := 0
	for _, r := range s.Runs {
		tone := toneOf(r)
		if filter != "" && tone != filter {
			continue
		}
		if len(out) >= gridCap {
			omitted++
			continue
		}
		out = append(out, RunCell{
			Seq:      r.Seq,
			Tone:     tone,
			Selected: r.Seq == selected,
			Href:     fmt.Sprintf("%s&run=%d", base, r.Seq),
			Title: fmt.Sprintf("#%d at %s — %s, latency %s",
				r.Seq, humanDur(r.At), tone, humanDur(r.Latency)),
		})
	}
	return out, omitted
}

// Filters counts each outcome across the kept flow-runs.
func Filters(s collector.Samples, selected, base string) []RunFilter {
	counts := map[string]int{}
	for _, r := range s.Runs {
		counts[toneOf(r)]++
	}

	out := []RunFilter{{Key: "", Label: "all", Count: len(s.Runs)}}
	for _, k := range []string{"ok", "throttled", "failed", "skipped"} {
		out = append(out, RunFilter{Key: k, Label: k, Count: counts[k]})
	}
	for i := range out {
		out[i].Selected = out[i].Key == selected
		out[i].Href = base
		if out[i].Key != "" {
			out[i].Href = base + "?outcome=" + out[i].Key
		}
	}
	return out
}

// Inspect describes one flow-run by sequence number.
func Inspect(s collector.Samples, seq int) *RunDetail {
	for _, r := range s.Runs {
		if r.Seq != seq {
			continue
		}
		return &RunDetail{
			Seq:     r.Seq,
			Flow:    r.Flow,
			At:      humanDur(r.At),
			Latency: humanDur(r.Latency),
			Service: humanDur(r.Service),
			// Latency counts from when the run was due, so the difference is
			// time the generator made it wait — a stall, not the target's cost.
			Queued:    humanDur(r.Latency - r.Service),
			Outcome:   r.Outcome,
			Throttled: r.Throttled,
		}
	}
	return nil
}

// SampleNote states what the grid is showing, so neither a thinned tier nor a
// capped grid is ever mistaken for the whole run.
func SampleNote(s collector.Samples, omitted int) string {
	note := ""
	switch {
	case s.Total == 0:
		return "no flow-runs recorded"
	case s.Complete():
		note = fmt.Sprintf("all %d flow-runs", s.Total)
	default:
		note = fmt.Sprintf("%d of %d flow-runs — every failure and throttle kept, successes sampled 1 in %d",
			s.Kept, s.Total, s.EveryNth)
	}
	if omitted > 0 {
		note += fmt.Sprintf(" · %d not drawn (filter to see them)", omitted)
	}
	return note
}

func toneOf(r collector.FlowRun) string {
	switch {
	case r.Throttled:
		return "throttled"
	case r.Outcome == span.OutcomeFailed:
		return "failed"
	case r.Outcome == span.OutcomeSkipped:
		return "skipped"
	default:
		return "ok"
	}
}

// BucketDetail is one selected column of the series strip.
type BucketDetail struct {
	At        string
	Window    string
	FlowRuns  int
	OK        int
	Failed    int
	Throttled int
	Skipped   int
	RPS       string
	P50       string
	P95       string
	P99       string
}

// InspectBucket describes the series bucket at index i.
func InspectBucket(s collector.Series, i int) *BucketDetail {
	if i < 0 || i >= len(s.Points) {
		return nil
	}
	p := s.Points[i]
	rps := 0.0
	if s.Bucket > 0 {
		rps = float64(p.FlowRuns) / s.Bucket.Seconds()
	}
	return &BucketDetail{
		At:        humanDur(p.At),
		Window:    humanDur(s.Bucket),
		FlowRuns:  p.FlowRuns,
		OK:        p.OK,
		Failed:    p.Failed,
		Throttled: p.Throttled,
		Skipped:   p.Skipped,
		RPS:       fmt.Sprintf("%.0f/s", rps),
		P50:       humanDur(p.P50),
		P95:       humanDur(p.P95),
		P99:       humanDur(p.P99),
	}
}

// StripLinks adds a selection href to each column of the strip.
func StripLinks(cells []StripCell, base string, selected int) []StripCell {
	for i := range cells {
		cells[i].Href = fmt.Sprintf("%s?at=%d", base, i)
		cells[i].Selected = i == selected
	}
	return cells
}

// PeakThrottle is the offset at which throttling was heaviest, the answer to
// "when did this start" that a rate on its own cannot give.
func PeakThrottle(s collector.Series) (time.Duration, int, bool) {
	best, at, found := 0, time.Duration(0), false
	for _, p := range s.Points {
		if p.Throttled > best {
			best, at, found = p.Throttled, p.At, true
		}
	}
	return at, best, found
}
