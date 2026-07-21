package executor

import (
	"sort"
	"time"

	"github.com/blackprince001/flowbench/internal/span"
)

// Sample is one flow-run's latency record. All offsets are from the run start.
// Intended is when the schedule wanted the run dispatched; Actual is when a VU
// actually began it. Under the closed model the two are equal; under a paced
// (open) model they diverge when the generator can't keep up.
type Sample struct {
	Flow     string        `json:"flow"`
	Intended time.Duration `json:"intended"`
	Actual   time.Duration `json:"actual"`
	Service  time.Duration `json:"service"`
	Outcome  span.Outcome  `json:"outcome"`
}

// Latency is the coordinated-omission-aware latency: the wait for a free slot
// plus the measured service time. Recording service alone would hide a stall,
// since a backed-up generator simply stops issuing the requests that would have
// been slow — so latency counts from when the run was due, not when it began.
func (s Sample) Latency() time.Duration { return (s.Actual - s.Intended) + s.Service }

// Percentile returns the q-quantile (0..1) of the samples' CO-aware latency.
func Percentile(samples []Sample, q float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	xs := make([]time.Duration, len(samples))
	for i, s := range samples {
		xs[i] = s.Latency()
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	i := int(q * float64(len(xs)-1))
	if i < 0 {
		i = 0
	} else if i >= len(xs) {
		i = len(xs) - 1
	}
	return xs[i]
}

// tally counts samples by outcome.
func tally(samples []Sample) map[span.Outcome]int {
	m := make(map[span.Outcome]int, 4)
	for _, s := range samples {
		m[s.Outcome]++
	}
	return m
}
