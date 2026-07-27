package report

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/blackprince001/flowbench/internal/span"
)

// The funnel answers "where do flow-runs stop", which is a different question
// from "what did they do" (the flame graph) and "what happened in this one"
// (the waterfall). A step's folded count is how many flow-runs reached it, so
// the narrowing between two stages is exactly the flow-runs that did not get
// past the first — a step whose assertion aborts the flow shows up as a cliff.
//
// Counts come from the folded tier, which sees every iteration. Order does not:
// FoldNode.Children is a map, and a funnel drawn in map order is not a funnel.
// The order comes from a kept trace, whose children are the steps in causal
// order — so the two tiers each supply the half they are honest about.

// The funnel has its own taller viewBox: it is the page's one wide graph, and
// the shape only reads when the widest stage has room to be wide.
const (
	funnelH  = 60.0
	funnelCY = funnelH / 2

	// funnelMaxHalf is the half-height of the widest stage.
	funnelMaxHalf = 22.0

	// funnelFloor is the thinnest a stage's core is drawn. A stage holding
	// 0.3% of the first would otherwise be invisible, and "the last step is
	// missing" is the opposite of what the graph should say. The count and the
	// share are printed on the band, so the number stays exact where the
	// thickness is only nearly so.
	funnelFloor = 0.45

	// The halos are what make a ribbon read as flowing rather than as a filled
	// polygon: two translucent layers a fixed distance outside the core, so a
	// wide stage gets a rim and a hairline stage gets a visible glow around
	// something too thin to see on its own. Additive rather than proportional
	// for exactly that reason.
	funnelHaloIn  = 1.6
	funnelHaloOut = 3.4
)

// funnelTones is a positional ramp, not a semantic one: stage 1 is not
// "better" than stage 3. Status colours are deliberately not reused — they are
// reserved for outcomes (ADR 0006), and a purple step would be read as one.
var funnelTones = []string{"f1", "f2", "f3", "f4", "f5"}

// FunnelStage is one step: how many flow-runs reached it, and where its label
// sits.
type FunnelStage struct {
	Name  string
	Count int64
	Share string // of the first stage
	Drop  string // flow-runs lost since the previous stage, empty on the first
	Glyph string // entry / middle / exit, so position reads without colour
	X     float64
	Edge  float64 // the divider between this stage's column and the next
	Last  bool
}

// FunnelBand is one stage's slice of the ribbon, coloured and clipped to that
// stage's column so the colour changes on the divider. Core is the solid band;
// the two halos sit outside it, faint, and are drawn first.
type FunnelBand struct {
	Tone    string
	Core    string
	HaloIn  string
	HaloOut string
}

// Funnel is one flow's stages.
type Funnel struct {
	Flow   string
	Stages []FunnelStage
	Bands  []FunnelBand
	Note   string
}

// Funnels builds one funnel per flow in the run.
//
// A flow with a single step is skipped: a funnel of one stage is a number, and
// the summary tiles already say it.
func Funnels(f *span.Folded, traces []*span.Span) []Funnel {
	if f == nil || f.Root == nil {
		return nil
	}
	order := stepOrder(traces)

	var out []Funnel
	for name, flow := range f.Root.Children {
		steps := order[name]
		if len(steps) == 0 {
			// No trace was kept for this flow, so nothing knows the step order.
			// Drawing it in map order would invent one.
			continue
		}
		if fn, ok := buildFunnel(name, flow, steps); ok {
			out = append(out, fn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Flow < out[j].Flow })
	return out
}

// stepOrder reads each flow's step sequence off the first trace kept for it.
// Later traces are ignored: they are the same flow, so they agree, and a
// partial trace from an aborted iteration would only ever be shorter.
func stepOrder(traces []*span.Span) map[string][]string {
	order := map[string][]string{}
	for _, tr := range traces {
		if tr == nil || len(tr.Children) == 0 {
			continue
		}
		if len(order[tr.Name]) >= len(tr.Children) {
			continue
		}
		names := make([]string, 0, len(tr.Children))
		for _, c := range tr.Children {
			names = append(names, c.Name)
		}
		order[tr.Name] = names
	}
	return order
}

func buildFunnel(flow string, node *span.FoldNode, steps []string) (Funnel, bool) {
	stages := make([]FunnelStage, 0, len(steps))
	seen := map[string]bool{}
	for _, name := range steps {
		if seen[name] {
			continue // a name repeated in one flow folds to one node
		}
		seen[name] = true
		// A step no flow-run ever reached has no fold node at all, because no
		// span was created for it. It is still a stage, at zero — dropping it
		// would turn the most interesting funnel there is ("nobody got past
		// step one") into one that looks like the flow ends at step one.
		var count int64
		if child, ok := node.Children[name]; ok {
			count = child.Count
		}
		stages = append(stages, FunnelStage{Name: name, Count: count})
	}
	if len(stages) < 2 {
		return Funnel{}, false
	}

	first := stages[0].Count
	if first <= 0 {
		return Funnel{}, false
	}

	colW := ribbonW / float64(len(stages))
	halves := make([]float64, len(stages))
	xs := make([]float64, len(stages))
	for i := range stages {
		xs[i] = colW*float64(i) + colW/2
		stages[i].X = xs[i]
		stages[i].Edge = colW * float64(i+1)
		stages[i].Last = i == len(stages)-1
		stages[i].Share = fmt.Sprintf("%.1f%%", float64(stages[i].Count)/float64(first)*100)
		stages[i].Glyph = "middle"
		if i > 0 {
			if lost := stages[i-1].Count - stages[i].Count; lost > 0 {
				stages[i].Drop = fmt.Sprintf("−%s here", plural(lost, "flow-run"))
			}
		}

		h := float64(stages[i].Count) / float64(first) * funnelMaxHalf
		if stages[i].Count > 0 && h < funnelFloor {
			h = funnelFloor
		}
		halves[i] = h
	}
	stages[0].Glyph = "entry"
	stages[len(stages)-1].Glyph = "exit"

	// One curve pair per layer. Sampling all three on the same x grid is what
	// keeps the halos concentric with the core through every transition.
	edges := func(pad float64) (curve, curve) {
		top := curve{xs: xs, ys: make([]float64, len(stages))}
		bottom := curve{xs: xs, ys: make([]float64, len(stages))}
		for i, h := range halves {
			top.ys[i] = funnelCY - (h + pad)
			bottom.ys[i] = funnelCY + (h + pad)
		}
		return top, bottom
	}
	coreTop, coreBottom := edges(0)
	inTop, inBottom := edges(funnelHaloIn)
	outTop, outBottom := edges(funnelHaloOut)

	bands := make([]FunnelBand, 0, len(stages))
	for i := range stages {
		x0, x1 := colW*float64(i), colW*float64(i+1)
		bands = append(bands, FunnelBand{
			Tone:    funnelTones[i%len(funnelTones)],
			Core:    bandPath(coreTop, coreBottom, x0, x1, samplesPerGap),
			HaloIn:  bandPath(inTop, inBottom, x0, x1, samplesPerGap),
			HaloOut: bandPath(outTop, outBottom, x0, x1, samplesPerGap),
		})
	}

	// A flat funnel is a finding, not an empty state: it says no step ever
	// aborted the flow, which is exactly what a load run should look like
	// (`record` is the mode default, so a failure is data and the flow carries
	// on). Saying that in words beats leaving a reader to infer it from bands
	// that happen to be the same width.
	last := stages[len(stages)-1].Count
	note := fmt.Sprintf("%s reached the first step; %s reached the last — %s stopped on the way",
		plural(first, "flow-run"), plural(last, "flow-run"), humanCount(first-last))
	if last == first {
		note = fmt.Sprintf("no flow-run stopped early — all %s reached every step",
			plural(first, "flow-run"))
	}

	return Funnel{Flow: trimFlowPrefix(flow), Stages: stages, Bands: bands, Note: note}, true
}

func plural(n int64, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return humanCount(n) + " " + unit + "s"
}

// humanCount groups thousands. A funnel's whole job is comparing magnitudes,
// and 218393 next to 7024 is two smudges where 218,393 next to 7,024 is a
// ratio a reader can see.
func humanCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if n < 0 {
		neg, s = "−", s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return neg + b.String()
}

// trimFlowPrefix drops the `flow:` the fold root uses to namespace flows.
func trimFlowPrefix(name string) string {
	if len(name) > 5 && name[:5] == "flow:" {
		return name[5:]
	}
	return name
}
