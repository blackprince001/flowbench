package report

import "fmt"

// The streamgraph is the outcome strip's successor: the same buckets, the same
// shares, the same links, drawn as three continuous bands instead of a row of
// separate columns.
//
// The reason is not decoration. A run is one continuous thing, and the question
// the strip exists to answer — *when* did the throttling start — is a question
// about a shape. Separate columns make a reader compare heights one pair at a
// time; a band makes the moment it swells the most legible thing on the page.
//
// What does not change: every bucket is still a link to its own detail, still
// carries its numbers in a tooltip, and the tallies row above still states each
// outcome in words. Colour is never the only carrier (the palette's own rule —
// red and green are indistinguishable under deuteranopia).

// streamMinLabel is the share a band needs before its inline label is drawn.
// Below it the pill would overflow its own band and point at the wrong colour,
// and the tallies row above already carries the number.
const streamMinLabel = 0.14

// StreamBand is one outcome's ribbon across the run.
type StreamBand struct {
	Tone  string
	Label string // "ok" / "throttled" / "failed"
	Path  string
}

// StreamMark is a band's inline label, placed where that band is thickest —
// which is where a reader is already looking.
type StreamMark struct {
	Tone  string
	Text  string
	X, Y  float64
	Above bool // draw the pill above the anchor rather than centred on it
}

// StreamCell is one bucket's hit area: the strip's link, tooltip and selection,
// carried over unchanged so the graph swap costs no interaction.
type StreamCell struct {
	X, W     float64
	Title    string
	Href     string
	Selected bool
}

// Stream is the whole graph.
type Stream struct {
	Bands  []StreamBand
	Marks  []StreamMark
	Cells  []StreamCell
	Height float64
	Width  float64
	Empty  bool
}

// StreamOf lays out the graph from the strip's own cells. It deliberately takes
// no second copy of the series: the cells already hold each bucket's shares,
// its tooltip and its link, so there is one place where a bucket's numbers are
// worked out and the graph cannot disagree with what it links to.
func StreamOf(cells []StripCell) Stream {
	out := Stream{Width: ribbonW, Height: ribbonH}
	if len(cells) == 0 {
		out.Empty = true
		return out
	}

	// One anchor per bucket, at the middle of its column: a bucket is a window,
	// not an instant, so its value belongs at the centre of the width it covers.
	n := len(cells)
	colW := ribbonW / float64(n)
	xs := make([]float64, n)
	okY := make([]float64, n)   // cumulative share, top of the ok band downward
	thrY := make([]float64, n)  // ok + throttled
	failY := make([]float64, n) // ok + throttled + failed
	var okPeak, thrPeak, failPeak float64
	var okAt, thrAt, failAt int

	for i, c := range cells {
		xs[i] = colW*float64(i) + colW/2
		if c.Empty {
			// A bucket the generator issued nothing in stays on the axis rather
			// than being dropped, so x remains linear in time.
			continue
		}
		ok, thr, fail := c.OK/100, c.Throttled/100, c.Failed/100

		okY[i] = ok * ribbonH
		thrY[i] = (ok + thr) * ribbonH
		failY[i] = (ok + thr + fail) * ribbonH

		if ok > okPeak {
			okPeak, okAt = ok, i
		}
		if thr > thrPeak {
			thrPeak, thrAt = thr, i
		}
		if fail > failPeak {
			failPeak, failAt = fail, i
		}
	}

	base := curve{xs: xs, ys: make([]float64, n)} // the top edge, y=0
	okC := curve{xs: xs, ys: okY}
	thrC := curve{xs: xs, ys: thrY}
	failC := curve{xs: xs, ys: failY}

	samples := max(n*samplesPerGap, 2)
	add := func(tone, label string, top, bottom curve, peak float64, at int) {
		if peak <= 0 {
			return
		}
		out.Bands = append(out.Bands, StreamBand{
			Tone: tone, Label: label,
			Path: bandPath(top, bottom, 0, ribbonW, samples),
		})
		if peak < streamMinLabel {
			return
		}
		mid := (top.at(xs[at]) + bottom.at(xs[at])) / 2
		out.Marks = append(out.Marks, StreamMark{
			Tone: tone,
			Text: fmt.Sprintf("%s %.1f%%", label, peak*100),
			X:    xs[at], Y: mid,
		})
	}

	// Stacked ok → throttled → failed, so the run's healthy mass sits on top and
	// anything else grows visibly out of the bottom of it.
	add("ok", "ok", base, okC, okPeak, okAt)
	add("throttled", "throttled", okC, thrC, thrPeak, thrAt)
	add("failed", "failed", thrC, failC, failPeak, failAt)

	if len(out.Bands) == 0 {
		out.Empty = true
		return out
	}

	out.Cells = make([]StreamCell, 0, n)
	for i, c := range cells {
		out.Cells = append(out.Cells, StreamCell{
			X: colW * float64(i), W: colW,
			Title: c.Title, Href: c.Href, Selected: c.Selected,
		})
	}
	return out
}
