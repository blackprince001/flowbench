package report

import (
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/span"
)

// Frame is one laid-out flame-graph rectangle. Left and Width are percentages
// of the whole run's folded time, so the view scales to any container width
// without re-layout — the reason geometry is percent rather than pixels.
//
// Path is the frame's structural identity: the dot-joined chain of names from
// the flow root, which is what folding keys on and therefore unique among
// siblings. It selects a frame in a URL, so an inspected frame is a link
// someone can send.
type Frame struct {
	Name     string
	Path     string
	Kind     Kind
	Depth    int
	Left     float64
	Width    float64
	Total    time.Duration
	Self     time.Duration
	Count    int64
	Selected bool

	// Href selects this frame, keeping the current zoom; ZoomHref re-roots the
	// graph here. Both are built in Go because html/template refuses to
	// interpolate into a URL assembled conditionally in markup.
	Href     string
	ZoomHref string
}

// FlameFrames lays out a folded aggregate depth-first, widest sibling first, so
// the dominant path reads down the left edge. The root is virtual: its children
// are the per-flow roots, laid out side by side.
func FlameFrames(f *span.Folded) []Frame {
	return FlameFramesAt(f, "")
}

// FlameFramesAt lays out the graph re-rooted at zoom, the span path to focus on.
// Zooming is re-rooting rather than scaling: the focused frame becomes the full
// width and its children divide that, which is what makes a 0.02% frame
// readable at all. An unknown path falls back to the whole graph.
func FlameFramesAt(f *span.Folded, zoom string) []Frame {
	if f == nil || f.Root == nil {
		return nil
	}

	if zoom == "" {
		var total time.Duration
		for _, c := range f.Root.Children {
			total += c.Total
		}
		if total <= 0 {
			return nil
		}
		var out []Frame
		layout(&out, f.Root, "", 0, 0, 0, 100, total)
		return out
	}

	node, ok := descend(f.Root, splitPath(zoom))
	if !ok || node.Total <= 0 {
		return FlameFramesAt(f, "") // a stale zoom link shows the whole graph
	}

	// Display depth restarts at 0 so the focused frame draws on the first row,
	// while classification still sees the frame's real depth in the tree.
	real := depthOf(zoom)
	out := []Frame{{
		Name: node.Name, Path: zoom, Kind: classify(node.Name, real),
		Left: 0, Width: 100, Total: node.Total, Self: node.Self, Count: node.Count,
	}}
	layout(&out, node, zoom, 1, real+1, 0, 100, node.Total)
	return out
}

// descend walks the fold tree by name path.
func descend(n *span.FoldNode, parts []string) (*span.FoldNode, bool) {
	for _, p := range parts {
		c, ok := n.Children[p]
		if !ok || c == nil {
			return nil, false
		}
		n = c
	}
	return n, true
}

func depthOf(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, ".")
}

// ZoomTrail is the chain of ancestors above the zoomed frame, each a link to
// zoom back out to, so a zoom is always reversible one level at a time.
func ZoomTrail(zoom, base string) []Frame {
	parts := splitPath(zoom)
	out := make([]Frame, 0, len(parts))
	for i := range parts {
		path := strings.Join(parts[:i+1], ".")
		out = append(out, Frame{
			Name:     parts[i],
			Path:     path,
			ZoomHref: zoomLink(base, path),
		})
	}
	return out
}

// LinkFrames gives every frame its two URLs. Selecting keeps the zoom, so
// inspecting a frame never snaps the graph back out to the whole run.
func LinkFrames(frames []Frame, base, zoom string) []Frame {
	for i := range frames {
		q := url.Values{}
		if zoom != "" {
			q.Set("zoom", zoom)
		}
		q.Set("frame", frames[i].Path)
		frames[i].Href = base + "?" + q.Encode()
		frames[i].ZoomHref = zoomLink(base, frames[i].Path)
	}
	return frames
}

// zoomLink re-roots at path and selects it, so zooming lands with the frame
// already described rather than needing a second click.
func zoomLink(base, path string) string {
	q := url.Values{}
	q.Set("zoom", path)
	q.Set("frame", path)
	return base + "?" + q.Encode()
}

// layout places n's children left to right inside the parent's [left, right]
// extent. A parent wider than its children is normal — the gap is its self
// time — so the row is deliberately left unfilled rather than stretched.
//
// depth is the row a frame draws on; real is its depth in the tree, which is
// what kind classification reads. The two differ only under zoom.
func layout(out *[]Frame, n *span.FoldNode, parent string, depth, real int, left, right float64, total time.Duration) {
	for _, c := range sortedChildren(n) {
		w := percent(c.Total, total)
		if left+w > right {
			w = right - left
		}
		if w <= 0 {
			continue
		}
		path := c.Name
		if parent != "" {
			path = parent + "." + c.Name
		}
		*out = append(*out, Frame{
			Name:  c.Name,
			Path:  path,
			Kind:  classify(c.Name, real),
			Depth: depth,
			Left:  left,
			Width: w,
			Total: c.Total,
			Self:  c.Self,
			Count: c.Count,
		})
		layout(out, c, path, depth+1, real+1, left, left+w, total)
		left += w
	}
}

// sortedChildren orders by time descending, breaking ties by name so a run
// renders identically every time it is opened.
func sortedChildren(n *span.FoldNode) []*span.FoldNode {
	kids := make([]*span.FoldNode, 0, len(n.Children))
	for _, c := range n.Children {
		kids = append(kids, c)
	}
	sort.Slice(kids, func(i, j int) bool {
		if kids[i].Total != kids[j].Total {
			return kids[i].Total > kids[j].Total
		}
		return kids[i].Name < kids[j].Name
	})
	return kids
}

// FrameDetail is what an inspected frame reveals beyond its bar: the cost
// split between the frame and its children, and where that cost came from.
type FrameDetail struct {
	Frame
	PerCall   time.Duration
	SelfShare float64 // self time as a percent of the frame's own total
	Ancestors []string
	Children  []FrameChild
}

// FrameChild is one direct child in the breakdown, measured against its parent
// rather than the run, which is the comparison the panel is asking for.
type FrameChild struct {
	Name     string
	Path     string
	Kind     Kind
	Total    time.Duration
	Count    int64
	Share    float64
	ZoomHref string
}

// SelectFrame marks the frame at path as selected and describes it. An unknown
// or empty path selects nothing, so a stale link degrades to the plain view.
func SelectFrame(frames []Frame, path string) (*FrameDetail, []Frame) {
	if path == "" {
		return nil, frames
	}
	idx := -1
	for i := range frames {
		if frames[i].Path == path {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, frames
	}
	frames[idx].Selected = true
	f := frames[idx]

	d := &FrameDetail{Frame: f}
	if f.Count > 0 {
		d.PerCall = f.Total / time.Duration(f.Count)
	}
	if f.Total > 0 {
		d.SelfShare = float64(f.Self) / float64(f.Total) * 100
	}
	if parts := splitPath(f.Path); len(parts) > 1 {
		d.Ancestors = parts[:len(parts)-1]
	}

	// Direct children are the frames one level deeper whose path extends this
	// one; the flame layout already has them in widest-first order.
	prefix := f.Path + "."
	for _, c := range frames {
		if c.Depth != f.Depth+1 || !strings.HasPrefix(c.Path, prefix) {
			continue
		}
		if strings.Contains(c.Path[len(prefix):], ".") {
			continue
		}
		share := 0.0
		if f.Total > 0 {
			share = float64(c.Total) / float64(f.Total) * 100
		}
		d.Children = append(d.Children, FrameChild{
			Name: c.Name, Path: c.Path, Kind: c.Kind,
			Total: c.Total, Count: c.Count, Share: share,
			ZoomHref: c.ZoomHref,
		})
	}
	return d, frames
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, ".")
}

// FlameDepth is the number of rows the frames occupy.
func FlameDepth(frames []Frame) int {
	d := 0
	for _, f := range frames {
		if f.Depth+1 > d {
			d = f.Depth + 1
		}
	}
	return d
}

func percent(d, total time.Duration) float64 {
	if total <= 0 {
		return 0
	}
	return float64(d) / float64(total) * 100
}
