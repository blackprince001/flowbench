package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/store"
)

// writeScenarioWithAgent is writeScenario plus a target carrying agent_addr,
// so a run against it polls a target-metrics agent (issue #32).
func writeScenarioWithAgent(t *testing.T, flowYAML, baseURL, agentAddr string) (scenario, targetPath string) {
	t.Helper()
	dir := t.TempDir()
	scenario = filepath.Join(dir, "checkout.flow.yaml")
	targetPath = filepath.Join(dir, "stub.yaml")
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(scenario, flowYAML)
	write(targetPath, fmt.Sprintf("name: teststub\nbase_urls:\n  - %s\nagent_addr: %s\n", baseURL, agentAddr))
	return scenario, targetPath
}

// agentAddrOf strips the httptest.Server's scheme, since agent.Poll expects a
// bare host:port and prepends "http://" itself.
func agentAddrOf(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

const agentLoadFlow = `flow: slowpoke
steps:
  - id: hit
    call: GET /slow
profile:
  mode: load
  vus: 2
  hold: 1500ms
`

// TestRunLoadWithAttachedAgentSavesTargetSeries is the issue #32 acceptance:
// a load run against a target with agent_addr set leaves a time-aligned
// target resource series in the run store, and the run's meta reports the
// agent as attached.
func TestRunLoadWithAttachedAgentSavesTargetSeries(t *testing.T) {
	agentSrv := httptest.NewServer(agent.Handler())
	t.Cleanup(agentSrv.Close)

	srv := slowStub(t, 2*time.Millisecond)
	scenario, targetPath := writeScenarioWithAgent(t, agentLoadFlow, srv.URL, agentAddrOf(agentSrv))
	storeDir := t.TempDir()

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", storeDir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}

	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want one stored run, got %d", len(runs))
	}
	m := runs[0]
	if !m.AgentAttached {
		t.Error("meta.agent_attached should be true when an agent was reachable throughout the run")
	}

	series, err := st.LoadAgentSeries(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) == 0 {
		t.Fatal("agent.json should hold at least one polled sample")
	}
	if series[0].NumCPU == 0 {
		t.Errorf("a real sample should report a nonzero core count: %+v", series[0])
	}
}

// TestRunSurvivesAgentDyingMidRun is the issue #32 acceptance's other half,
// verbatim: killing the agent mid-run leaves the run unharmed. A dead agent
// must never fail or block the flow it's attached to (fail-open).
func TestRunSurvivesAgentDyingMidRun(t *testing.T) {
	agentSrv := httptest.NewServer(agent.Handler())
	go func() {
		time.Sleep(400 * time.Millisecond)
		agentSrv.Close() // the agent goes away partway through the run
	}()

	srv := slowStub(t, 2*time.Millisecond)
	flow := strings.Replace(agentLoadFlow, "hold: 1500ms", "hold: 2s", 1)
	scenario, targetPath := writeScenarioWithAgent(t, flow, srv.URL, agentAddrOf(agentSrv))
	storeDir := t.TempDir()

	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"run", scenario, "--target", targetPath, "--store", storeDir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (a dead agent must not fail the run)\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "run saved to") {
		t.Errorf("the run should still save cleanly despite the agent dying:\n%s", stdout.String())
	}

	st, err := store.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("want one stored run despite the agent dying mid-run, got %d", len(runs))
	}
	// agent.json must still load cleanly, whether or not a sample landed
	// before the agent died.
	if _, err := st.LoadAgentSeries(runs[0].ID); err != nil {
		t.Errorf("LoadAgentSeries should not error even with a partial/empty series: %v", err)
	}
}
