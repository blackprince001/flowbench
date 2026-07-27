// Package server is the results server embedded in the flowbench binary: a
// read-only view over a run store, bound to localhost (ADR 0004). It has no
// accounts, no storage of its own, and — for now — no write path at all.
package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/report"
	"github.com/blackprince001/flowbench/internal/span"
	"github.com/blackprince001/flowbench/internal/store"
)

// navLimit caps the sidebar's run list; the full set is always on the index.
const navLimit = 12

type Server struct {
	ws  *store.Workspace
	mux *http.ServeMux
}

// New serves a workspace: one project per run store. Every run URL is
// project-scoped, so two projects can hold runs recorded at the same instant
// without either shadowing the other.
func New(ws *store.Workspace) *Server {
	s := &Server{ws: ws, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("GET /p/{project}/{$}", s.runs)
	s.mux.HandleFunc("GET /p/{project}/runs/{id}", s.run)
	s.mux.HandleFunc("GET /p/{project}/runs/{id}/dashboard", s.dashboard)
	s.mux.HandleFunc("GET /p/{project}/runs/{id}/flame", s.flame)
	s.mux.HandleFunc("GET /p/{project}/runs/{id}/waterfall", s.waterfall)
	s.mux.HandleFunc("GET /p/{project}/runs/{id}/outcomes", s.outcomes)
	s.mux.HandleFunc("GET /p/{project}/runs/{id}/failures", s.failures)
	s.mux.HandleFunc("GET /p/{project}/runs/{id}/compare", s.compare)
	s.mux.Handle("GET "+report.AssetPrefix+"{file}", report.ServeAssets())
	return s
}

// NewSingle serves one store as an unnamed workspace, the shape `serve --store`
// had before projects existed.
func NewSingle(st *store.Store) *Server {
	return New(store.WorkspaceOf(st))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// index is the workspace root. With one project there is nothing to choose
// between, so it goes straight to that project's runs.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.ws.Single(); ok {
		http.Redirect(w, r, "/p/"+p.Slug+"/", http.StatusFound)
		return
	}

	cards := make([]report.ProjectCard, 0, len(s.ws.Projects()))
	for _, p := range s.ws.Projects() {
		runs, err := p.Runs()
		if err != nil {
			fail(w, "read run store "+p.Name, err)
			return
		}
		card := report.ProjectCard{
			Name:  p.Name,
			Href:  "/p/" + p.Slug + "/",
			Store: p.Store.Root(),
			Runs:  len(runs),
		}
		if len(runs) > 0 {
			card.Latest = runs[0].Scenario
			card.When = runs[0].StartedAt.Local().Format("2006-01-02 15:04")
			card.Verdict = verdict(runs[0])
			card.Scenarios = len(store.GroupByScenario(runs))
		}
		cards = append(cards, card)
	}

	render(w, report.RenderIndex, report.IndexPage{
		Shell: report.Shell{
			Title:      "Projects",
			Crumbs:     []report.Crumb{{Label: "flowbench"}, {Label: "projects"}},
			ProjectNav: s.projectNav(""),
		},
		Projects: cards,
	})
}

// project resolves the {project} path segment, or writes a 404.
func (s *Server) project(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	slug := r.PathValue("project")
	p, ok := s.ws.Project(slug)
	if !ok {
		http.Error(w, fmt.Sprintf("no project %q", slug), http.StatusNotFound)
		return p, false
	}
	return p, true
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	index, err := p.Runs()
	if err != nil {
		fail(w, "read run store", err)
		return
	}

	groups := make([]report.RunGroup, 0)
	for _, g := range store.GroupByScenario(index) {
		rows := make([]report.RunRow, 0, len(g.Runs))
		for i, m := range g.Runs {
			row := s.row(p, m)
			row.Latest = i == 0 // GroupByScenario keeps each group newest-first
			rows = append(rows, row)
		}
		groups = append(groups, report.RunGroup{Scenario: g.Scenario, Runs: rows})
	}

	render(w, report.RenderRuns, report.RunsPage{
		Shell: report.Shell{
			Title:      p.Name,
			Crumbs:     s.crumbs(p, ""),
			ProjectNav: s.projectNav(p.Slug),
			Nav:        s.nav(p, index, ""),
		},
		Project: p.Name,
		Store:   p.Store.Root(),
		Total:   len(index),
		Groups:  groups,
	})
}

// run is the run itself: the summary numbers, the run as time-series, where its
// time went, its gates, and a way into each detail view. Overview and dashboard
// were two tabs of one question, so they are one page.
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	p, m, index, ok := s.runContext(w, r)
	if !ok {
		return
	}
	id := m.ID

	// Each tier is loaded on its own and each is optional: a run cancelled
	// mid-flight, or one that kept no traces, still has a readable page.
	folded, _ := p.Store.LoadFolded(id)
	traces, _ := p.Store.LoadTraces(id)
	series, _ := p.Store.LoadSeries(id)
	samples, _ := p.Store.LoadSamples(id)
	metrics, _ := p.Store.LoadMetrics(id)

	peak := ""
	if at, n, found := report.PeakThrottle(series); found {
		peak = fmt.Sprintf("throttling peaks at %s (%d flow-runs)", at.Round(100*time.Millisecond), n)
	}

	base := s.runBase(p, id)
	charts := report.LinkCharts([]report.LineChart{
		report.ThroughputChart(series),
		report.LatencyChart(series),
		report.RatesChart(series),
		vuChart(series, metrics),
	}, base)
	chart, charts := report.SelectChart(charts, r.URL.Query().Get("chart"))
	bucket := intParam(r, "at", -1)

	page := report.RunPage{
		Shell:   s.shell(p, m, index, "overview", len(report.FlameFrames(folded)), traces, samples.Kept),
		RunHead: head(m),
		Tiles: []report.Tile{
			{Label: "flow-runs", Value: fmt.Sprint(m.FlowRuns), Sub: fmt.Sprintf("%d iterations", m.Iterations)},
			{Label: "duration", Value: m.Duration.Round(10 * time.Millisecond).String()},
			{Label: "error rate", Value: pct(m.ErrorRate), Tone: toneFor(m.ErrorRate > 0, "failed")},
			{Label: "throttle rate", Value: pct(m.ThrottleRate), Tone: toneFor(m.ThrottleRate > 0, "throttled")},
			{Label: "p50", Value: m.P50.Round(time.Microsecond).String()},
			{Label: "p95", Value: m.P95.Round(time.Microsecond).String()},
			{Label: "p99", Value: m.P99.Round(time.Microsecond).String()},
		},
		Charts:  charts,
		Chart:   chart,
		All:     base,
		Tallies: report.Tallies(series),
		Strip:   stripView(series, base, bucket),
		Funnels: report.Funnels(folded, traces),
		Peak:    peak,
		Bucket:  report.InspectBucket(series, bucket),
		Steps:   report.StepRows(folded),
		Gates:   gates(m),
		Agent:   "no agent attached — target CPU/memory overlay lands with #32",
		Links:   jumps(base, folded, len(traces), samples),
	}
	if m.Mode == "soak" {
		page.Trend = report.TrendFrom(series, m.Thresholds)
	}
	render(w, report.RenderRun, page)
}

func (s *Server) flame(w http.ResponseWriter, r *http.Request) {
	p, m, index, ok := s.runContext(w, r)
	if !ok {
		return
	}
	id := m.ID

	folded, _ := p.Store.LoadFolded(id)
	traces, _ := p.Store.LoadTraces(id)
	samples, _ := p.Store.LoadSamples(id)

	base := s.runBase(p, id) + "/flame"
	zoom := r.URL.Query().Get("zoom")

	frames := report.LinkFrames(report.FlameFramesAt(folded, zoom), base, zoom)
	detail, frames := report.SelectFrame(frames, r.URL.Query().Get("frame"))

	// The view's own baseline: the whole run unzoomed, the focused frame's total
	// once zoomed, so "% of view" always means what is on screen.
	total := time.Duration(0)
	for _, f := range frames {
		if f.Depth == 0 {
			total += f.Total
		}
	}

	render(w, report.RenderFlame, report.FlamePage{
		Shell:    s.shell(p, m, index, "flame", len(report.FlameFrames(folded)), traces, samples.Kept),
		RunHead:  head(m),
		Frames:   frames,
		Detail:   detail,
		Base:     base,
		TotalDur: total.Round(time.Millisecond).String(),
		Zoom:     zoom,
		ZoomOf:   lastSegment(zoom),
		Trail:    report.ZoomTrail(zoom, base),
	})
}

func (s *Server) waterfall(w http.ResponseWriter, r *http.Request) {
	p, m, index, ok := s.runContext(w, r)
	if !ok {
		return
	}
	id := m.ID

	folded, _ := p.Store.LoadFolded(id)
	traces, _ := p.Store.LoadTraces(id)
	samples, _ := p.Store.LoadSamples(id)

	trace, at := report.TraceAt(traces, intParam(r, "trace", -1))
	rows := report.WaterfallRows(trace)
	detail, rows := report.SelectRow(rows, r.URL.Query().Get("span"))

	note := "no traces were kept for this run — capture policy keeps every failure plus a sample of the rest"
	traceOf := ""
	if trace != nil {
		note = fmt.Sprintf("%d kept of %d flow-runs", len(traces), m.FlowRuns)
		traceOf = fmt.Sprintf("trace #%d, %d spans", at, len(rows))
	}

	render(w, report.RenderWaterfall, report.WaterfallPage{
		Shell:   s.shell(p, m, index, "waterfall", len(report.FlameFrames(folded)), traces, samples.Kept),
		RunHead: head(m),
		Traces:  report.Summarize(traces, at),
		Rows:    rows,
		Detail:  detail,
		Base:    fmt.Sprintf("%s/waterfall?trace=%d", s.runBase(p, id), at),
		TraceOf: traceOf,
		Note:    note,
	})
}

func (s *Server) outcomes(w http.ResponseWriter, r *http.Request) {
	p, m, index, ok := s.runContext(w, r)
	if !ok {
		return
	}
	id := m.ID

	folded, _ := p.Store.LoadFolded(id)
	traces, _ := p.Store.LoadTraces(id)
	series, _ := p.Store.LoadSeries(id)
	samples, _ := p.Store.LoadSamples(id)

	base := s.runBase(p, id) + "/outcomes"
	filter := r.URL.Query().Get("outcome")
	bucket := intParam(r, "at", -1)
	run := intParam(r, "run", -1)

	// The grid's links carry the active filter, so selecting a flow-run does
	// not silently widen the set being looked at.
	cellBase := base + "?outcome=" + url.QueryEscape(filter)
	cells, omitted := report.Cells(samples, filter, run, cellBase)

	render(w, report.RenderOutcomes, report.OutcomesPage{
		Shell:   s.shell(p, m, index, "outcomes", len(report.FlameFrames(folded)), traces, samples.Kept),
		RunHead: head(m),
		Tallies: report.Tallies(series),
		Strip:   stripView(series, base, bucket),
		Bucket:  report.InspectBucket(series, bucket),
		Filters: report.Filters(samples, filter, base),
		Cells:   cells,
		Detail:  report.Inspect(samples, run),
		Note:    report.SampleNote(samples, omitted),
		Base:    base,
	})
}

// failures is the drill-down: every failure across the kept traces grouped by
// the step it happened in and its cause, each group opening onto the iterations
// behind it — and each of those onto its own waterfall, so a count is two links
// from the request that produced it.
func (s *Server) failures(w http.ResponseWriter, r *http.Request) {
	p, m, index, ok := s.runContext(w, r)
	if !ok {
		return
	}
	id := m.ID

	folded, _ := p.Store.LoadFolded(id)
	traces, _ := p.Store.LoadTraces(id)
	samples, _ := p.Store.LoadSamples(id)

	base := s.runBase(p, id)
	groups := report.FailureGroups(traces, base+"/failures", base+"/waterfall")
	selected, groups := report.SelectGroup(groups, r.URL.Query().Get("group"))

	render(w, report.RenderFailures, report.FailuresPage{
		Shell:    s.shell(p, m, index, "failures", len(report.FlameFrames(folded)), traces, samples.Kept),
		RunHead:  head(m),
		Groups:   groups,
		Selected: selected,
		Note:     report.FailureNote(samples, traces),
	})
}

// dashboard was the run's time-series tab before the two were merged. It stays
// as a redirect so links and bookmarks into it still land somewhere real,
// carrying any ?chart= with them.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	p, m, _, ok := s.runContext(w, r)
	if !ok {
		return
	}
	to := s.runBase(p, m.ID)
	if q := r.URL.RawQuery; q != "" {
		to += "?" + q
	}
	http.Redirect(w, r, to, http.StatusMovedPermanently)
}

// vuChart plots active virtual users over time from the generator's own metric
// series (metrics.json). Its samples sit on their own grid, so they are placed by
// time within the run's span rather than by index.
func vuChart(series collector.Series, metrics []executor.MetricSample) report.LineChart {
	span := report.SeriesSpan(series)
	for _, mm := range metrics {
		if mm.At > span {
			span = mm.At
		}
	}
	pts := make([]report.ChartPoint, 0, len(metrics))
	var last int
	for _, mm := range metrics {
		x := 0.0
		if span > 0 {
			x = float64(mm.At) / float64(span)
		}
		pts = append(pts, report.ChartPoint{X: x, Y: float64(mm.ActiveVUs)})
		last = mm.ActiveVUs
	}
	return report.LineChartOf("vus", "Virtual users", []report.ChartSeries{{
		Label: "VUs", Tone: "kind-retry", Points: pts, Last: fmt.Sprint(last),
	}}, func(v float64) string { return fmt.Sprintf("%.0f", v) }, span)
}

// compare sets a run beside a baseline — the previous run of the same scenario
// by default, or any same-scenario run named by ?with= — and reports the metric
// deltas, the gates that changed verdict, and the step that regressed most. An
// unknown or cross-scenario baseline falls back to the default rather than
// erroring, the way a stale ?frame= degrades on the flame page.
func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	p, m, index, ok := s.runContext(w, r)
	if !ok {
		return
	}
	id := m.ID

	folded, _ := p.Store.LoadFolded(id)
	traces, _ := p.Store.LoadTraces(id)
	samples, _ := p.Store.LoadSamples(id)

	base := s.runBase(p, id) + "/compare"
	peers := sameScenario(index, m)

	bm, haveB := store.Meta{}, false
	if with := r.URL.Query().Get("with"); with != "" {
		if cand, err := p.Store.Load(with); err == nil && cand.Scenario == m.Scenario && cand.ID != id {
			bm, haveB = cand, true
		}
	}
	if !haveB {
		bm, haveB = previousRun(peers, m)
	}

	page := report.ComparePage{
		Shell:     s.shell(p, m, index, "compare", len(report.FlameFrames(folded)), traces, samples.Kept),
		RunHead:   head(m),
		Cur:       s.compareRef(p, m),
		Baselines: baselineOptions(peers, base, bm.ID),
	}

	if !haveB {
		page.Note = "no earlier run of this scenario to compare against — record another run, or pick one above"
		render(w, report.RenderCompare, page)
		return
	}

	baseFolded, _ := p.Store.LoadFolded(bm.ID)
	framesCur := report.FlameFrames(folded)
	framesBase := report.FlameFrames(baseFolded)

	page.Baseline = s.compareRef(p, bm)
	page.Deltas = []report.Tile{
		report.DurDelta("p50", m.P50, bm.P50),
		report.DurDelta("p95", m.P95, bm.P95),
		report.DurDelta("p99", m.P99, bm.P99),
		report.RateDelta("error rate", m.ErrorRate, bm.ErrorRate, "failed"),
		report.RateDelta("throttle rate", m.ThrottleRate, bm.ThrottleRate, "throttled"),
	}
	page.Flips = report.ThresholdFlips(m.Thresholds, bm.Thresholds)
	// MarkRegressedStep marks the regressed frame in framesCur in place.
	page.Regressed = report.MarkRegressedStep(framesCur, framesBase)
	page.FramesCur = framesCur
	page.FramesBase = framesBase
	page.CurTotal = rootTotal(framesCur)
	page.BaseTotal = rootTotal(framesBase)

	render(w, report.RenderCompare, page)
}

// sameScenario returns the project's other runs of m's scenario, newest first.
func sameScenario(index []store.Meta, m store.Meta) []store.Meta {
	out := make([]store.Meta, 0)
	for _, o := range index {
		if o.Scenario == m.Scenario && o.ID != m.ID {
			out = append(out, o)
		}
	}
	return out
}

// previousRun is the newest run older than m — the natural baseline. peers are
// newest-first, so the first one older than m is it.
func previousRun(peers []store.Meta, m store.Meta) (store.Meta, bool) {
	for _, o := range peers {
		if o.StartedAt.Before(m.StartedAt) {
			return o, true
		}
	}
	return store.Meta{}, false
}

func (s *Server) compareRef(p store.Project, m store.Meta) report.CompareRef {
	return report.CompareRef{
		ID:        m.ID,
		When:      m.StartedAt.Local().Format("2006-01-02 15:04"),
		Verdict:   verdict(m),
		FlameHref: s.runBase(p, m.ID) + "/flame",
	}
}

func baselineOptions(peers []store.Meta, base, selected string) []report.BaselineOption {
	out := make([]report.BaselineOption, 0, len(peers))
	for _, o := range peers {
		out = append(out, report.BaselineOption{
			ID:       o.ID,
			When:     o.StartedAt.Local().Format("01-02 15:04"),
			Href:     base + "?with=" + url.QueryEscape(o.ID),
			Selected: o.ID == selected,
		})
	}
	return out
}

// rootTotal is the summed time of the top-level frames — the run's whole folded
// extent — for the flame column header.
func rootTotal(frames []report.Frame) string {
	var t time.Duration
	for _, f := range frames {
		if f.Depth == 0 {
			t += f.Total
		}
	}
	return t.Round(time.Millisecond).String()
}

// runContext resolves the project and the run, or writes the error response.
func (s *Server) runContext(w http.ResponseWriter, r *http.Request) (store.Project, store.Meta, []store.Meta, bool) {
	var m store.Meta
	p, ok := s.project(w, r)
	if !ok {
		return p, m, nil, false
	}

	id := r.PathValue("id")
	m, err := p.Store.Load(id)
	if err != nil {
		http.Error(w, fmt.Sprintf("no run %q in %s", id, p.Store.Root()), http.StatusNotFound)
		return p, m, nil, false
	}
	index, err := p.Runs()
	if err != nil {
		fail(w, "read run store", err)
		return p, m, nil, false
	}
	return p, m, index, true
}

func (s *Server) runBase(p store.Project, id string) string {
	return "/p/" + p.Slug + "/runs/" + id
}

func (s *Server) row(p store.Project, m store.Meta) report.RunRow {
	return report.RunRow{
		Href:         s.runBase(p, m.ID),
		Scenario:     m.Scenario,
		Mode:         m.Mode,
		Target:       m.Target,
		Started:      m.StartedAt.Local().Format("2006-01-02 15:04"),
		Duration:     m.Duration.Round(time.Millisecond).String(),
		FlowRuns:     m.FlowRuns,
		ErrorRate:    pct(m.ErrorRate),
		ThrottleRate: pct(m.ThrottleRate),
		P95:          m.P95.Round(time.Microsecond).String(),
		Verdict:      verdict(m),
	}
}

// crumbs lead back out of a run: the workspace root only appears when there is
// more than one project to go back to.
func (s *Server) crumbs(p store.Project, runID string) []report.Crumb {
	out := []report.Crumb{}
	if _, single := s.ws.Single(); !single {
		out = append(out, report.Crumb{Label: "projects", Href: "/"})
	}
	if runID == "" {
		return append(out, report.Crumb{Label: p.Name})
	}
	return append(out,
		report.Crumb{Label: p.Name, Href: "/p/" + p.Slug + "/"},
		report.Crumb{Label: runID},
	)
}

func (s *Server) shell(p store.Project, m store.Meta, index []store.Meta, view string, frames int, traces []*span.Span, runs int) report.Shell {
	base := s.runBase(p, m.ID)
	tabs := []report.Tab{
		{Label: "Run", Href: base, Selected: view == "overview"},
		{Label: "Flame graph", Href: base + "/flame", Count: frames, Selected: view == "flame"},
		{Label: "Waterfall", Href: base + "/waterfall", Count: len(traces), Selected: view == "waterfall"},
		{Label: "Failures", Href: base + "/failures", Count: report.FailingTraces(traces), Selected: view == "failures"},
		{Label: "Outcomes", Href: base + "/outcomes", Count: runs, Selected: view == "outcomes"},
		{Label: "Compare", Href: base + "/compare", Selected: view == "compare"},
	}
	return report.Shell{
		Title:      m.Scenario,
		Crumbs:     s.crumbs(p, m.ID),
		ProjectNav: s.projectNav(p.Slug),
		Nav:        s.nav(p, index, m.ID),
		Tabs:       tabs,
	}
}

// nav is the sidebar: this project's recent runs. Every other project is one
// click away via the workspace root, so the sidebar stays about where you are.
func (s *Server) nav(p store.Project, index []store.Meta, current string) []report.NavRun {
	// The index is newest-first, so the first run seen for each scenario is that
	// scenario's latest — tagged so the sidebar is scannable without dates.
	seen := map[string]bool{}
	if len(index) > navLimit {
		index = index[:navLimit]
	}
	out := make([]report.NavRun, 0, len(index))
	for _, m := range index {
		latest := !seen[m.Scenario]
		seen[m.Scenario] = true
		out = append(out, report.NavRun{
			Href:    s.runBase(p, m.ID),
			Label:   m.Scenario,
			Meta:    m.Mode,
			Current: m.ID == current,
			Latest:  latest,
		})
	}
	return out
}

// projectNav lists the workspace's projects for the sidebar, so switching
// between them is a click from anywhere rather than a trip back to the root. A
// single-project workspace has nothing to switch to, so it lists nothing.
func (s *Server) projectNav(current string) []report.NavRun {
	if _, single := s.ws.Single(); single && current != "" {
		return nil
	}
	out := make([]report.NavRun, 0, len(s.ws.Projects()))
	for _, p := range s.ws.Projects() {
		runs, _ := p.Runs()
		out = append(out, report.NavRun{
			Href:    "/p/" + p.Slug + "/",
			Label:   p.Name,
			Meta:    fmt.Sprint(len(runs)),
			Current: p.Slug == current,
		})
	}
	return out
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// verdict reduces a run to one word, and must agree with the exit code the CLI
// gave for the same run. That means reading the scenario's own thresholds, not
// the raw rates: a 0.2% error rate under a `< 1%` gate is a pass. A run with no
// thresholds declared has nothing to pass or fail, so it is only described.
func verdict(m store.Meta) report.Verdict {
	switch {
	case m.Aborted:
		return report.Verdict{Tone: "skipped", Label: "aborted"}
	case m.Breached:
		return report.Verdict{Tone: "failed", Label: "breached"}
	case len(m.Thresholds) > 0:
		return report.Verdict{Tone: "ok", Label: "passed"}
	case m.ErrorRate > 0:
		return report.Verdict{Tone: "failed", Label: "errors"}
	case m.ThrottleRate > 0:
		return report.Verdict{Tone: "throttled", Label: "throttled"}
	default:
		return report.Verdict{Tone: "ok", Label: "clean"}
	}
}

// gates turns the persisted threshold outcomes into rows for the page, so the
// browser shows exactly what the CLI printed.
func gates(m store.Meta) []report.Gate {
	out := make([]report.Gate, 0, len(m.Thresholds))
	for _, o := range m.Thresholds {
		tone := "ok"
		if !o.Pass {
			tone = "failed"
		}
		out = append(out, report.Gate{Expr: o.Expr, Detail: o.Detail, Pass: o.Pass, Tone: tone})
	}
	return out
}

func head(m store.Meta) report.RunHead {
	byline := []string{m.Mode, m.Target}
	if m.Initiator != "" {
		byline = append(byline, m.Initiator)
	}
	byline = append(byline, m.StartedAt.Local().Format("2006-01-02 15:04"))
	if m.Commit != "" {
		commit := m.Commit
		if len(commit) > 9 {
			commit = commit[:9]
		}
		if m.Dirty {
			commit += "-dirty"
		}
		byline = append(byline, commit)
	}
	return report.RunHead{Heading: m.Scenario, Byline: byline, Verdict: verdict(m)}
}

// jumps are the overview's ways into the detail views, each carrying the number
// that says whether it is worth opening.
func jumps(base string, folded *span.Folded, traces int, samples collector.Samples) []report.Jump {
	frames := report.FlameFrames(folded)

	return []report.Jump{{
		Title: "Flame graph",
		Value: fmt.Sprint(len(frames)),
		Note:  fmt.Sprintf("distinct span paths, %d deep", report.FlameDepth(frames)),
		Href:  base + "/flame",
	}, {
		Title: "Waterfall",
		Value: fmt.Sprint(traces),
		Note:  "kept traces, worst first",
		Href:  base + "/waterfall",
	}, {
		Title: "Outcomes",
		Value: fmt.Sprint(samples.Kept),
		Note:  "flow-runs, individually",
		Href:  base + "/outcomes",
	}}
}

func intParam(r *http.Request, key string, def int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return v
}

func toneFor(on bool, tone string) string {
	if on {
		return tone
	}
	return ""
}

func pct(v float64) string { return fmt.Sprintf("%.2f%%", v*100) }

// render buffers the page so a template error becomes a 500 rather than a
// half-written body under a 200.
func render[T any](w http.ResponseWriter, fn func(io.Writer, T) error, page T) {
	var buf bytes.Buffer
	if err := fn(&buf, page); err != nil {
		fail(w, "render page", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func fail(w http.ResponseWriter, what string, err error) {
	http.Error(w, fmt.Sprintf("flowbench: %s: %v", what, err), http.StatusInternalServerError)
}

// stripView lays out the outcomes-over-time graph. The per-bucket cells carry
// the links, tooltips and selection; the stream is drawn from those same cells,
// so the graph and its hit areas cannot drift apart.
func stripView(series collector.Series, base string, bucket int) report.StripView {
	cells := report.StripLinks(report.Strip(series), base, bucket)
	return report.StripView{
		Cells:  cells,
		Stream: report.StreamOf(cells),
		End:    report.SeriesSpan(series).Round(100 * time.Millisecond).String(),
	}
}
