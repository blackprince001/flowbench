package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
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
}

// Save writes a run's two span tiers, self-metrics, and attribution to a fresh
// directory and appends it to the index. It returns the run directory.
func (s *Store) Save(info RunInfo, res *executor.Result) (string, error) {
	id := runID(info.StartedAt)
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create run dir: %w", err)
	}

	meta := metaFrom(id, info, res)
	writes := []struct {
		name string
		v    any
	}{
		{"meta.json", meta},
		{"folded.json", res.Folded}, // tier 1: flame-graph aggregate
		{"traces.json", res.Traces}, // tier 2: sampled raw trees
		{"metrics.json", res.Metrics},
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

func metaFrom(id string, info RunInfo, res *executor.Result) Meta {
	return Meta{
		ID:           id,
		Scenario:     info.Scenario,
		Mode:         info.Mode,
		Initiator:    info.Initiator,
		Target:       info.Target,
		Commit:       info.Commit,
		Dirty:        info.Dirty,
		StartedAt:    info.StartedAt,
		Duration:     res.Duration,
		Iterations:   res.Iterations,
		FlowRuns:     len(res.Samples),
		ErrorRate:    res.ErrorRate(),
		ThrottleRate: res.ThrottleRate(),
		P50:          executor.Percentile(res.Samples, 0.50),
		P95:          executor.Percentile(res.Samples, 0.95),
		P99:          executor.Percentile(res.Samples, 0.99),
		Aborted:      res.Aborted,
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

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
