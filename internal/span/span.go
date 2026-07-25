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
	Payload  *Payload `json:"Payload,omitempty"`

	// rawReq/rawResp hold the call's bodies by reference during the run; they
	// are turned into a stored (redacted, capped) Payload only when the trace is
	// kept, so sampling costs nothing for dropped traces. See capture.go.
	rawReq  []byte
	rawResp []byte

	// The call's identity and result, held the same way and for the same reason.
	callMethod string
	callURL    string
	callStatus int
	retryAfter string

	// failure is why this span failed, in the engine's own words. Held by
	// reference like the rest and materialized only for kept traces.
	failure string
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
