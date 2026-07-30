package collector

import (
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

// kneeRun builds a 10s stress run whose second half misbehaves per late:
// 50 flow-runs per second, the first 5s clean and fast.
func kneeRun(late func(at time.Duration) executor.Sample) *executor.Result {
	var samples []executor.Sample
	for i := 0; i < 500; i++ {
		at := time.Duration(i) * 20 * time.Millisecond
		if at < 5*time.Second {
			samples = append(samples, sample(20*time.Millisecond, span.OutcomeOK, false, at))
		} else {
			samples = append(samples, late(at))
		}
	}
	return &executor.Result{Duration: 10 * time.Second, Samples: samples}
}

// agentRamp builds a 10s agent series polled every second on a 4-core target:
// cumulative CPU accrues at preCores until 5s, then at postCores; memory flat.
func agentRamp(preCores, postCores float64) []agent.PolledSample {
	var series []agent.PolledSample
	cpu := 100.0 // arbitrary since-boot baseline; only deltas matter
	for i := 0; i <= 10; i++ {
		at := time.Duration(i) * time.Second
		series = append(series, agent.PolledSample{At: at, Sample: agent.Sample{
			CPUSeconds: cpu, NumCPU: 4, MemUsedBytes: 2 << 30, MemTotalBytes: 8 << 30,
		}})
		if at < 5*time.Second {
			cpu += preCores
		} else {
			cpu += postCores
		}
	}
	return series
}

func breach(expr string) []Outcome {
	return []Outcome{{Expr: expr, Pass: false, Detail: "test breach"}}
}

func TestClassifyKneeNilWithoutBreach(t *testing.T) {
	res := kneeRun(func(at time.Duration) executor.Sample {
		return sample(20*time.Millisecond, span.OutcomeOK, false, at)
	})
	outcomes := []Outcome{{Expr: "error_rate < 1%", Pass: true}}
	if k := ClassifyKnee(res, outcomes, agentRamp(1.6, 1.6)); k != nil {
		t.Fatalf("no breach should mean no knee, got %+v", k)
	}
}

func TestClassifyKneeThrottled(t *testing.T) {
	// Second half: heavily throttled, latency still fine, target CPU flat at
	// 40% of 4 cores — a rate limiter, not saturation.
	res := kneeRun(func(at time.Duration) executor.Sample {
		return sample(20*time.Millisecond, span.OutcomeOK, true, at)
	})
	k := ClassifyKnee(res, breach("throttle_rate < 5%"), agentRamp(1.6, 1.6))
	if k == nil || k.Class != KneeThrottled {
		t.Fatalf("want throttled, got %+v", k)
	}
	if k.At < 4*time.Second || k.At > 6*time.Second {
		t.Errorf("knee onset should sit near the 5s break, got %s", k.At)
	}
	if !strings.Contains(k.Detail, "rate limit") {
		t.Errorf("detail should name the rate limit, got %q", k.Detail)
	}
}

func TestClassifyKneeDegraded(t *testing.T) {
	// Second half: latency blows past the gate as target CPU climbs from 40%
	// to 95% — genuine saturation.
	res := kneeRun(func(at time.Duration) executor.Sample {
		return sample(900*time.Millisecond, span.OutcomeOK, false, at)
	})
	k := ClassifyKnee(res, breach("p95(latency) < 200ms"), agentRamp(1.6, 3.8))
	if k == nil || k.Class != KneeDegraded {
		t.Fatalf("want degraded, got %+v", k)
	}
	if !strings.Contains(k.Detail, "saturating") {
		t.Errorf("detail should name saturation, got %q", k.Detail)
	}
}

func TestClassifyKneeInconclusiveWithoutAgent(t *testing.T) {
	res := kneeRun(func(at time.Duration) executor.Sample {
		return sample(20*time.Millisecond, span.OutcomeOK, true, at)
	})
	k := ClassifyKnee(res, breach("throttle_rate < 5%"), nil)
	if k == nil || k.Class != KneeInconclusive {
		t.Fatalf("no agent series should classify inconclusive, got %+v", k)
	}
	if !strings.Contains(k.Detail, "agent") {
		t.Errorf("detail should point at the missing agent, got %q", k.Detail)
	}
}

func TestClassifyKneeInconclusiveOnFlatResourcesQualityBreak(t *testing.T) {
	// Latency broke but the target never moved: the bottleneck is elsewhere,
	// and claiming "degraded" would send someone to the wrong host.
	res := kneeRun(func(at time.Duration) executor.Sample {
		return sample(900*time.Millisecond, span.OutcomeOK, false, at)
	})
	k := ClassifyKnee(res, breach("p95(latency) < 200ms"), agentRamp(1.6, 1.6))
	if k == nil || k.Class != KneeInconclusive {
		t.Fatalf("quality break over flat resources should be inconclusive, got %+v", k)
	}
}

func TestClassifyKneeVUsAtOnset(t *testing.T) {
	res := kneeRun(func(at time.Duration) executor.Sample {
		return sample(20*time.Millisecond, span.OutcomeOK, true, at)
	})
	for i := 0; i <= 10; i++ {
		res.Metrics = append(res.Metrics, executor.MetricSample{
			At: time.Duration(i) * time.Second, ActiveVUs: (i + 1) * 10,
		})
	}
	k := ClassifyKnee(res, breach("throttle_rate < 5%"), agentRamp(1.6, 1.6))
	if k == nil {
		t.Fatal("expected a knee")
	}
	if k.VUs < 40 || k.VUs > 70 {
		t.Errorf("VUs at a ~5s onset should read from the ramp (50-60), got %d", k.VUs)
	}
}
