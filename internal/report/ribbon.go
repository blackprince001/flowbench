package report

import (
	"fmt"
	"strings"
)

// A ribbon is a band whose thickness varies across the x axis: the funnel's
// narrowing flow and the streamgraph's stacked outcomes are both one, drawn the
// same way and for the same reason — a shape that changes smoothly reads as one
// continuous thing, where a row of separate columns reads as a row of separate
// things.
//
// Both are server-rendered SVG in a fixed viewBox, like the flame graph and the
// line charts (ADR 0014): geometry in user space, the browser reflows on
// resize, no client rendering step and no JS.

// ribbonW/ribbonH are the shared user-space extent. y grows downward.
const (
	ribbonW = 100.0
	ribbonH = 44.0
)

// samplesPerGap is how many points a curve between two anchors is sampled at.
// The path is a polyline rather than a bezier because the curve is defined by a
// function (smoothstep) rather than by control points — sampling it is exact
// where fitting control points to it would only be close.
const samplesPerGap = 14

// smoothstep is the ease used between anchors: flat where it meets each one, so
// consecutive segments join without a visible crease, and steepest in between.
func smoothstep(t float64) float64 {
	switch {
	case t <= 0:
		return 0
	case t >= 1:
		return 1
	}
	return t * t * (3 - 2*t)
}

// curve is a piecewise-smooth function through anchors: y is held flat outside
// the first and last anchor, and eased between them. It is what makes a stage's
// label sit on a level stretch of ribbon rather than on a slope.
type curve struct {
	xs, ys []float64
}

func (c curve) at(x float64) float64 {
	if len(c.xs) == 0 {
		return 0
	}
	if x <= c.xs[0] {
		return c.ys[0]
	}
	last := len(c.xs) - 1
	if x >= c.xs[last] {
		return c.ys[last]
	}
	for i := 0; i < last; i++ {
		if x <= c.xs[i+1] {
			span := c.xs[i+1] - c.xs[i]
			if span <= 0 {
				return c.ys[i+1]
			}
			t := smoothstep((x - c.xs[i]) / span)
			return c.ys[i] + (c.ys[i+1]-c.ys[i])*t
		}
	}
	return c.ys[last]
}

// bandPath closes the area between two curves over [x0, x1] into an SVG path.
// Sampling both edges on the same x grid is what keeps a stacked band's bottom
// exactly flush with the band below it — computing them independently would
// leave hairline gaps wherever the curve is steep.
func bandPath(top, bottom curve, x0, x1 float64, samples int) string {
	if samples < 2 {
		samples = 2
	}
	var b strings.Builder
	step := (x1 - x0) / float64(samples-1)

	for i := 0; i < samples; i++ {
		x := x0 + step*float64(i)
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&b, "%s%.2f %.2f", cmd, x, top.at(x))
	}
	for i := samples - 1; i >= 0; i-- {
		x := x0 + step*float64(i)
		fmt.Fprintf(&b, "L%.2f %.2f", x, bottom.at(x))
	}
	b.WriteString("Z")
	return b.String()
}
