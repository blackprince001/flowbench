package report_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/report"
	"github.com/blackprince001/flowbench/internal/span"
)

var update = flag.Bool("update", false, "rewrite golden files")

// trace builds a flow root the way the executor does: the root stamped with its
// offset into the run, its steps stamped from the iteration's own anchor.
func trace(runOffset time.Duration) *span.Span {
	root := span.New("flow:checkout", runOffset)
	root.Duration = 100 * time.Millisecond
	root.Outcome = span.OutcomeFailed

	step := span.New("checkout", 0)
	step.Duration = 90 * time.Millisecond
	root.Children = []*span.Span{step}

	call := step.Child("http_call", 5*time.Millisecond)
	call.Duration = 80 * time.Millisecond
	call.Payload = &span.Payload{Response: `{"error":"boom"}`}
	call.Child("dns", 5*time.Millisecond).Duration = 2 * time.Millisecond
	call.Child("ttfb", 10*time.Millisecond).Duration = 70 * time.Millisecond

	assert := step.Child("assert_status", 88*time.Millisecond)
	assert.Outcome = span.OutcomeFailed
	return root
}

// A flow root carries a run-relative offset while its steps are
// iteration-relative; placing children against the root's origin would push
// every bar off the track.
func TestWaterfallRowsStayInsideTheTrack(t *testing.T) {
	rows := report.WaterfallRows(trace(4800 * time.Millisecond))
	if len(rows) != 6 {
		t.Fatalf("want a row per span, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Left < 0 || r.Left > 100 || r.Width < 0 || r.Left+r.Width > 100.001 {
			t.Errorf("%s escapes the track: left=%.2f width=%.2f", r.Name, r.Left, r.Width)
		}
	}

	if root := rows[0]; root.Left != 0 || root.Width != 100 {
		t.Errorf("root should span the track, got left=%.2f width=%.2f", root.Left, root.Width)
	}
	// http_call starts 5ms into a 100ms iteration and runs 80ms.
	if call := rows[2]; call.Name != "http_call" || call.Left != 5 || call.Width != 80 {
		t.Errorf("call misplaced: %+v", call)
	}
}

// The same run-relative root offset must not move any bar.
func TestWaterfallIsIndependentOfRunOffset(t *testing.T) {
	early := report.WaterfallRows(trace(0))
	late := report.WaterfallRows(trace(9 * time.Second))
	for i := range early {
		if early[i].Left != late[i].Left || early[i].Width != late[i].Width {
			t.Fatalf("row %d moved with the run offset: %+v vs %+v", i, early[i], late[i])
		}
	}
}

func TestWaterfallOrdersCausallyAndKindsSpans(t *testing.T) {
	rows := report.WaterfallRows(trace(0))
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	want := "flow:checkout checkout http_call dns ttfb assert_status"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("causal order wrong:\n got %s\nwant %s", got, want)
	}

	kinds := map[string]report.Kind{
		"flow:checkout": report.KindFlow,
		"checkout":      report.KindStep,
		"http_call":     report.KindNet,
		"dns":           report.KindNet,
		"assert_status": report.KindLogic,
	}
	for _, r := range rows {
		if want, ok := kinds[r.Name]; ok && r.Kind != want {
			t.Errorf("%s classified %q, want %q", r.Name, r.Kind, want)
		}
	}
}

func TestPickTracePrefersFailureThenThrottle(t *testing.T) {
	ok := span.New("flow:a", 0)
	ok.Duration = time.Second // slowest, but healthy

	throttled := span.New("flow:b", 0)
	throttled.Duration = time.Millisecond
	throttled.Child("s", 0).Outcome = span.OutcomeThrottled

	failed := trace(0)

	if got := report.PickTrace([]*span.Span{ok, throttled, failed}); got != failed {
		t.Errorf("a failure outranks everything, got %v", got.Name)
	}
	if got := report.PickTrace([]*span.Span{ok, throttled}); got != throttled {
		t.Errorf("a throttle outranks a slow success, got %v", got.Name)
	}
	if got := report.PickTrace([]*span.Span{ok}); got != ok {
		t.Errorf("fall back to the slowest, got %v", got.Name)
	}
	if report.PickTrace(nil) != nil {
		t.Error("no traces should pick nothing")
	}
}

func folded() *span.Folded {
	f := span.NewFolded()
	for range 3 {
		f.Add(trace(0))
	}
	return f
}

func TestFlameFramesLayOutWidestFirst(t *testing.T) {
	frames := report.FlameFrames(folded())
	if len(frames) == 0 {
		t.Fatal("no frames")
	}
	if root := frames[0]; root.Name != "flow:checkout" || root.Left != 0 || root.Width != 100 {
		t.Errorf("flow root should fill the width, got %+v", root)
	}
	// flow -> step -> call -> phase
	if report.FlameDepth(frames) != 4 {
		t.Errorf("want 4 rows of depth, got %d", report.FlameDepth(frames))
	}

	// ttfb (70ms) must be laid out before its sibling dns (2ms).
	var order []string
	for _, f := range frames {
		if f.Depth == 3 {
			order = append(order, f.Name)
		}
	}
	if len(order) < 2 || order[0] != "ttfb" {
		t.Errorf("siblings should be widest-first, got %v", order)
	}

	for _, f := range frames {
		if f.Left < 0 || f.Left+f.Width > 100.001 {
			t.Errorf("%s escapes the canvas: left=%.4f width=%.4f", f.Name, f.Left, f.Width)
		}
	}
}

func TestFlameFramesEmpty(t *testing.T) {
	if got := report.FlameFrames(nil); got != nil {
		t.Errorf("nil folded should render nothing, got %v", got)
	}
	if got := report.FlameFrames(span.NewFolded()); got != nil {
		t.Errorf("empty folded should render nothing, got %v", got)
	}
}

func shell() report.Shell {
	return report.Shell{
		Title:  "checkout.flow.yaml",
		Crumbs: []report.Crumb{{Label: "flowbench", Href: "/"}, {Label: "20260724T133907Z"}},
		Nav:    []report.NavRun{{Href: "/runs/x", Label: "checkout.flow.yaml", Meta: "stress", Current: true}},
		Tabs: []report.Tab{
			{Label: "Overview", Href: "/runs/x", Selected: true},
			{Label: "Flame graph", Href: "/runs/x/flame", Count: 6},
		},
	}
}

func runHead() report.RunHead {
	return report.RunHead{
		Heading: "checkout.flow.yaml",
		Byline:  []string{"stress", "local-stub"},
		Verdict: report.Verdict{Tone: "throttled", Label: "throttled"},
	}
}

// check guards every rendered page against the escaper silently dropping
// geometry — html/template emits ZgotmplZ when it cannot verify a computed
// value in a style attribute, and every bar would vanish without a trace.
func check(t *testing.T, out string, want ...string) {
	t.Helper()
	if strings.Contains(out, "ZgotmplZ") {
		t.Error("geometry was stripped by the template escaper")
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("rendered page missing %q", w)
		}
	}
}

func TestRenderRunOverview(t *testing.T) {
	var sb strings.Builder
	err := report.RenderRun(&sb, report.RunPage{
		Shell:   shell(),
		RunHead: runHead(),
		Tiles:   []report.Tile{{Label: "throttle rate", Value: "40.10%", Tone: "throttled"}},
		Tallies: []report.Tally{{Outcome: "throttled", Count: 802, Label: "throttled", Lead: true}},
		Strip: report.StripView{
			Cells: []report.StripCell{{OK: 60, Throttled: 40, Title: "1s"}, {Empty: true, Title: "2s"}},
			End:   "5s",
		},
		Links: []report.Jump{{Title: "Flame graph", Value: "6", Note: "distinct span paths", Href: "/runs/x/flame"}},
	})
	if err != nil {
		t.Fatalf("RenderRun: %v", err)
	}
	out := sb.String()

	check(t, out, "s-throttled", "40.10%", "--throttled", "/runs/x/flame", "prefers-color-scheme: light")
	// Status must never be colour alone: the word rides along with the tone.
	if !strings.Contains(out, `class="pill t-throttled">throttled`) {
		t.Error("verdict pill must name its outcome in words")
	}
}

func TestRenderFlameSelectsAFrame(t *testing.T) {
	frames := report.LinkFrames(report.FlameFrames(folded()), "/runs/x/flame", "")
	detail, frames := report.SelectFrame(frames, "flow:checkout.checkout.http_call")
	if detail == nil {
		t.Fatal("http_call should be selectable by its span path")
	}
	if detail.PerCall == 0 || len(detail.Children) != 2 {
		t.Errorf("panel should break the frame down, got per-call %s and %d children", detail.PerCall, len(detail.Children))
	}

	var sb strings.Builder
	if err := report.RenderFlame(&sb, report.FlamePage{
		Shell: shell(), RunHead: runHead(), Frames: frames, Detail: detail,
		Base: "/runs/x/flame", TotalDur: "300ms",
	}); err != nil {
		t.Fatalf("RenderFlame: %v", err)
	}
	out := sb.String()
	check(t, out, `class="frame k-net is-selected"`, "Where its time goes", "ttfb", "frame=flow%3Acheckout", "Focus subtree")

	// The client viewport reads geometry off the frames, so every frame must
	// carry its true position as data — the script has no other source for it.
	for _, want := range []string{`data-l="0"`, `data-w="100"`, `data-path="flow:checkout"`, "data-flame", "data-flame-reset", "data-flame-overview", "data-flame-zoom-in"} {
		if !strings.Contains(out, want) {
			t.Errorf("flame page missing %q — client zoom would have nothing to work from", want)
		}
	}
	// A title attribute is the no-JS tooltip; the script swaps it for its own.
	if !strings.Contains(out, "title=\"flow:checkout") {
		t.Error("frames must carry a native tooltip for the no-JS case")
	}
	if !strings.Contains(out, "Wheel to zoom") {
		t.Error("the interaction should be discoverable, not guessed at")
	}
}

func TestRenderWaterfallSelectsASpan(t *testing.T) {
	rows := report.WaterfallRows(trace(4800 * time.Millisecond))
	detail, rows := report.SelectRow(rows, "0.0.0")
	if detail == nil || detail.Name != "http_call" {
		t.Fatalf("the index path should address the call span, got %+v", detail)
	}

	var sb strings.Builder
	if err := report.RenderWaterfall(&sb, report.WaterfallPage{
		Shell: shell(), RunHead: runHead(),
		Traces: report.Summarize([]*span.Span{trace(0)}, 0),
		Rows:   rows, Detail: detail,
		Base: "/runs/x/waterfall?trace=0", TraceOf: "trace #0", Note: "1 kept",
	}); err != nil {
		t.Fatalf("RenderWaterfall: %v", err)
	}
	// The selected span's captured payload is shown, and escaped.
	check(t, sb.String(), `<tr class="is-selected">`, "assert_status", `{&#34;error&#34;:&#34;boom&#34;}`, "&amp;span=0.0.0")
}

// A grid larger than the render cap must say how many squares it left out,
// rather than quietly showing a subset that looks like the whole run.
func TestCellsCapIsStated(t *testing.T) {
	var runs []collector.FlowRun
	for i := range 5000 {
		runs = append(runs, collector.FlowRun{Seq: i, Outcome: span.OutcomeOK})
	}
	s := collector.Samples{Total: 5000, Kept: 5000, EveryNth: 1, Runs: runs}

	cells, omitted := report.Cells(s, "", -1, "/x")
	if len(cells) != 4000 || omitted != 1000 {
		t.Fatalf("want 4000 drawn and 1000 omitted, got %d and %d", len(cells), omitted)
	}
	if note := report.SampleNote(s, omitted); !strings.Contains(note, "1000 not drawn") {
		t.Errorf("the cap must be stated, got %q", note)
	}
	// Under the cap nothing is claimed to be missing.
	if note := report.SampleNote(s, 0); strings.Contains(note, "not drawn") {
		t.Errorf("no cap applied, yet the note mentions one: %q", note)
	}
}

func TestRenderOutcomesShowsEveryFlowRun(t *testing.T) {
	var sb strings.Builder
	err := report.RenderOutcomes(&sb, report.OutcomesPage{
		Shell: shell(), RunHead: runHead(),
		Tallies: []report.Tally{{Outcome: "failed", Count: 2, Label: "failed"}},
		Strip: report.StripView{
			Cells: []report.StripCell{{OK: 100, Href: "/runs/x/outcomes?at=0", Title: "0s"}},
			End:   "5s",
		},
		Filters: []report.RunFilter{{Key: "", Label: "all", Count: 3, Href: "/runs/x/outcomes", Selected: true}},
		Cells: []report.RunCell{
			{Seq: 0, Tone: "ok", Title: "#0", Href: "?run=0"},
			{Seq: 1, Tone: "throttled", Title: "#1", Href: "?run=1", Selected: true},
		},
		Detail: &report.RunDetail{Seq: 1, Flow: "checkout", At: "1s", Latency: "15ms", Outcome: "throttled", Throttled: true},
		Note:   "all 3 flow-runs",
	})
	if err != nil {
		t.Fatalf("RenderOutcomes: %v", err)
	}
	check(t, sb.String(), `class="cell o-ok`, `class="cell o-throttled is-selected"`, "Flow-run #1", "all 3 flow-runs")
}

// The mark ships twice: inline in every page, and as a file under docs/assets
// that the README can point at, because GitHub strips inline SVG from
// markdown. One geometry, two renderings, so this pins the file to the code
// that draws it — regenerate with `go test ./internal/report -run Logo -update`.
func TestLogoFileMatchesTheMark(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "assets", "logo.svg")
	want := report.LogoSVG()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the checked-in mark: %v", err)
	}
	if string(got) != want {
		t.Errorf("docs/assets/logo.svg has drifted from the mark the pages draw:\ngot  %s\nwant %s", got, want)
	}
}
