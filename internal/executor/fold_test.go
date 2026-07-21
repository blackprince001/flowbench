package executor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// TestFoldedEqualsRawSums is the issue #18 acceptance: after a multi-VU run,
// the incrementally-folded aggregate equals a fresh fold of the retained raw
// traces. With full retention every iteration is in both tiers, so the folded
// totals must equal the raw-span sums.
func TestFoldedEqualsRawSums(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "hit", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/"},
	}}}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule:    holdSchedule(ir.ModeLoad, 16, 250*time.Millisecond),
		Flows:       []ir.Flow{flow},
		BaseURL:     srv.URL,
		Metrics:     -1,
		SampleEvery: 1,         // keep every success...
		MaxTraces:   1_000_000, // ...so the sample is the whole run
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Iterations < 16 || len(res.Traces) != res.Iterations {
		t.Fatalf("want every trace retained: %d iterations, %d traces", res.Iterations, len(res.Traces))
	}

	// Re-fold the raw traces from scratch; it must match the streamed fold.
	refold := span.NewFolded()
	for _, tr := range res.Traces {
		refold.Add(tr)
	}
	if !foldEqual(res.Folded.Root, refold.Root) {
		t.Fatal("folded aggregate does not equal a fresh fold of the raw traces")
	}

	// Numeric invariant: the folded flow root's Total equals the sum of raw
	// root-span durations.
	var rawTotal time.Duration
	for _, tr := range res.Traces {
		rawTotal += tr.Duration
	}
	if got := res.Folded.Root.Children["flow:hit"].Total; got != rawTotal {
		t.Fatalf("folded flow total = %s, raw sum = %s", got, rawTotal)
	}
}

func foldEqual(a, b *span.FoldNode) bool {
	if a.Count != b.Count || a.Total != b.Total || a.Self != b.Self || len(a.Children) != len(b.Children) {
		return false
	}
	for name, ac := range a.Children {
		bc, ok := b.Children[name]
		if !ok || !foldEqual(ac, bc) {
			return false
		}
	}
	return true
}
