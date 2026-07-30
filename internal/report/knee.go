package report

import (
	"fmt"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
)

// KneeSection is the stress run's knee card: the classification the collector
// persisted (issue #39), or an honest empty state. It renders only on stress
// runs — the knee is a stress finding the way drift is a soak finding.
type KneeSection struct {
	Found  bool
	Class  string
	Tone   string // chip tone: the outcome vocabulary the rest of the page uses
	Detail string
	Where  string // "thresholds first broke 2.5s in, at ~340 VUs"
	Note   string // empty-state prose when no knee was recorded
}

// KneeFrom builds the card from a run's persisted knee. breached
// disambiguates the nil case: a stress run that held has no knee by design,
// while a breached run without one predates classification (or was written by
// another producer) — the card should never claim a hold it can't prove.
func KneeFrom(k *collector.Knee, breached bool) *KneeSection {
	if k == nil {
		note := "the thresholds held for the whole ramp — no knee to classify; ramp higher or tighten the gates to find one"
		if breached {
			note = "thresholds broke but no knee classification was recorded for this run"
		}
		return &KneeSection{Note: note}
	}
	s := &KneeSection{
		Found:  true,
		Class:  string(k.Class),
		Detail: k.Detail,
	}
	switch k.Class {
	case collector.KneeThrottled:
		s.Tone = "throttled"
	case collector.KneeDegraded:
		s.Tone = "failed"
	default:
		s.Tone = "skipped"
	}
	s.Where = fmt.Sprintf("thresholds first broke %s into the run", k.At.Round(100*time.Millisecond))
	if k.VUs > 0 {
		s.Where += fmt.Sprintf(", at ~%d VUs", k.VUs)
	}
	return s
}
