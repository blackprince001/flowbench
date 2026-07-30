package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/store"
)

// Issue #39's acceptance, verbatim: against a rate-limited stub the report
// says throttled; against a genuinely saturating stub it says degraded. Both
// run the real CLI path — stub, fake agent, stress profile, store — and read
// the finding back from stdout and meta.json.

// fakeAgent serves the agent's wire protocol with a scripted CPU curve:
// cumulative cpu_seconds accrued at preCores until the switch, postCores
// after, on a 4-core host with flat memory. Wall-clock-driven, so the series
// shape is independent of poll timing.
func fakeAgent(t *testing.T, switchAfter time.Duration, preCores, postCores float64) *httptest.Server {
	t.Helper()
	var once sync.Once
	var start time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { start = time.Now() })
		e := time.Since(start)
		cpu := preCores * e.Seconds()
		if e > switchAfter {
			cpu = preCores*switchAfter.Seconds() + postCores*(e-switchAfter).Seconds()
		}
		json.NewEncoder(w).Encode(agent.Sample{
			CPUSeconds: 100 + cpu, NumCPU: 4,
			MemUsedBytes: 2 << 30, MemTotalBytes: 8 << 30,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stubFlip serves fast 200s until switchAfter, then behaves per late.
func stubFlip(t *testing.T, switchAfter time.Duration, late http.HandlerFunc) *httptest.Server {
	t.Helper()
	var once sync.Once
	var start time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { start = time.Now() })
		if time.Since(start) > switchAfter {
			late(w, r)
			return
		}
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runStress(t *testing.T, flow string, stub, agentSrv *httptest.Server) (exit int, stdout string, meta store.Meta) {
	t.Helper()

	old := defaultAgentPollInterval
	defaultAgentPollInterval = 100 * time.Millisecond
	t.Cleanup(func() { defaultAgentPollInterval = old })

	scenario, targetPath := writeScenarioWithAgent(t, flow, stub.URL, agentAddrOf(agentSrv))
	storeDir := t.TempDir()

	var out, errb strings.Builder
	exit = run(&out, &errb, []string{"run", scenario, "--target", targetPath, "--store", storeDir})

	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want one stored run, got %d\nstdout:\n%s\nstderr:\n%s", len(runs), out.String(), errb.String())
	}
	return exit, out.String(), runs[0]
}

const kneeFlowTemplate = `flow: knee
steps:
  - id: hit
    call: GET /work
profile:
  mode: stress
  vus: 6
  hold: 2s
  thresholds:
    - "%s"
`

func TestStressKneeAgainstRateLimitedStubReadsThrottled(t *testing.T) {
	// The stub flips to 429 + Retry-After halfway; the target's CPU stays
	// pinned at 40% throughout — an enforced limit, not saturation.
	stub := stubFlip(t, time.Second, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	agentSrv := fakeAgent(t, time.Second, 1.6, 1.6)

	flow := fmt.Sprintf(kneeFlowTemplate, "throttle_rate < 5%")
	exit, stdout, m := runStress(t, flow, stub, agentSrv)

	if exit != exitFail {
		t.Errorf("a breached stress run should exit %d, got %d\n%s", exitFail, exit, stdout)
	}
	if !strings.Contains(stdout, "knee_point_found: throttled") {
		t.Errorf("stdout should state the throttled finding:\n%s", stdout)
	}
	if m.Knee == nil || m.Knee.Class != collector.KneeThrottled {
		t.Fatalf("meta should persist a throttled knee, got %+v", m.Knee)
	}
}

func TestStressKneeAgainstSaturatingStubReadsDegraded(t *testing.T) {
	// The stub's latency jumps 2ms → 100ms halfway while the target's CPU
	// climbs from 40% to 95% — genuine saturation.
	stub := stubFlip(t, 800*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	agentSrv := fakeAgent(t, 800*time.Millisecond, 1.6, 3.8)

	flow := fmt.Sprintf(kneeFlowTemplate, "p99(latency) < 50ms")
	exit, stdout, m := runStress(t, flow, stub, agentSrv)

	if exit != exitFail {
		t.Errorf("a breached stress run should exit %d, got %d\n%s", exitFail, exit, stdout)
	}
	if !strings.Contains(stdout, "knee_point_found: degraded") {
		t.Errorf("stdout should state the degraded finding:\n%s", stdout)
	}
	if m.Knee == nil || m.Knee.Class != collector.KneeDegraded {
		t.Fatalf("meta should persist a degraded knee, got %+v", m.Knee)
	}
}
