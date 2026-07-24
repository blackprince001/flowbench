package server_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	}, throttledRun(), gates)
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
		}, throttledRun(), nil); err != nil {
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

	_, fl := get(t, s, runBase+"/flame")
	if !strings.Contains(fl, `class="flame-inspector is-empty"`) {
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
	if !strings.Contains(body, "ttfb") || !strings.Contains(body, "Inspect a frame") {
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
	if code, body := get(t, s, base+"?frame=nope.nope"); code != http.StatusOK || !strings.Contains(body, "Inspect a frame") {
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
	rd, err := st.Save(store.RunInfo{Scenario: "s.yaml", Mode: "stress", StartedAt: time.Now()}, res, nil)
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
	)
	if err != nil {
		t.Fatal(err)
	}
	curDir, err := st.Save(
		store.RunInfo{Scenario: "checkout.flow.yaml", Mode: "load", StartedAt: time.Date(2026, 7, 24, 13, 30, 0, 0, time.UTC)},
		runWithStep(40*time.Millisecond),
		[]collector.Outcome{{Expr: "p95(latency) < 30ms", Pass: false, Detail: "p95 = 41ms, want < 30ms"}},
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
