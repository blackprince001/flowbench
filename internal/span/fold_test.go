package span

import (
	"testing"
	"time"
)

// tree builds a two-level trace: a root of duration d with one child of
// duration childD (nanoseconds).
func tree(d, childD int) *Span {
	root := New("flow:f", 0)
	root.Duration = time.Duration(d)
	c := root.Child("step", 0)
	c.Duration = time.Duration(childD)
	return root
}

func TestFoldAggregates(t *testing.T) {
	f := NewFolded()
	f.Add(tree(100, 60))
	f.Add(tree(200, 80))

	root := f.Root.Children["flow:f"]
	if root == nil || root.Count != 2 || root.Total != 300 {
		t.Fatalf("flow root = %+v, want count 2 total 300", root)
	}
	if root.Self != 160 { // (100-60) + (200-80)
		t.Errorf("flow root self = %d, want 160", root.Self)
	}
	step := root.Children["step"]
	if step == nil || step.Count != 2 || step.Total != 140 || step.Self != 140 {
		t.Errorf("step = %+v, want count 2 total 140 self 140", step)
	}
}

func TestFoldMergeEqualsSequential(t *testing.T) {
	seq := NewFolded()
	seq.Add(tree(100, 60))
	seq.Add(tree(200, 80))

	a, b := NewFolded(), NewFolded()
	a.Add(tree(100, 60))
	b.Add(tree(200, 80))
	a.Merge(b)

	if !foldNodeEqual(seq.Root, a.Root) {
		t.Fatal("merged fold differs from sequential fold")
	}
}

func foldNodeEqual(a, b *FoldNode) bool {
	if a.Count != b.Count || a.Total != b.Total || a.Self != b.Self || len(a.Children) != len(b.Children) {
		return false
	}
	for name, ac := range a.Children {
		bc, ok := b.Children[name]
		if !ok || !foldNodeEqual(ac, bc) {
			return false
		}
	}
	return true
}
