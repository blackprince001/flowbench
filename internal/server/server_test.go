package server_test

import (
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/server"
	"github.com/blackprince001/flowbench/internal/span"
	"github.com/blackprince001/flowbench/internal/store"
)

// throttledRun is the M2 headline shape: a stress run where most flow-runs were
// throttled and a couple genuinely failed.
func throttledRun() *executor.Result {
	folded := span.NewFolded()
	var samples []executor.Sample

	for i := range 100 {
		root := span.New("flow:checkout", time.Duration(i)*10*time.Millisecond)
		root.Duration = 15 * time.Millisecond
		call := root.Child("checkout", 0)
		call.Duration = 14 * time.Millisecond
		call.Child("ttfb", time.Millisecond).Duration = 12 * time.Millisecond

		s := executor.Sample{
			Flow:    "checkout",
			Actual:  time.Duration(i) * 10 * time.Millisecond,
			Service: 15 * time.Millisecond,
			Outcome: span.OutcomeOK,
		}
		switch {
		case i%10 == 0:
			s.Outcome, root.Outcome = span.OutcomeThrottled, span.OutcomeThrottled
			s.Throttled = true
		case i == 42:
			s.Outcome, root.Outcome = span.OutcomeFailed, span.OutcomeFailed
			call.Payload = &span.Payload{Response: `{"error":"boom"}`}
		}
		folded.Add(root)
		samples = append(samples, s)
	}

	failing := span.New("flow:checkout", 420*time.Millisecond)
	failing.Duration = 15 * time.Millisecond
	failing.Outcome = span.OutcomeFailed
	failing.Child("checkout", 0).Payload = &span.Payload{Response: `{"error":"boom"}`}

	return &executor.Result{
		Duration:   time.Second,
		Iterations: 100,
		Samples:    samples,
		Folded:     folded,
		Traces:     []*span.Span{failing},
	}
}

// saveRun writes one throttled run into a fresh store under dir and returns its
// id. dir must exist so a workspace can open it by name.
func saveRun(t *testing.T, dir string, gates []collector.Outcome) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := st.Save(store.RunInfo{
		Scenario:  "checkout.flow.yaml",
		Mode:      "stress",
		Target:    "local-stub",
		Initiator: "ada",
		StartedAt: time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC),
	}, throttledRun(), gates, nil)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(rd)
}

// serve builds a one-project workspace named "checkout" and returns the server
// plus the base path of its single run, so tests address project-scoped URLs
// without guessing the slug.
func serve(t *testing.T) (*server.Server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "checkout")
	id := saveRun(t, dir, []collector.Outcome{
		// The stub's own error floor sits under the declared gate: a run the CLI
		// exits 0 on must not read as failed in the browser.
		{Expr: "error_rate < 2%", Pass: true, Detail: "error_rate = 1.00%, want < 2.00%"},
	})
	ws, err := store.NewWorkspace([]string{"checkout=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	return server.New(ws), "/p/checkout/runs/" + id
}

func get(t *testing.T, s *server.Server, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

// projectOf trims a run base back to its project root, "/p/checkout/runs/{id}"
// → "/p/checkout/".
func projectOf(runBase string) string {
	return runBase[:strings.Index(runBase, "/runs/")+1]
}

// Issue #34's acceptance: a project lists its runs and links to each.
func TestProjectListsRuns(t *testing.T) {
	s, runBase := serve(t)

	code, body := get(t, s, projectOf(runBase))
	if code != http.StatusOK {
		t.Fatalf("project page returned %d", code)
	}
	for _, want := range []string{"checkout.flow.yaml", "stress", "local-stub", runBase} {
		if !strings.Contains(body, want) {
			t.Errorf("project page missing %q", want)
		}
	}
}

// The newest run of each scenario is tagged, so the list is scannable without
// reading timestamps.
func TestLatestRunIsTagged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Two runs of one scenario; the later StartedAt is the latest.
	for _, min := range []int{10, 40} {
		if _, err := st.Save(store.RunInfo{
			Scenario:  "checkout.flow.yaml",
			Mode:      "stress",
			StartedAt: time.Date(2026, 7, 24, 13, min, 0, 0, time.UTC),
		}, throttledRun(), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := store.NewWorkspace([]string{"checkout=" + dir})
	if err != nil {
		t.Fatal(err)
	}

	_, body := get(t, server.New(ws), "/p/checkout/")
	// Exactly one run in the scenario group carries the tag (the CSS mentions
	// the class too, so match the element).
	if got := strings.Count(body, `<span class="latest-tag">`); got != 1 {
		t.Errorf("want one latest tag per scenario, got %d", got)
	}
	// The tag sits on the 13:40 run's row, not the 13:10 one.
	i40 := strings.Index(body, "13:40")
	i10 := strings.Index(body, "13:10")
	tag := strings.Index(body, `<span class="latest-tag">`)
	if !(i40 < tag && tag < i10) {
		t.Errorf("latest tag should mark the newer run (13:40), positions: 40=%d tag=%d 10=%d", i40, tag, i10)
	}
	// The sidebar carries the same signal.
	if !strings.Contains(body, "latest-dot") {
		t.Error("sidebar should mark the latest run of the scenario")
	}
}

// Empty detail panels remain present and useful without reserving a tall blank
// rectangle beside compact charts.
func TestDetailPanelsRenderCompactEmptyStates(t *testing.T) {
	s, runBase := serve(t)

	_, wf := get(t, s, runBase+"/waterfall")
	if !strings.Contains(wf, `class="card panel is-empty"`) {
		t.Error("waterfall span panel should reserve space when nothing is selected")
	}

	_, oc := get(t, s, runBase+"/outcomes")
	// Both the bucket panel and the flow-run panel start empty and reserved.
	if got := strings.Count(oc, `class="card panel is-empty"`); got != 2 {
		t.Errorf("outcomes should have two reserved empty panels, got %d", got)
	}
	if !strings.Contains(oc, `class="rail-label"`) {
		t.Error("the rail should say what it is above the panels")
	}

	_, fl := get(t, s, runBase+"/flame")
	if !strings.Contains(fl, "flame-inspector is-empty") {
		t.Error("flame inspector should reserve space when no frame is selected")
	}

	// And once selected, the panel is no longer empty — proving the class tracks
	// selection rather than being always-on.
	_, sel := get(t, s, runBase+"/outcomes?at=0")
	if strings.Count(sel, `class="card panel is-empty"`) != 1 {
		t.Error("selecting a bucket should fill the bucket panel, leaving only the flow-run panel empty")
	}
}

func TestRunOverviewSummarizesAndLinksOut(t *testing.T) {
	s, runBase := serve(t)

	code, body := get(t, s, runBase)
	if code != http.StatusOK {
		t.Fatalf("run page returned %d", code)
	}

	// Summary: the aggregate numbers and the attribution the run was saved with.
	for _, want := range []string{"flow-runs", "throttle rate", "checkout.flow.yaml", "ada", "local-stub"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview missing %q", want)
		}
	}
	if !strings.Contains(body, "Outcomes over time") || !strings.Contains(body, "s-throttled") {
		t.Error("outcome strip missing its throttled segments")
	}
	// The detail views are pages of their own, reachable from here.
	for _, want := range []string{runBase + "/flame", runBase + "/waterfall", runBase + "/outcomes"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview does not lead to %q", want)
		}
	}
}

func TestFlamePageSelectsAFrame(t *testing.T) {
	s, runBase := serve(t)
	base := runBase + "/flame"

	code, body := get(t, s, base)
	if code != http.StatusOK {
		t.Fatalf("flame page returned %d", code)
	}
	if !strings.Contains(body, "ttfb") || !strings.Contains(body, "No frame selected") {
		t.Error("flame page should render frames and an empty panel")
	}

	code, body = get(t, s, base+"?frame=flow:checkout.checkout")
	if code != http.StatusOK {
		t.Fatalf("frame selection returned %d", code)
	}
	// Match the class attribute, not the bare word — the stylesheet is inlined
	// and mentions every state class.
	if !strings.Contains(body, `class="frame k-step is-selected"`) {
		t.Error("selected frame is not marked")
	}
	for _, want := range []string{"calls folded", "mean per call", "Where its time goes", "ttfb"} {
		if !strings.Contains(body, want) {
			t.Errorf("frame panel missing %q", want)
		}
	}

	// A stale link must degrade to the plain view, not 500.
	if code, body := get(t, s, base+"?frame=nope.nope"); code != http.StatusOK || !strings.Contains(body, "No frame selected") {
		t.Errorf("unknown frame should fall back cleanly, got %d", code)
	}
}

// Zoom re-roots the graph: the focused frame becomes the full width, which is
// the only thing that makes a sub-percent frame readable.
func TestFlamePageZoomsByReRooting(t *testing.T) {
	s, runBase := serve(t)
	base := runBase + "/flame"

	_, body := get(t, s, base+"?zoom=flow:checkout.checkout.ttfb")
	if !strings.Contains(body, "zoomed to ttfb") {
		t.Error("the page should say where it is zoomed")
	}
	// ttfb is the deepest frame, so zoomed in it is the only one and fills the row.
	if !strings.Contains(body, `width:100.0000%;bottom:0px`) {
		t.Error("the zoomed frame should fill the width on the first row")
	}
	// The trail walks back out one level at a time.
	for _, want := range []string{
		">All spans</a>",
		`zoom=flow%3Acheckout">`, // out to the flow root
		`zoom=flow%3Acheckout.checkout">`,
		">checkout</a>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("zoom trail missing %q", want)
		}
	}

	// Selecting a frame while zoomed must keep the zoom.
	_, body = get(t, s, base+"?zoom=flow:checkout.checkout&frame=flow:checkout.checkout.ttfb")
	if !strings.Contains(body, "zoomed to checkout") {
		t.Error("selecting a frame snapped the zoom back out")
	}
	if !strings.Contains(body, `class="frame k-net is-selected"`) {
		t.Error("frame selection lost under zoom")
	}

	// A stale zoom shows the whole graph rather than an empty one.
	_, body = get(t, s, base+"?zoom=nope.nope")
	if !strings.Contains(body, "flow:checkout") {
		t.Error("unknown zoom should fall back to the whole run")
	}
}

func TestWaterfallPageSelectsATraceAndSpan(t *testing.T) {
	s, runBase := serve(t)
	base := runBase + "/waterfall"

	code, body := get(t, s, base)
	if code != http.StatusOK {
		t.Fatalf("waterfall page returned %d", code)
	}
	if !strings.Contains(body, "Kept traces") || !strings.Contains(body, "No span selected") {
		t.Error("waterfall should offer a trace picker and an empty panel")
	}

	code, body = get(t, s, base+"?trace=0&span=0.0")
	if code != http.StatusOK {
		t.Fatalf("span selection returned %d", code)
	}
	// The panel carries the span's timing and the payload captured against it.
	for _, want := range []string{"self time", `{&#34;error&#34;:&#34;boom&#34;}`, "Captured payload"} {
		if !strings.Contains(body, want) {
			t.Errorf("span panel missing %q", want)
		}
	}
}

func TestOutcomesPageShowsEveryFlowRunAndFilters(t *testing.T) {
	s, runBase := serve(t)
	base := runBase + "/outcomes"

	code, body := get(t, s, base)
	if code != http.StatusOK {
		t.Fatalf("outcomes page returned %d", code)
	}
	// 100 flow-runs were recorded and none were thinned, so all 100 squares are here.
	if got := strings.Count(body, `class="cell o-`); got != 100 {
		t.Errorf("want one square per flow-run, got %d", got)
	}
	if !strings.Contains(body, "all 100 flow-runs") {
		t.Error("the page should state what the grid covers")
	}

	// Filtering narrows the grid to the ten throttled flow-runs.
	_, body = get(t, s, base+"?outcome=throttled")
	if got := strings.Count(body, `class="cell o-`); got != 10 {
		t.Errorf("throttled filter should leave 10 squares, got %d", got)
	}

	// Selecting one flow-run describes it, keeping the filter.
	_, body = get(t, s, base+"?outcome=throttled&run=10")
	if !strings.Contains(body, "Flow-run #10") || !strings.Contains(body, "queued") {
		t.Error("flow-run panel missing its detail")
	}

	// Selecting a bucket describes that window on its own terms.
	_, body = get(t, s, base+"?at=0")
	for _, want := range []string{"throughput", "p95", "window"} {
		if !strings.Contains(body, want) {
			t.Errorf("bucket panel missing %q", want)
		}
	}
}

// The browser's verdict must agree with the exit code the CLI gave. Errors
// under the declared gate are a pass, not a failure — reading the raw rate
// instead of the threshold is how the two disagree.
func TestVerdictFollowsThresholdsNotRawRates(t *testing.T) {
	s, runBase := serve(t)

	_, body := get(t, s, runBase)
	if !strings.Contains(body, `class="pill t-ok">passed`) {
		t.Error("a run inside its gates should read as passed")
	}
	if strings.Contains(body, `class="pill t-failed">`) {
		t.Error("a nonzero error rate under its gate must not read as a failure")
	}
	// The gates themselves are shown, as the CLI printed them.
	for _, want := range []string{"Thresholds", "error_rate &lt; 2%", "want &lt; 2.00%"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview missing %q", want)
		}
	}
}

func TestVerdictReportsABreach(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := st.Save(
		store.RunInfo{Scenario: "breach.flow.yaml", Mode: "load", StartedAt: time.Now()},
		throttledRun(),
		[]collector.Outcome{{Expr: "p95(latency) < 5ms", Pass: false, Detail: "p95(latency) = 16ms, want < 5ms"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.NewWorkspace([]string{"svc=" + dir})
	if err != nil {
		t.Fatal(err)
	}

	_, body := get(t, server.New(ws), "/p/svc/runs/"+filepath.Base(rd))
	if !strings.Contains(body, `class="pill t-failed">breached`) {
		t.Error("a breaching run should say so")
	}
	if !strings.Contains(body, "o-failed\">breach") {
		t.Error("the breaching gate should be marked in the threshold table")
	}
}

// A run with throttles but no errors is called throttled, never failed
// (ADR 0006) — and the word appears, not just the colour.
func TestVerdictSeparatesThrottledFromFailed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	res := throttledRun()
	for i := range res.Samples {
		if res.Samples[i].Outcome == span.OutcomeFailed {
			res.Samples[i].Outcome = span.OutcomeOK
		}
	}
	rd, err := st.Save(store.RunInfo{Scenario: "s.yaml", Mode: "stress", StartedAt: time.Now()}, res, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.NewWorkspace([]string{"svc=" + dir})
	if err != nil {
		t.Fatal(err)
	}

	code, body := get(t, server.New(ws), "/p/svc/runs/"+filepath.Base(rd))
	if code != http.StatusOK {
		t.Fatalf("returned %d", code)
	}
	if !strings.Contains(body, `class="pill t-throttled">throttled`) {
		t.Error("a throttles-only run should read as throttled")
	}
	if strings.Contains(body, `class="pill t-failed">failed`) {
		t.Error("throttles must not be reported as failures")
	}
}

func TestUnknownRunIs404(t *testing.T) {
	s, runBase := serve(t)
	if code, _ := get(t, s, projectOf(runBase)+"runs/nope"); code != http.StatusNotFound {
		t.Errorf("unknown run returned %d, want 404", code)
	}
}

func TestEmptyStoreStillRenders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := store.NewWorkspace([]string{"empty=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	code, body := get(t, server.New(ws), "/p/empty/")
	if code != http.StatusOK {
		t.Fatalf("empty store returned %d", code)
	}
	if !strings.Contains(body, "Nothing recorded in this store yet") {
		t.Error("an empty store should say so, not render a bare table")
	}
}

// A multi-project workspace shows a chooser at the root; a single-project one
// goes straight in.
func TestWorkspaceIndex(t *testing.T) {
	a := filepath.Join(t.TempDir(), "checkout")
	b := filepath.Join(t.TempDir(), "billing")
	saveRun(t, a, nil)
	saveRun(t, b, nil)
	ws, err := store.NewWorkspace([]string{"Checkout=" + a, "Billing=" + b})
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(ws)

	code, body := get(t, s, "/")
	if code != http.StatusOK {
		t.Fatalf("index returned %d", code)
	}
	for _, want := range []string{"Checkout", "Billing", "/p/checkout/", "/p/billing/"} {
		if !strings.Contains(body, want) {
			t.Errorf("project index missing %q", want)
		}
	}

	// One project has nothing to choose, so its root redirects straight in.
	solo := server.New(store.WorkspaceOf(mustStore(t, a)))
	if code, _ := get(t, solo, "/"); code != http.StatusFound {
		t.Errorf("single-project root should redirect, got %d", code)
	}
}

func mustStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// runWithStep is a healthy checkout flow whose one step takes stepDur, so two
// runs built with different stepDur differ by exactly an injected slowdown.
func runWithStep(stepDur time.Duration) *executor.Result {
	folded := span.NewFolded()
	var samples []executor.Sample
	for i := range 20 {
		root := span.New("flow:checkout", time.Duration(i)*time.Millisecond)
		root.Duration = stepDur + 2*time.Millisecond
		step := root.Child("checkout", 0)
		step.Duration = stepDur
		step.Child("ttfb", time.Millisecond).Duration = stepDur - 2*time.Millisecond
		folded.Add(root)
		samples = append(samples, executor.Sample{
			Flow:    "checkout",
			Actual:  time.Duration(i) * time.Millisecond,
			Service: stepDur,
			Outcome: span.OutcomeOK,
		})
	}
	return &executor.Result{Duration: 100 * time.Millisecond, Iterations: 20, Samples: samples, Folded: folded}
}

// Issue #37: the dashboard renders the time-series charts and the per-step table
// for a stored run.
// Overview and dashboard are one page: the numbers and the shape that produced
// them, read together.
func TestRunPageCarriesChartsAndSteps(t *testing.T) {
	s, runBase := serve(t)

	code, body := get(t, s, runBase)
	if code != http.StatusOK {
		t.Fatalf("dashboard returned %d", code)
	}
	for _, want := range []string{"Charts", "<polyline", "req/s", "Steps", "checkout", "no agent attached"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Error("chart geometry was stripped by the escaper")
	}
}

// withAgentMetrics gives a synthetic result a generator self-metrics series,
// so the overlay's "generator" line has something to plot.
func withAgentMetrics(res *executor.Result) *executor.Result {
	res.Metrics = []executor.MetricSample{
		{At: 0, CPUSeconds: 1.0, HeapAlloc: 10 << 20},
		{At: time.Second, CPUSeconds: 1.4, HeapAlloc: 12 << 20},
		{At: 2 * time.Second, CPUSeconds: 1.9, HeapAlloc: 14 << 20},
	}
	return res
}

// saveRunWithAgent is saveRun plus a target-metrics agent series (issue #32),
// so the dashboard's overlay has both a generator and a target line to show.
func saveRunWithAgent(t *testing.T, dir string, gates []collector.Outcome, agentSeries []agent.PolledSample) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := st.Save(store.RunInfo{
		Scenario:  "checkout.flow.yaml",
		Mode:      "stress",
		Target:    "local-stub",
		Initiator: "ada",
		StartedAt: time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC),
	}, withAgentMetrics(throttledRun()), gates, agentSeries)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(rd)
}

// serveWithAgent is serve plus an attached target-metrics agent series.
func serveWithAgent(t *testing.T) (*server.Server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "checkout")
	agentSeries := []agent.PolledSample{
		{At: 0, Sample: agent.Sample{CPUSeconds: 10.0, MemUsedBytes: 500 << 20}},
		{At: time.Second, Sample: agent.Sample{CPUSeconds: 13.5, MemUsedBytes: 520 << 20}},
		{At: 2 * time.Second, Sample: agent.Sample{CPUSeconds: 15.5, MemUsedBytes: 540 << 20}},
	}
	id := saveRunWithAgent(t, dir, nil, agentSeries)
	ws, err := store.NewWorkspace([]string{"checkout=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	return server.New(ws), "/p/checkout/runs/" + id
}

// TestRunPageOverlaysTargetResourcesWhenAgentAttached is issue #37's
// acceptance: an agent-attached run shows target and generator resource
// series, distinguishable, on the run page.
func TestRunPageOverlaysTargetResourcesWhenAgentAttached(t *testing.T) {
	s, runBase := serveWithAgent(t)

	code, body := get(t, s, runBase)
	if code != http.StatusOK {
		t.Fatalf("run page returned %d", code)
	}
	for _, want := range []string{
		"CPU: target vs generator",
		"Memory: target RSS vs generator heap",
		"target", "generator", // distinguishable series labels
		"cores",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("run page missing %q", want)
		}
	}
	if strings.Contains(body, "no agent attached") {
		t.Error("an agent-attached run should not show the empty-state hint")
	}
}

// TestRunPageShowsEmptyStateWithoutAnAgent is the regression: a run with no
// agent attached keeps the plain hint and renders no overlay charts.
func TestRunPageShowsEmptyStateWithoutAnAgent(t *testing.T) {
	s, runBase := serve(t)

	code, body := get(t, s, runBase)
	if code != http.StatusOK {
		t.Fatalf("run page returned %d", code)
	}
	if !strings.Contains(body, "no agent attached") {
		t.Error("a run without an agent should keep the empty-state hint")
	}
	if strings.Contains(body, "CPU: target vs generator") {
		t.Error("no agent means no target-resource chart should render")
	}
}

// Issue #41's trend: a soak run's dashboard shows the drift section, including
// any persisted trend finding.
func TestDashboardShowsSoakTrend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := st.Save(
		store.RunInfo{Scenario: "soak.flow.yaml", Mode: "soak", StartedAt: time.Now()},
		throttledRun(),
		[]collector.Outcome{{Expr: "p95(latency) trend", Detail: "p95 latency crept 10ms → 30ms over the run"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.NewWorkspace([]string{"svc=" + dir})
	if err != nil {
		t.Fatal(err)
	}

	_, body := get(t, server.New(ws), "/p/svc/runs/"+filepath.Base(rd))
	if !strings.Contains(body, "Soak trend") {
		t.Error("a soak run should show the trend section")
	}
	if !strings.Contains(body, "crept") {
		t.Error("the persisted trend finding should be surfaced")
	}
}

// Issue #40's acceptance: comparing two runs of the same scenario where one has
// an injected slowdown highlights the regressed step.
func TestComparePageHighlightsTheRegressedStep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Baseline passes its gate; the later run is slower and breaches it.
	baseDir, err := st.Save(
		store.RunInfo{Scenario: "checkout.flow.yaml", Mode: "load", StartedAt: time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC)},
		runWithStep(10*time.Millisecond),
		[]collector.Outcome{{Expr: "p95(latency) < 30ms", Pass: true, Detail: "p95 = 12ms, want < 30ms"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	curDir, err := st.Save(
		store.RunInfo{Scenario: "checkout.flow.yaml", Mode: "load", StartedAt: time.Date(2026, 7, 24, 13, 30, 0, 0, time.UTC)},
		runWithStep(40*time.Millisecond),
		[]collector.Outcome{{Expr: "p95(latency) < 30ms", Pass: false, Detail: "p95 = 41ms, want < 30ms"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	baseID, curID := filepath.Base(baseDir), filepath.Base(curDir)

	ws, err := store.NewWorkspace([]string{"checkout=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(ws)
	base := "/p/checkout/runs/" + curID + "/compare"

	// With no ?with=, the previous run is the baseline.
	code, body := get(t, s, base)
	if code != http.StatusOK {
		t.Fatalf("compare returned %d", code)
	}
	for _, want := range []string{"Regression comparison", baseID, "biggest regression", "checkout", "is-regressed"} {
		if !strings.Contains(body, want) {
			t.Errorf("compare page missing %q", want)
		}
	}
	// The gate that passed in the baseline and fails now is marked regressed.
	if !strings.Contains(body, `class="chip o-failed">regressed`) {
		t.Error("the flipped threshold should read as a regression")
	}

	// An explicit baseline is honoured.
	if code, _ := get(t, s, base+"?with="+baseID); code != http.StatusOK {
		t.Errorf("explicit baseline returned %d", code)
	}
	// An unknown baseline degrades to the default rather than erroring.
	if code, body := get(t, s, base+"?with=nope"); code != http.StatusOK || !strings.Contains(body, "biggest regression") {
		t.Errorf("unknown baseline should fall back cleanly, got %d", code)
	}
	// The oldest run has nothing earlier: the empty state, never a 500.
	if code, body := get(t, s, "/p/checkout/runs/"+baseID+"/compare"); code != http.StatusOK || !strings.Contains(body, "no earlier run") {
		t.Errorf("the oldest run should show the empty state, got %d", code)
	}
}

// mixedFailureRun is issue #38's shape: one run whose flow-runs fail for
// different reasons in different steps — a refused status, a wrong body, a
// target that never answered — alongside throttles the mode counts as errors.
func mixedFailureRun() *executor.Result {
	folded := span.NewFolded()
	var samples []executor.Sample
	var traces []*span.Span

	step := func(root *span.Span, name string, p *span.Payload, outcome span.Outcome) *span.Span {
		st := root.Child(name, 0)
		st.Duration = 12 * time.Millisecond
		st.Outcome = outcome
		st.Payload = p
		return st
	}

	for i := range 24 {
		at := time.Duration(i) * 20 * time.Millisecond
		root := span.New("flow:checkout", at)
		root.Duration = 15 * time.Millisecond
		root.Outcome = span.OutcomeFailed
		s := executor.Sample{Flow: "checkout", Actual: at, Service: 15 * time.Millisecond, Outcome: span.OutcomeFailed}

		switch i % 4 {
		case 0: // the target refused: an assertion noticed the status
			a := step(root, "checkout", &span.Payload{Method: "POST", Status: 503}, span.OutcomeFailed).
				Child("assert_status", 11*time.Millisecond)
			a.Outcome = span.OutcomeFailed
			a.Payload = &span.Payload{Failure: "status: want 200, got 503"}
		case 1: // it answered, and the answer was wrong
			a := step(root, "checkout", &span.Payload{Method: "POST", Status: 200}, span.OutcomeFailed).
				Child("assert_body_paid", 11*time.Millisecond)
			a.Outcome = span.OutcomeFailed
			a.Payload = &span.Payload{Failure: "body $.paid: want true, got false"}
		case 2: // it never answered
			leg := step(root, "login", &span.Payload{
				Method:  "POST",
				Failure: "call failed: POST http://svc/login: context deadline exceeded",
			}, span.OutcomeFailed).Child("http_call", time.Millisecond)
			leg.Outcome = span.OutcomeFailed
		case 3: // it asked to be left alone, and this mode calls that an error
			step(root, "checkout", &span.Payload{
				Method: "POST", Status: 429, RetryAfter: "1", Failure: "throttled: HTTP 429",
			}, span.OutcomeThrottled)
			s.Throttled = true
		}

		folded.Add(root)
		samples = append(samples, s)
		traces = append(traces, root)
	}

	return &executor.Result{
		Duration:   time.Second,
		Iterations: len(samples),
		Samples:    samples,
		Folded:     folded,
		Traces:     traces,
	}
}

// serveFailures serves a one-project workspace holding one mixed-failure run.
func serveFailures(t *testing.T) (*server.Server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := st.Save(store.RunInfo{
		Scenario:  "checkout.flow.yaml",
		Mode:      "integration",
		Target:    "local-stub",
		StartedAt: time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC),
	}, mixedFailureRun(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.NewWorkspace([]string{"checkout=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	return server.New(ws), "/p/checkout/runs/" + filepath.Base(rd)
}

var hrefPat = regexp.MustCompile(`href="([^"]+)"`)

// hrefsIn returns the page's links that contain want, in document order, with
// the entity escaping html/template applied undone.
func hrefsIn(body, want string) []string {
	out := []string{}
	for _, m := range hrefPat.FindAllStringSubmatch(body, -1) {
		if href := html.UnescapeString(m[1]); strings.Contains(href, want) {
			out = append(out, href)
		}
	}
	return out
}

// Issue #38's acceptance: from a mixed-failure run, two clicks reach a specific
// failing iteration's waterfall.
func TestFailuresDrillDownReachesAnIterationInTwoClicks(t *testing.T) {
	s, runBase := serveFailures(t)

	code, body := get(t, s, runBase+"/failures")
	if code != http.StatusOK {
		t.Fatalf("failures page returned %d", code)
	}
	for _, want := range []string{"Failures by step and cause", "No group selected", "flow-runs"} {
		if !strings.Contains(body, want) {
			t.Errorf("failures page missing %q", want)
		}
	}

	// Click one: a group.
	groups := hrefsIn(body, "/failures?group=")
	if len(groups) < 4 {
		t.Fatalf("want a group per step and cause, got %v", groups)
	}
	code, body = get(t, s, groups[0])
	if code != http.StatusOK {
		t.Fatalf("group returned %d", code)
	}
	if !strings.Contains(body, "Recorded reason") || !strings.Contains(body, "Iterations") {
		t.Error("a selected group should show the reason and its iterations")
	}

	// Click two: an iteration, which lands on its waterfall with the span that
	// failed already selected.
	runs := hrefsIn(body, "/waterfall?trace=")
	if len(runs) == 0 {
		t.Fatal("a selected group should link to each of its iterations")
	}
	code, body = get(t, s, runs[0])
	if code != http.StatusOK {
		t.Fatalf("iteration waterfall returned %d", code)
	}
	if !strings.Contains(body, `<tr class="is-selected">`) {
		t.Error("the iteration's failing span should arrive selected")
	}
	if strings.Contains(body, "No span selected") {
		t.Error("the waterfall opened with nothing selected")
	}
}

// The acceptance's second half: throttled never appears inside generic errors.
// This run counts throttles as errors, so every flow-run below failed — and the
// throttles are still their own group, in their own colour and their own word.
func TestFailuresKeepThrottlesOutOfGenericErrors(t *testing.T) {
	s, runBase := serveFailures(t)

	_, body := get(t, s, runBase+"/failures")
	if !strings.Contains(body, `<span class="chip o-throttled">throttled</span>`) {
		t.Error("throttles should be grouped as throttles, in the throttle tone")
	}
	for _, want := range []string{"status", "assertion", "timeout"} {
		if !strings.Contains(body, `<span class="chip o-failed">`+want+`</span>`) {
			t.Errorf("no %q group — the causes should be separated", want)
		}
	}

	// Selecting the throttle group lists throttled iterations only.
	var throttle string
	for _, href := range hrefsIn(body, "/failures?group=") {
		if strings.Contains(href, "throttled") {
			throttle = href
		}
	}
	if throttle == "" {
		t.Fatal("no throttle group to select")
	}
	_, body = get(t, s, throttle)
	if !strings.Contains(body, "throttled: HTTP 429") {
		t.Error("the throttle group should show what the target answered")
	}
	if strings.Contains(body, "context deadline exceeded") {
		t.Error("a timeout leaked into the throttle group")
	}
}

// The tab carries the number that says whether the drill-down is worth opening.
func TestFailuresTabCountsFailingTraces(t *testing.T) {
	s, runBase := serveFailures(t)
	_, body := get(t, s, runBase)
	if !strings.Contains(body, `href="`+runBase+`/failures"`) {
		t.Fatal("every run page should offer the failures tab")
	}
	if !strings.Contains(body, `Failures <span class="tab-count">24</span>`) {
		t.Error("the failures tab should count the failing traces")
	}
}

// A clean run has nothing to drill into, and says so rather than 500ing.
func TestFailuresPageOnACleanRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "clean")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	res := mixedFailureRun()
	res.Traces = nil
	rd, err := st.Save(store.RunInfo{Scenario: "clean.flow.yaml", Mode: "load", StartedAt: time.Now()}, res, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.NewWorkspace([]string{"clean=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	code, body := get(t, server.New(ws), "/p/clean/runs/"+filepath.Base(rd)+"/failures")
	if code != http.StatusOK {
		t.Fatalf("returned %d", code)
	}
	if !strings.Contains(body, "no failing traces were kept") {
		t.Error("an empty drill-down should explain itself")
	}
}

// The stylesheet is inlined into every page, but the web fonts are files: they
// have to be reachable, cacheable, and typed, or every page silently falls back
// to the system faces.
func TestFontsAreServedAndCacheable(t *testing.T) {
	s, runBase := serve(t)

	_, body := get(t, s, runBase)
	if !strings.Contains(body, `url("/assets/OpenRunde-Regular.woff2")`) {
		t.Error("pages should ask for the embedded sans")
	}

	for _, name := range []string{
		"OpenRunde-Regular.woff2", "OpenRunde-Medium.woff2", "OpenRunde-Semibold.woff2",
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/"+name, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s returned %d", name, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != "font/woff2" {
			t.Errorf("%s served as %q", name, got)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s should be cacheable across navigations", name)
		}
		// wOF2 — the file is the font, not a stray text asset.
		if b := rec.Body.Bytes(); len(b) < 4 || string(b[:4]) != "wOF2" {
			t.Errorf("%s is not a woff2", name)
		}
	}
}

// The asset route serves fonts and nothing else: the templates and stylesheet
// are inlined by the renderer, never reachable as files.
func TestAssetRouteServesOnlyFonts(t *testing.T) {
	s, _ := serve(t)
	for _, path := range []string{
		"/assets/report.css",
		"/assets/flame.js",
		"/assets/nope.woff2",
		// The mux normalizes this to /assets/report.css before the handler sees
		// it; either way it must not reach a file.
		"/assets/OpenRunde-Regular.woff2/../report.css",
	} {
		if code, _ := get(t, s, path); code == http.StatusOK {
			t.Errorf("%s was served; the asset route is fonts only", path)
		}
	}
}

// A small multiple is a summary; opening one has to give it the whole card plus
// the detail there was no room for at four-up.
func TestRunPageExpandsOneChart(t *testing.T) {
	s, runBase := serve(t)
	base := runBase

	_, body := get(t, s, base)
	if !strings.Contains(body, `href="`+base+`?chart=latency"`) {
		t.Fatal("each chart should link to its own full-size view")
	}
	// Match the class attribute, not the bare word: the stylesheet is inlined
	// and mentions every state class.
	if strings.Contains(body, `class="chart is-expanded"`) {
		t.Error("the grid of small multiples has nothing expanded")
	}

	code, body := get(t, s, base+"?chart=latency")
	if code != http.StatusOK {
		t.Fatalf("expanded chart returned %d", code)
	}
	for _, want := range []string{
		"is-expanded",        // the chart takes the card
		"cpeak",              // with a rule at its highest point
		"peak ",              // stated in words, never the rule alone
		"chart-ymid",         // a labelled y axis, not just the maximum
		"axis-ticks",         // and quarter marks across the run
		"Min", "Mean", "Max", // the per-series summary a small multiple cannot carry
		"all charts", // and the way back
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expanded chart missing %q", want)
		}
	}
	// The other charts stay one click away.
	if !strings.Contains(body, `?chart=throughput"`) {
		t.Error("the expanded view should offer the other charts")
	}
	// A stale chart key degrades to the grid rather than erroring.
	if code, body := get(t, s, base+"?chart=nope"); code != http.StatusOK || strings.Contains(body, `class="chart is-expanded"`) {
		t.Errorf("an unknown chart should fall back to all of them, got %d", code)
	}
}

// The shell is three columns: runs and projects on the left, the flow in the
// middle, whatever is selected on the right. Both outer columns collapse, and
// the state is written on <html> so the stylesheet owns the layout.
func TestShellHasThreeColumns(t *testing.T) {
	s, runBase := serve(t)

	_, body := get(t, s, runBase)
	for _, want := range []string{
		`<nav class="side"`,                        // left: runs
		`<div class="flow">`,                       // middle: the flow, at a reading width
		`<aside class="rail"`,                      // right: details
		`data-toggle="side"`, `data-toggle="rail"`, // both collapsible
		`[data-side="off"]`, `[data-rail="off"]`, // and the CSS that does it
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell missing %q", want)
		}
	}

	// A page with nothing to inspect leaves the rail literally empty, so `:empty`
	// can collapse the column rather than leaving a 340px gap.
	if !strings.Contains(body, ".rail:empty { display: none; }") {
		t.Error("an empty rail should collapse itself")
	}
	if _, list := get(t, s, projectOf(runBase)); !strings.Contains(list, `<aside class="rail" aria-label="Details"></aside>`) {
		t.Error("the run list has nothing to inspect, so its rail should be empty")
	}
}

// The left column switches projects as well as runs, so moving between them
// does not mean a trip back to the workspace root.
func TestSidebarSwitchesProjects(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "checkout"), filepath.Join(dir, "billing")
	saveRun(t, a, nil)
	id := saveRun(t, b, nil)
	ws, err := store.NewWorkspace([]string{"Checkout=" + a, "Billing=" + b})
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(ws)

	_, body := get(t, s, "/p/billing/runs/"+id)
	if !strings.Contains(body, `id="nav-projects"`) {
		t.Fatal("a multi-project workspace should list its projects in the sidebar")
	}
	if !strings.Contains(body, `href="/p/checkout/"`) {
		t.Error("the other project should be one click away")
	}

	// One project is not a choice, so it is not offered inside a run.
	s2, runBase := serve(t)
	if _, single := get(t, s2, runBase); strings.Contains(single, `id="nav-projects"`) {
		t.Error("a single-project workspace has nothing to switch between")
	}
}

// Overview and dashboard were two tabs of one question. The merged page carries
// both, and the old URL still lands on it.
func TestDashboardRedirectsIntoTheRunPage(t *testing.T) {
	s, runBase := serve(t)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, runBase+"/dashboard?chart=latency", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("dashboard returned %d, want a redirect", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != runBase+"?chart=latency" {
		t.Errorf("redirected to %q, want the run page carrying the chart", got)
	}

	_, body := get(t, s, runBase)
	for _, want := range []string{"Summary", "Over the run", "Steps", "Thresholds", "error rate", "req/s"} {
		if !strings.Contains(body, want) {
			t.Errorf("merged run page missing %q", want)
		}
	}
	if strings.Contains(body, `>Dashboard<`) {
		t.Error("the dashboard tab should be gone, not merely redirected")
	}
}
