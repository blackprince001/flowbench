package span

import "time"

// Folded is the first storage tier: many trace trees folded together by
// structural span-path, so its size is bounded by the number of distinct paths
// (span names), not by the iteration count. It is the sole input to flame
// graphs. Fold every iteration in, sampled or not.
type Folded struct {
	Root *FoldNode `json:"root"`
}

// FoldNode aggregates every span that shares a path (the chain of names from
// the root). Total is the summed span duration; Self excludes children, the
// value a flame graph attributes to this frame.
type FoldNode struct {
	Name     string               `json:"name"`
	Count    int64                `json:"count"`
	Total    time.Duration        `json:"total"`
	Self     time.Duration        `json:"self"`
	Children map[string]*FoldNode `json:"children,omitempty"`
}

// NewFolded returns an empty accumulator. Its root is a virtual frame whose
// children are the per-flow roots.
func NewFolded() *Folded {
	return &Folded{Root: &FoldNode{}}
}

func (n *FoldNode) child(name string) *FoldNode {
	if n.Children == nil {
		n.Children = make(map[string]*FoldNode)
	}
	c := n.Children[name]
	if c == nil {
		c = &FoldNode{Name: name}
		n.Children[name] = c
	}
	return c
}

// Add folds one trace tree into the aggregate.
func (f *Folded) Add(root *Span) {
	addSpan(f.Root, root)
}

func addSpan(fn *FoldNode, sp *Span) {
	c := fn.child(sp.Name)
	c.Count++
	c.Total += sp.Duration
	c.Self += sp.SelfTime()
	for _, ch := range sp.Children {
		addSpan(c, ch)
	}
}

// Merge folds another accumulator into this one, for combining the per-VU
// partials computed concurrently.
func (f *Folded) Merge(other *Folded) {
	if other == nil {
		return
	}
	mergeNode(f.Root, other.Root)
}

func mergeNode(dst, src *FoldNode) {
	for name, sc := range src.Children {
		dc := dst.child(name)
		dc.Count += sc.Count
		dc.Total += sc.Total
		dc.Self += sc.Self
		mergeNode(dc, sc)
	}
}
