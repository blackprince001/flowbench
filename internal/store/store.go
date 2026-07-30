package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

// Store is a run store rooted at a directory the user owns: one subdirectory
// per run plus an index.json. There is no retention machinery (ADR 0007).
type Store struct {
	root string
}

// Open ensures the store directory exists.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create run store %s: %w", root, err)
	}
	return &Store{root: root}, nil
}

func (s *Store) Root() string { return s.root }

// RunInfo is the attribution and identity a run is saved with — enough to
// answer who ran what against which target, at which revision.
type RunInfo struct {
	Scenario  string
	Mode      string
	Initiator string
	Target    string
	Commit    string
	Dirty     bool
	StartedAt time.Time
}

// Meta is a run's persisted summary: attribution plus the aggregate numbers,
// written to meta.json alongside the folded and raw span tiers.
type Meta struct {
	ID           string        `json:"id"`
	Scenario     string        `json:"scenario"`
	Mode         string        `json:"mode"`
	Initiator    string        `json:"initiator"`
	Target       string        `json:"target"`
	Commit       string        `json:"commit,omitempty"`
	Dirty        bool          `json:"dirty,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
	Duration     time.Duration `json:"duration"`
	Iterations   int           `json:"iterations"`
	FlowRuns     int           `json:"flow_runs"`
	ErrorRate    float64       `json:"error_rate"`
	ThrottleRate float64       `json:"throttle_rate"`
	P50          time.Duration `json:"p50"`
	P95          time.Duration `json:"p95"`
	P99          time.Duration `json:"p99"`
	Aborted      bool          `json:"aborted,omitempty"`

	// Thresholds is the run's own verdict: the evaluated gates the scenario
	// declared. Without it a reader can only guess from the rates, and would
	// disagree with the exit code — a 0.2% error rate under a 1% gate is a pass,
	// not a failure.
	Thresholds []collector.Outcome `json:"thresholds,omitempty"`
	Breached   bool                `json:"breached,omitempty"`

	// Knee is the stress knee's classification (issue #39): when a stress
	// run's thresholds broke, whether they broke because the target was
	// genuinely saturating or because it enforced a rate limit, correlated
	// against the agent series saved beside it. Nil for every other mode,
	// for stress runs that held, and for runs written before it existed —
	// a reader treats absence as "not classified", never as "held".
	Knee *collector.Knee `json:"knee,omitempty"`

	// Identities are the structural span names this run recorded that an
	// author chose — its steps, and the prompt observations its code opened,
	// as the dot-paths folding keys them by (`classify.classify@concise`).
	// Kept so the next run of the same scenario can tell a rename from a
	// disappearance: folding is by name (ADR 0007), so a renamed variant
	// silently splits a flame graph into a before and an after.
	//
	// Written by the Python-driven producer, which is the only one that can
	// discover them — an observation is declared nowhere, it exists because a
	// step's body opened one. The engine's own step ids are declared, so the
	// YAML side warns from the parser instead (internal/parser's
	// Options.PriorStepIDs, still unwired to the store).
	Identities []string `json:"identities,omitempty"`

	// AgentAttached reports whether a target-metrics agent (issue #32)
	// contributed at least one sample to this run — derived from the saved
	// agent series itself, never a caller-supplied flag, so the two can
	// never disagree.
	AgentAttached bool `json:"agent_attached,omitempty"`
}

// Save writes a run's span tiers, self-metrics, an optional target-metrics
// agent series, evaluated thresholds, and attribution to a fresh directory
// and appends it to the index. It returns the run directory. agentSeries may
// be nil or empty — no agent was attached, or every poll against one failed
// (fail-open, issue #32).
func (s *Store) Save(info RunInfo, res *executor.Result, outcomes []collector.Outcome, agentSeries []agent.PolledSample) (string, error) {
	id := runID(info.StartedAt)
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create run dir: %w", err)
	}

	meta := metaFrom(id, info, res, outcomes, agentSeries)
	writes := []struct {
		name string
		v    any
	}{
		{"meta.json", meta},
		{"folded.json", res.Folded},                   // tier 1: flame-graph aggregate
		{"traces.json", res.Traces},                   // tier 2: sampled raw trees
		{"series.json", collector.BuildSeries(res)},   // tier 3: bucketed over time
		{"samples.json", collector.BuildSamples(res)}, // tier 4: per flow-run, thinned
		{"metrics.json", res.Metrics},                 // the generator's own resource use
		{"agent.json", agentSeries},                   // the target's resource use, if an agent was attached
	}
	for _, w := range writes {
		if err := writeJSON(filepath.Join(dir, w.name), w.v); err != nil {
			return "", fmt.Errorf("write %s: %w", w.name, err)
		}
	}

	if err := s.appendIndex(meta); err != nil {
		return "", fmt.Errorf("update index: %w", err)
	}
	return dir, nil
}

// Load reads a run's meta by id.
func (s *Store) Load(id string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(s.root, id, "meta.json"))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// List returns the index entries, newest first.
func (s *Store) List() ([]Meta, error) {
	return s.readIndex()
}

// The results server reads the tiers separately: a run list needs only meta,
// a flame graph only folded, a waterfall only traces. Nothing loads all four.

// LoadFolded reads a run's folded span aggregate, the flame-graph input.
func (s *Store) LoadFolded(id string) (*span.Folded, error) {
	return readJSON[*span.Folded](s.artifact(id, "folded.json"))
}

// LoadTraces reads a run's sampled raw trace trees, the waterfall input.
func (s *Store) LoadTraces(id string) ([]*span.Span, error) {
	return readJSON[[]*span.Span](s.artifact(id, "traces.json"))
}

// LoadSeries reads a run's bucketed outcomes and latencies over time.
func (s *Store) LoadSeries(id string) (collector.Series, error) {
	return readJSON[collector.Series](s.artifact(id, "series.json"))
}

// LoadSamples reads the per-flow-run tier, thinned per its capture policy.
func (s *Store) LoadSamples(id string) (collector.Samples, error) {
	return readJSON[collector.Samples](s.artifact(id, "samples.json"))
}

// LoadMetrics reads the generator's own resource series, so a saturated
// generator is never read as a saturated target.
func (s *Store) LoadMetrics(id string) ([]executor.MetricSample, error) {
	return readJSON[[]executor.MetricSample](s.artifact(id, "metrics.json"))
}

// LoadAgentSeries reads the target's own resource series, if an agent was
// attached to the run (issue #32) — nil, no error, when the file holds an
// empty or null array.
func (s *Store) LoadAgentSeries(id string) ([]agent.PolledSample, error) {
	return readJSON[[]agent.PolledSample](s.artifact(id, "agent.json"))
}

func (s *Store) artifact(id, name string) string {
	return filepath.Join(s.root, id, name)
}

func metaFrom(id string, info RunInfo, res *executor.Result, outcomes []collector.Outcome, agentSeries []agent.PolledSample) Meta {
	breached := false
	for _, o := range outcomes {
		if !o.Pass {
			breached = true
			break
		}
	}
	// Mode is a plain string here by the same convention the server reads it
	// back with (m.Mode == "soak"); store deliberately doesn't import ir.
	var knee *collector.Knee
	if info.Mode == "stress" {
		knee = collector.ClassifyKnee(res, outcomes, agentSeries)
	}
	return Meta{
		ID:            id,
		Scenario:      info.Scenario,
		Mode:          info.Mode,
		Initiator:     info.Initiator,
		Target:        info.Target,
		Commit:        info.Commit,
		Dirty:         info.Dirty,
		StartedAt:     info.StartedAt,
		Duration:      res.Duration,
		Iterations:    res.Iterations,
		FlowRuns:      len(res.Samples),
		ErrorRate:     res.ErrorRate(),
		ThrottleRate:  res.ThrottleRate(),
		P50:           executor.Percentile(res.Samples, 0.50),
		P95:           executor.Percentile(res.Samples, 0.95),
		P99:           executor.Percentile(res.Samples, 0.99),
		Aborted:       res.Aborted,
		Thresholds:    outcomes,
		Breached:      breached,
		Knee:          knee,
		AgentAttached: len(agentSeries) > 0,
	}
}

// runID is a sortable UTC timestamp, unique per run at nanosecond resolution.
func runID(t time.Time) string {
	return t.UTC().Format("20060102T150405.000000000Z")
}

func (s *Store) indexPath() string { return filepath.Join(s.root, "index.json") }

func (s *Store) readIndex() ([]Meta, error) {
	b, err := os.ReadFile(s.indexPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var idx []Meta
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// appendIndex prepends the run so the index reads newest-first. Runs are
// normally sequential, so this read-modify-write is not locked.
func (s *Store) appendIndex(m Meta) error {
	idx, err := s.readIndex()
	if err != nil {
		return err
	}
	idx = append([]Meta{m}, idx...)
	return writeJSON(s.indexPath(), idx)
}

func readJSON[T any](path string) (T, error) {
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(b, &v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
