package span_test

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/span"
)

func TestSelfTimeSubtractsChildren(t *testing.T) {
	s := span.New("login", 0)
	s.Duration = 100 * time.Millisecond
	s.Child("dns", 0).Duration = 30 * time.Millisecond
	s.Child("connect", 30*time.Millisecond).Duration = 20 * time.Millisecond

	if got := s.SelfTime(); got != 50*time.Millisecond {
		t.Errorf("SelfTime = %v, want 50ms", got)
	}
}

func TestSelfTimeNeverNegative(t *testing.T) {
	s := span.New("step", 0)
	s.Duration = 10 * time.Millisecond
	s.Child("overlapping", 0).Duration = 25 * time.Millisecond

	if got := s.SelfTime(); got != 0 {
		t.Errorf("SelfTime = %v, want clamped 0", got)
	}
}

func TestChildAttachesInOrder(t *testing.T) {
	s := span.New("step", 0)
	s.Child("first", 1*time.Millisecond)
	s.Child("second", 2*time.Millisecond)

	if len(s.Children) != 2 || s.Children[0].Name != "first" || s.Children[1].Name != "second" {
		t.Errorf("children = %+v", s.Children)
	}
	if s.Outcome != span.OutcomeOK {
		t.Errorf("new spans default to ok, got %q", s.Outcome)
	}
}
