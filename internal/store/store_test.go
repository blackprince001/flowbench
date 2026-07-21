package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
	"github.com/blackprince001/flowbench/internal/store"
)

func syntheticResult() *executor.Result {
	folded := span.NewFolded()
	root := span.New("flow:f", 0)
	root.Duration = 50 * time.Millisecond
	folded.Add(root)

	return &executor.Result{
		Duration:   2 * time.Second,
		Iterations: 3,
		Outcomes:   map[span.Outcome]int{span.OutcomeOK: 3},
		Samples: []executor.Sample{
			{Service: 10 * time.Millisecond, Outcome: span.OutcomeOK},
			{Service: 20 * time.Millisecond, Outcome: span.OutcomeOK},
			{Service: 30 * time.Millisecond, Outcome: span.OutcomeOK},
		},
		Traces: []*span.Span{root},
		Folded: folded,
	}
}

func TestSaveCarriesAttributionAndTiers(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	info := store.RunInfo{
		Scenario:  "checkout.flow.yaml",
		Mode:      "load",
		Initiator: "ada",
		Target:    "dev",
		Commit:    "abc123def",
		Dirty:     true,
		StartedAt: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
	}
	dir, err := st.Save(info, syntheticResult())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, f := range []string{"meta.json", "folded.json", "traces.json", "metrics.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("run dir missing %s: %v", f, err)
		}
	}

	m, err := st.Load(filepath.Base(dir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Initiator != "ada" || m.Target != "dev" || m.Commit != "abc123def" || !m.Dirty {
		t.Errorf("attribution not persisted: %+v", m)
	}
	if m.Mode != "load" || m.Scenario != "checkout.flow.yaml" {
		t.Errorf("run identity not persisted: %+v", m)
	}
	if m.FlowRuns != 3 || m.ErrorRate != 0 {
		t.Errorf("summary wrong: flow_runs=%d error_rate=%.2f", m.FlowRuns, m.ErrorRate)
	}

	list, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != filepath.Base(dir) {
		t.Fatalf("index should hold the one run, got %+v", list)
	}
}
