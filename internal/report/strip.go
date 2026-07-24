package report

import (
	"fmt"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
)

// StripCell is one time bucket of the outcome strip: a column stacked by
// outcome share. Read left to right it is the run in order, which is the one
// question a rate on its own cannot answer — when the throttling started.
type StripCell struct {
	OK        float64 // segment heights, percent of the column
	Throttled float64
	Failed    float64
	Empty     bool
	Title     string
	Href      string
	Selected  bool
}

// Strip turns a run's series into stacked columns. A bucket where the generator
// issued nothing stays empty rather than being dropped, so the x-axis remains
// linear in time.
func Strip(s collector.Series) []StripCell {
	cells := make([]StripCell, 0, len(s.Points))
	for _, p := range s.Points {
		if p.FlowRuns == 0 {
			cells = append(cells, StripCell{
				Empty: true,
				Title: fmt.Sprintf("%s — no flow-runs", humanDur(p.At)),
			})
			continue
		}
		// Throttled and failed overlap only in the modes where a throttle is
		// also an error; normalizing keeps the column a whole.
		total := float64(p.OK + p.Failed + p.Throttled)
		if total <= 0 {
			total = float64(p.FlowRuns)
		}
		cells = append(cells, StripCell{
			OK:        float64(p.OK) / total * 100,
			Throttled: float64(p.Throttled) / total * 100,
			Failed:    float64(p.Failed) / total * 100,
			Title: fmt.Sprintf("%s — %d flow-runs · %d ok · %d throttled · %d failed · p95 %s",
				humanDur(p.At), p.FlowRuns, p.OK, p.Throttled, p.Failed, humanDur(p.P95)),
		})
	}
	return cells
}

// Tally is one outcome's count in the summary row above the strip. Glyph and
// word carry the meaning; the colour only reinforces it.
type Tally struct {
	Outcome string
	Count   int
	Label   string
	Lead    bool
}

// Tallies totals the series by outcome, marking the largest non-ok group as the
// lead so a run's character reads before any individual number does.
func Tallies(s collector.Series) []Tally {
	var ok, failed, throttled, skipped int
	for _, p := range s.Points {
		ok += p.OK
		failed += p.Failed
		throttled += p.Throttled
		skipped += p.Skipped
	}

	out := []Tally{
		{Outcome: "ok", Count: ok, Label: "successful"},
		{Outcome: "failed", Count: failed, Label: "failed"},
		{Outcome: "throttled", Count: throttled, Label: "throttled"},
		{Outcome: "skipped", Count: skipped, Label: "skipped"},
	}

	lead, best := -1, 0
	for i, t := range out {
		if t.Outcome != "ok" && t.Count > best {
			lead, best = i, t.Count
		}
	}
	if lead >= 0 {
		out[lead].Lead = true
	}
	return out
}

// SeriesSpan is the wall-clock extent the strip covers, for its axis labels.
func SeriesSpan(s collector.Series) time.Duration {
	if len(s.Points) == 0 {
		return 0
	}
	return s.Points[len(s.Points)-1].At + s.Bucket
}
