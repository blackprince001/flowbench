// Package secret tracks the values that must never reach storage. Every value
// resolved from {{ env.* }} is registered here (ADR 0005: credentials come
// from the environment, not a bespoke secrets file), and the capture path
// scrubs payloads, failure details, and logs through it before anything is
// persisted.
package secret

import (
	"sort"
	"strings"
	"sync"
)

const Placeholder = "[redacted]"

// Set is a thread-safe collection of secret values. VUs share one Set per run,
// so it guards every mutation and read.
type Set struct {
	mu     sync.RWMutex
	values map[string]struct{}
}

func NewSet() *Set {
	return &Set{values: map[string]struct{}{}}
}

// Add registers a secret. Empty values are ignored so they cannot turn every
// redaction into a no-op match.
func (s *Set) Add(v string) {
	if v == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[v] = struct{}{}
}

// Redact replaces every registered secret in text with the placeholder.
func (s *Set) Redact(text string) string {
	for _, v := range s.ordered() {
		text = strings.ReplaceAll(text, v, Placeholder)
	}
	return text
}

func (s *Set) RedactBytes(b []byte) []byte {
	return []byte(s.Redact(string(b)))
}

func (s *Set) Contains(v string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.values[v]
	return ok
}

// Values returns the registered secrets, longest-first.
func (s *Set) Values() []string { return s.ordered() }

// ordered returns the secrets longest-first, so a secret that contains another
// is replaced before its substring leaves a placeholder mid-value.
func (s *Set) ordered() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vs := make([]string, 0, len(s.values))
	for v := range s.values {
		vs = append(vs, v)
	}
	sort.Slice(vs, func(i, j int) bool { return len(vs[i]) > len(vs[j]) })
	return vs
}
