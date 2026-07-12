// Package span defines the atomic unit of tracing (ADR 0007): a named,
// timed node with children. Every step, protocol phase, extraction,
// assertion, and poll or retry attempt is a span; one iteration's spans form
// a trace tree, the single source of truth behind both the waterfall view
// and flame graphs. Names are structural identity segments ("login",
// "http_call", "tls"); folding joins them into dot-paths like
// "login.http_call.tls", which is why identifiers elsewhere may not contain
// dots or "@". The storage encoding is deliberately undecided (PRD section
// 17); this package is the in-memory form the collector will fold in M2.
package span

import "time"

// Outcome classifies how the spanned work ended. Status-based
// classification (throttled vs failed, mode-aware defaults) happens at the
// assertion point in M2; adapters set ok or failed from transport truth.
type Outcome string

const (
	OutcomeOK        Outcome = "ok"
	OutcomeFailed    Outcome = "failed"
	OutcomeThrottled Outcome = "throttled"
	OutcomeSkipped   Outcome = "skipped"
)

// Span is one node of a trace tree. Start is the offset from the
// iteration's anchor time, so spans order causally without wall-clock
// storage; parenthood is the tree shape.
type Span struct {
	Name     string
	Start    time.Duration
	Duration time.Duration
	Outcome  Outcome
	Children []*Span
}

// New opens a span at the given offset from the iteration anchor.
func New(name string, start time.Duration) *Span {
	return &Span{Name: name, Start: start, Outcome: OutcomeOK}
}

// Child opens a child span and attaches it.
func (s *Span) Child(name string, start time.Duration) *Span {
	c := New(name, start)
	s.Children = append(s.Children, c)
	return c
}

// SelfTime is the span's duration minus its children's — the time this node
// spent on its own work, the quantity flame graphs render as width.
func (s *Span) SelfTime() time.Duration {
	self := s.Duration
	for _, c := range s.Children {
		self -= c.Duration
	}
	if self < 0 {
		return 0
	}
	return self
}
