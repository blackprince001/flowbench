package collector

import (
	"fmt"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/executor"
)

// Stress posture (issue #39): when a stress ramp breaks its thresholds, say
// *why* — the same broken gate means opposite things depending on the target's
// resource series. Rising latency/errors while target CPU or memory climbs is
// genuine saturation (degraded); a rising throttle rate while resources stay
// flat is an enforced limit (throttled). Without the agent's series the two
// are indistinguishable, which is the whole reason the agent exists.
const (
	// kneeResourceRise separates a "rising" resource from a "flat" one: the
	// growth, in utilization points (CPU 0..1 of all cores, memory 0..1 of
	// total), from the pre-knee window to the post-knee window.
	kneeResourceRise = 0.10

	// kneeMinBucketRuns is the fewest flow-runs a bucket needs before its
	// metrics can place the knee — a two-sample bucket breaking a p95 gate is
	// noise, not an onset.
	kneeMinBucketRuns = 5
)

// KneeClass is the stress knee's classification.
type KneeClass string

const (
	KneeDegraded     KneeClass = "degraded"     // thresholds broke as target resources climbed: genuine saturation
	KneeThrottled    KneeClass = "throttled"    // throttle rate rose while target resources stayed flat: an enforced limit
	KneeInconclusive KneeClass = "inconclusive" // signals absent or contradictory; Detail says which
)

// Knee is a classified stress knee: where the ramp broke, at what concurrency,
// and whether the break was saturation or throttling. It exists only when a
// threshold actually broke — a stress run that held has no knee to classify.
type Knee struct {
	Class KneeClass `json:"class"`
	// At is the onset: the start of the first bucket whose own metrics broke
	// a breached threshold (falling back to the run's midpoint when only the
	// whole run breaks).
	At time.Duration `json:"at"`
	// VUs is the generator's active VU count at the onset, 0 when the run
	// kept no self-metrics.
	VUs    int    `json:"vus,omitempty"`
	Detail string `json:"detail"`
}

// ClassifyKnee classifies a stress run's knee against the target's resource
// series. It returns nil when no threshold broke. outcomes are the evaluated
// gates (trend outcomes and other unparseable expressions are ignored);
// agentSeries is the attached agent's polls, and may be empty — the knee is
// then found but inconclusive, because throttling and saturation cannot be
// told apart without the target's side of the story.
func ClassifyKnee(res *executor.Result, outcomes []Outcome, agentSeries []agent.PolledSample) *Knee {
	breached := breachedThresholds(outcomes)
	if len(breached) == 0 || res == nil || len(res.Samples) == 0 {
		return nil
	}

	at := kneeOnset(res, breached)
	pre, post := splitAt(res.Samples, at)
	if len(pre) == 0 || len(post) == 0 {
		at = time.Duration(res.Duration) / 2
		pre, post = splitAt(res.Samples, at)
	}

	k := &Knee{At: at, VUs: vusAt(res.Metrics, at)}

	if len(pre) == 0 || len(post) == 0 {
		k.Class = KneeInconclusive
		k.Detail = "thresholds broke, but the run is too short to compare a before and an after"
		return k
	}

	throttleBroke, qualityBroke := breachKinds(breached)
	throttleRise := throttleRate(post) - throttleRate(pre)

	preCPU, postCPU, preMem, postMem, ok := resourceWindows(agentSeries, at)
	if !ok {
		k.Class = KneeInconclusive
		k.Detail = "thresholds broke, but no target agent series was recorded — attach an agent (target agent_addr) to tell saturation from throttling"
		return k
	}
	rising := postCPU-preCPU > kneeResourceRise || postMem-preMem > kneeResourceRise

	cpu := fmt.Sprintf("target CPU %.0f%% → %.0f%%", preCPU*100, postCPU*100)
	switch {
	case throttleBroke && !rising && throttleRise > 0:
		k.Class = KneeThrottled
		k.Detail = fmt.Sprintf(
			"throttle rate rose %.1f%% → %.1f%% across the knee while %s stayed flat — the target enforced a rate limit rather than saturating",
			throttleRate(pre)*100, throttleRate(post)*100, cpu)
	case qualityBroke && rising:
		k.Class = KneeDegraded
		k.Detail = fmt.Sprintf(
			"p95 rose %s → %s (errors %.1f%% → %.1f%%) as %s — the target is genuinely saturating",
			executor.Percentile(pre, 0.95).Round(time.Millisecond),
			executor.Percentile(post, 0.95).Round(time.Millisecond),
			errorRate(pre)*100, errorRate(post)*100, cpu)
	case throttleBroke && rising:
		k.Class = KneeInconclusive
		k.Detail = fmt.Sprintf(
			"throttle rate rose %.1f%% → %.1f%% but %s — the target throttles and saturates at once; neither signal is clean",
			throttleRate(pre)*100, throttleRate(post)*100, cpu)
	case qualityBroke && !rising:
		k.Class = KneeInconclusive
		k.Detail = fmt.Sprintf(
			"latency/errors broke their gates while %s stayed flat — the bottleneck is not this host's CPU or memory (a dependency, a lock, or the generator itself)",
			cpu)
	default:
		k.Class = KneeInconclusive
		k.Detail = "thresholds broke, but neither the throttle rate nor the target's resources moved with them"
	}
	return k
}

// breachedThresholds re-parses the breached outcomes' expressions. Trend
// outcomes ("p95(latency) trend") don't parse and are skipped, which is
// correct: they are a soak finding, not a stress gate.
func breachedThresholds(outcomes []Outcome) []Threshold {
	var out []Threshold
	for _, o := range outcomes {
		if o.Pass {
			continue
		}
		if t, err := ParseThreshold(o.Expr); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// breachKinds reports which families of gate broke: the throttle-rate family,
// and the quality family (latency or error rate).
func breachKinds(breached []Threshold) (throttle, quality bool) {
	for _, t := range breached {
		if t.kind == kindThrottleRate {
			throttle = true
		} else {
			quality = true
		}
	}
	return throttle, quality
}

// kneeOnset is the start of the first sufficiently-populated bucket whose own
// samples break any breached threshold — where the gate would first have
// tripped, read off the same buckets the dashboard plots. When no single
// bucket breaks (only the whole run does), the midpoint stands in.
func kneeOnset(res *executor.Result, breached []Threshold) time.Duration {
	end := time.Duration(res.Duration)
	for _, s := range res.Samples {
		if s.Actual > end {
			end = s.Actual
		}
	}
	width := bucketWidth(end)
	n := int(end/width) + 1

	buckets := make([][]executor.Sample, n)
	for _, s := range res.Samples {
		i := int(s.Actual / width)
		if i < 0 {
			i = 0
		} else if i >= n {
			i = n - 1
		}
		buckets[i] = append(buckets[i], s)
	}

	for i, b := range buckets {
		if len(b) < kneeMinBucketRuns {
			continue
		}
		for _, t := range breached {
			if !compare(t.actual(b), t.op, t.limit) {
				return time.Duration(i) * width
			}
		}
	}
	return end / 2
}

// splitAt partitions samples by dispatch time around the knee.
func splitAt(samples []executor.Sample, at time.Duration) (pre, post []executor.Sample) {
	for _, s := range samples {
		if s.Actual < at {
			pre = append(pre, s)
		} else {
			post = append(post, s)
		}
	}
	return pre, post
}

// vusAt is the generator's active VU count at the knee: the last self-metrics
// tick at or before it, or the first tick after when the knee lands before
// sampling began.
func vusAt(metrics []executor.MetricSample, at time.Duration) int {
	vus := 0
	for _, m := range metrics {
		if m.At > at {
			if vus == 0 {
				vus = m.ActiveVUs
			}
			break
		}
		vus = m.ActiveVUs
	}
	return vus
}

// resourceWindows summarizes the agent series either side of the knee: mean
// CPU utilization (0..1 across all cores, diffing the cumulative counter) and
// mean memory fraction. A window needs two polls to diff; when the split
// leaves one side short but the series as a whole has enough, the series'
// own midpoint stands in — better a shifted window than no classification.
func resourceWindows(series []agent.PolledSample, at time.Duration) (preCPU, postCPU, preMem, postMem float64, ok bool) {
	if len(series) < 3 {
		return 0, 0, 0, 0, false
	}
	split := 0
	for split < len(series) && series[split].At < at {
		split++
	}
	if split < 2 || len(series)-split < 2 {
		split = len(series) / 2
	}
	pre, post := series[:split], series[split:]
	if len(pre) < 2 || len(post) < 2 {
		return 0, 0, 0, 0, false
	}
	preCPU, preMem = windowUse(pre)
	postCPU, postMem = windowUse(post)
	return preCPU, postCPU, preMem, postMem, true
}

// windowUse is one window's CPU utilization (Δcpu_seconds over elapsed wall
// time × cores, the convention agent.Sample documents) and its mean memory
// fraction.
func windowUse(w []agent.PolledSample) (cpu, mem float64) {
	first, last := w[0], w[len(w)-1]
	if dt := (last.At - first.At).Seconds(); dt > 0 && last.NumCPU > 0 {
		cpu = (last.CPUSeconds - first.CPUSeconds) / (dt * float64(last.NumCPU))
	}
	n := 0
	for _, s := range w {
		if s.MemTotalBytes > 0 {
			mem += float64(s.MemUsedBytes) / float64(s.MemTotalBytes)
			n++
		}
	}
	if n > 0 {
		mem /= float64(n)
	}
	return cpu, mem
}
