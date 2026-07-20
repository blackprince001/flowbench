// Package span defines the atomic unit of tracing: a named, timed node with
// children. One iteration's spans form the trace tree behind both the
// waterfall view and flame graphs.
package span

import "time"

type Outcome string

const (
	OutcomeOK        Outcome = "ok"
	OutcomeFailed    Outcome = "failed"
	OutcomeThrottled Outcome = "throttled"
	OutcomeSkipped   Outcome = "skipped"
)

type Span struct {
	Name     string
	Start    time.Duration
	Duration time.Duration
	Outcome  Outcome
	Children []*Span
}

func New(name string, start time.Duration) *Span {
	return &Span{Name: name, Start: start, Outcome: OutcomeOK}
}

func (s *Span) Child(name string, start time.Duration) *Span {
	c := New(name, start)
	s.Children = append(s.Children, c)
	return c
}

// SelfTime is the span's duration minus its children's.
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
