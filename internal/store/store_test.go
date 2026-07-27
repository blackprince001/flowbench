package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"context"
	"net/http"
	"net/http/httptest"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
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
	dir, err := st.Save(info, syntheticResult(), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, f := range []string{"meta.json", "folded.json", "traces.json", "series.json", "metrics.json"} {
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

// AgentAttached must be derived from the saved series itself (issue #32),
// never a caller-supplied flag, so the two can never disagree — and a run
// with no agent must round-trip to an unattached, empty series rather than
// an error.
func TestAgentSeriesRoundTripsAndDrivesAgentAttached(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	series := []agent.PolledSample{
		{At: 0, Sample: agent.Sample{CPUSeconds: 1.5, MemUsedBytes: 100}},
		{At: time.Second, Sample: agent.Sample{CPUSeconds: 1.8, MemUsedBytes: 120}},
	}
	dir, err := st.Save(store.RunInfo{Scenario: "s.yaml", Mode: "stress", StartedAt: time.Now()}, syntheticResult(), nil, series)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := filepath.Base(dir)

	m, err := st.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.AgentAttached {
		t.Error("a run saved with a non-empty agent series should report AgentAttached")
	}

	got, err := st.LoadAgentSeries(id)
	if err != nil {
		t.Fatalf("LoadAgentSeries: %v", err)
	}
	if len(got) != 2 || got[1].CPUSeconds != 1.8 {
		t.Errorf("agent series did not round-trip: %+v", got)
	}

	// No agent attached: still saves and loads cleanly, just unattached.
	dir2, err := st.Save(store.RunInfo{Scenario: "s.yaml", Mode: "stress", StartedAt: time.Now()}, syntheticResult(), nil, nil)
	if err != nil {
		t.Fatalf("Save without an agent: %v", err)
	}
	m2, err := st.Load(filepath.Base(dir2))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m2.AgentAttached {
		t.Error("a run saved without an agent series should not report AgentAttached")
	}
}

// The results server reads each tier back on its own, so every artifact Save
// writes must round-trip without the others.
func TestTiersRoundTripIndependently(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dir, err := st.Save(store.RunInfo{Scenario: "s.yaml", Mode: "load", StartedAt: time.Now()}, syntheticResult(), nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	id := filepath.Base(dir)

	folded, err := st.LoadFolded(id)
	if err != nil {
		t.Fatalf("LoadFolded: %v", err)
	}
	if folded.Root == nil || folded.Root.Children["flow:f"] == nil {
		t.Errorf("folded tier lost its span paths: %+v", folded)
	}

	traces, err := st.LoadTraces(id)
	if err != nil {
		t.Fatalf("LoadTraces: %v", err)
	}
	if len(traces) != 1 || traces[0].Name != "flow:f" {
		t.Errorf("trace tier did not round-trip: %+v", traces)
	}

	series, err := st.LoadSeries(id)
	if err != nil {
		t.Fatalf("LoadSeries: %v", err)
	}
	runs := 0
	for _, p := range series.Points {
		runs += p.FlowRuns
	}
	if runs != 3 {
		t.Errorf("series tier should hold all 3 flow-runs, got %d in %d points", runs, len(series.Points))
	}

	if _, err := st.LoadMetrics(id); err != nil {
		t.Errorf("LoadMetrics: %v", err)
	}
}

// TestSavePartialRunAfterCancel is the issue #20 acceptance essence: a run
// cancelled mid-flight (as Ctrl-C does) yields a partial Result that still
// saves to a readable run store.
func TestSavePartialRunAfterCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sched := &planner.Schedule{
		Mode:    ir.ModeLoad,
		Arrival: planner.Closed,
		Segments: []planner.Segment{
			{Kind: planner.Hold, StartVUs: 8, EndVUs: 8, Duration: ir.Duration(5 * time.Second)},
		},
		Stop:     planner.StopDuration,
		PeakVUs:  8,
		Duration: ir.Duration(5 * time.Second),
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(80*time.Millisecond, cancel)

	flow := ir.Flow{Name: "hit", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/"},
	}}}
	res, err := executor.Run(ctx, executor.Options{Schedule: sched, Flows: []ir.Flow{flow}, BaseURL: srv.URL, Metrics: -1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Samples) == 0 {
		t.Fatal("cancelled run produced no partial data")
	}
	if res.Duration > 2*time.Second {
		t.Fatalf("run did not stop early on cancel: ran %s of a 5s schedule", res.Duration)
	}

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := st.Save(store.RunInfo{Scenario: "s.yaml", Mode: "load", StartedAt: time.Date(2026, 7, 21, 13, 0, 0, 0, time.UTC)}, res, nil, nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	m, err := st.Load(filepath.Base(dir))
	if err != nil {
		t.Fatalf("partial run store not readable: %v", err)
	}
	if m.FlowRuns != len(res.Samples) {
		t.Errorf("partial run metadata wrong: flow_runs=%d, samples=%d", m.FlowRuns, len(res.Samples))
	}
}
