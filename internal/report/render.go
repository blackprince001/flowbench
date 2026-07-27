package report

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed assets/report.css assets/flame.js assets/live.js assets/shell.js assets/fonts/*.woff2 templates/*.html
var assets embed.FS

// AssetPrefix is the URL space the embedded binary assets live under. The
// stylesheet and scripts are inlined into every page, but a web font is a file
// the browser caches across navigations, so it needs a URL of its own —
// inlining one would re-send it with every page.
const AssetPrefix = "/assets/"

// ServeAssets serves the embedded web fonts. They ship in the binary and change
// only when it does, so they are served immutable; anything else 404s rather
// than exposing the template and stylesheet sources under a second name.
func ServeAssets() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		b, err := assets.ReadFile("assets/fonts/" + name)
		if err != nil || !strings.HasSuffix(name, ".woff2") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(b)
	})
}

// rowHeight is the flame-graph row pitch: 34px of frame plus a 2px gap, so
// depth maps to a bottom offset by multiplication alone.
const rowHeight = 36

// Shell is the chrome every page wears: the left rail's projects and runs, the
// breadcrumb trail, and — inside a run — the tabs between its views. The right
// rail is filled by each page's own "detail" template, not from here, because
// what is worth inspecting is the page's business.
type Shell struct {
	Title      string
	Crumbs     []Crumb
	ProjectNav []NavRun
	Nav        []NavRun
	Tabs       []Tab
}

// Tab is one view of a run. Count is shown when the view has a natural size
// (kept traces, distinct frames); zero hides it.
type Tab struct {
	Label    string
	Href     string
	Count    int
	Selected bool
}

// Crumb with no Href is the current page.
type Crumb struct {
	Label string
	Href  string
}

type NavRun struct {
	Href    string
	Label   string
	Meta    string
	Current bool
	Latest  bool
}

// IndexPage is the workspace root: one card per project. It only appears when
// there is more than one, since choosing between one thing is not a choice.
type IndexPage struct {
	Shell
	Projects []ProjectCard
}

type ProjectCard struct {
	Name      string
	Href      string
	Store     string
	Runs      int
	Scenarios int
	Latest    string
	When      string
	Verdict   Verdict
}

// RunsPage is one project's runs, grouped by scenario — re-running a scenario is
// the common case, so its runs belong together.
type RunsPage struct {
	Shell
	Project string
	Store   string
	Total   int
	Groups  []RunGroup
}

type RunGroup struct {
	Scenario string
	Runs     []RunRow
}

type RunRow struct {
	Href         string
	Scenario     string
	Mode         string
	Target       string
	Started      string
	Duration     string
	FlowRuns     int
	ErrorRate    string
	ThrottleRate string
	P95          string
	Verdict      Verdict
	Latest       bool // newest run of its scenario, tagged so timestamps need not be read
}

// Verdict is a run's one-word outcome. Tone names a status colour; Label is the
// word, and both always ship together.
type Verdict struct {
	Tone  string
	Label string
}

// RunHead is the title block every page inside a run shares.
type RunHead struct {
	Heading string
	Byline  []string
	Verdict Verdict
}

// StripView is the outcome graph plus the label closing its axis. Cells are
// still the per-bucket links; the stream is what is drawn from them.
type StripView struct {
	Cells  []StripCell
	Stream Stream
	End    string
}

// Fact is one label/value pair in a detail panel. Tone names a status colour
// for the value; empty leaves it in ordinary ink.
type Fact struct {
	Label string
	Value string
	Tone  string
}

// Gate is one evaluated threshold, shown as the CLI printed it.
type Gate struct {
	Expr   string
	Detail string
	Pass   bool
	Tone   string
}

// RunPage is the run: the numbers, the run as time-series, where its time went,
// its gates, and a way into each detail view. Summary and time-series used to be
// two tabs, which meant reading a run in two places — the rates in one and the
// shape that produced them in the other.
type RunPage struct {
	Shell
	RunHead
	Tiles   []Tile
	Charts  []LineChart
	Chart   *LineChart // the one opened full size, when ?chart= names it
	All     string     // the link back to every chart at once
	Tallies []Tally
	Strip   StripView
	Funnels []Funnel // where flow-runs stopped, one per flow
	Peak    string
	Bucket  *BucketDetail // the selected strip column, shown in the rail
	Steps   []StepRow
	Gates   []Gate
	Agent   AgentOverlay  // the target-metrics overlay: charts once attached, an empty-state note otherwise
	Trend   *TrendSection // soak runs only; nil otherwise
	Links   []Jump
}

// Jump is a card on the overview that leads into one of the detail views,
// carrying the one number that says whether it is worth opening.
type Jump struct {
	Title string
	Note  string
	Value string
	Href  string
}

// FlamePage is the flame graph workspace with the inspected frame kept beside
// the visualization on wide screens, so context and detail remain visible
// together.
type FlamePage struct {
	Shell
	RunHead
	Frames   []Frame
	Detail   *FrameDetail
	Base     string
	TotalDur string

	// Zoom is the span path the graph is re-rooted at; Trail is the ancestors
	// above it, each a link back out one level.
	Zoom   string
	ZoomOf string
	Trail  []Frame
}

// WaterfallPage is one kept trace at a time, with one span inspected.
type WaterfallPage struct {
	Shell
	RunHead
	Traces  []TraceSummary
	Rows    []Row
	Detail  *Row
	Base    string
	TraceOf string
	Note    string
}

// OutcomesPage is the whole run's outcomes: bucketed over time, then every
// kept flow-run individually.
type OutcomesPage struct {
	Shell
	RunHead
	Tallies []Tally
	Strip   StripView
	Bucket  *BucketDetail
	Filters []RunFilter
	Cells   []RunCell
	Detail  *RunDetail
	Note    string
	Base    string
}

// FailuresPage is the failure drill-down: every failure across the kept traces
// grouped by step and cause, with one group's iterations open beside it.
type FailuresPage struct {
	Shell
	RunHead
	Groups   []FailureGroup
	Selected *FailureGroup
	Note     string
}

type Tile struct {
	Label string
	Value string
	Sub   string
	Tone  string
}

var (
	indexTmpl     = parse("templates/index.html")
	runsTmpl      = parse("templates/runs.html")
	runTmpl       = parse("templates/run.html")
	flameTmpl     = parse("templates/flame.html")
	waterfallTmpl = parse("templates/waterfall.html")
	outcomesTmpl  = parse("templates/outcomes.html")
	failuresTmpl  = parse("templates/failures.html")
	compareTmpl   = parse("templates/compare.html")
	liveTmpl      = parse("templates/live.html")
)

func parse(page string) *template.Template {
	return template.Must(template.New("").Funcs(funcs).ParseFS(
		assets, "templates/layout.html", "templates/partials.html", page))
}

var funcs = template.FuncMap{
	"css":        func() template.CSS { return template.CSS(mustRead("assets/report.css")) },
	"flameJS":    func() template.JS { return template.JS(mustRead("assets/flame.js")) },
	"liveJS":     func() template.JS { return template.JS(mustRead("assets/live.js")) },
	"shellJS":    func() template.JS { return template.JS(mustRead("assets/shell.js")) },
	"dur":        humanDur,
	"framePos":   framePos,
	"barPos":     barPos,
	"humanCount": humanCount,
	// leftAt positions an overlay against the plot's own box in percentage
	// terms, so it tracks the SVG as the browser scales it.
	"leftAt": func(x float64) template.CSS {
		return template.CSS(fmt.Sprintf("left:%.3f%%", x/ribbonW*100))
	},
	"markAt": func(x, y float64) template.CSS {
		return template.CSS(fmt.Sprintf("left:%.3f%%;top:%.3f%%", x/ribbonW*100, y/ribbonH*100))
	},
	"flameTall": func(fs []Frame) template.CSS {
		return template.CSS(fmt.Sprintf("height:%dpx", FlameDepth(fs)*rowHeight))
	},
	"indent": func(depth int) template.CSS { return template.CSS(fmt.Sprintf("padding-left:%dpx", 10+depth*13)) },
	"pct1":   func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
}

// RenderIndex writes the project list.
func RenderIndex(w io.Writer, p IndexPage) error {
	return indexTmpl.ExecuteTemplate(w, "layout", p)
}

// RenderRuns writes the run list.
func RenderRuns(w io.Writer, p RunsPage) error {
	return runsTmpl.ExecuteTemplate(w, "layout", p)
}

// RenderRun writes a run's overview.
func RenderRun(w io.Writer, p RunPage) error {
	return runTmpl.ExecuteTemplate(w, "layout", p)
}

// RenderFlame writes the flame-graph page.
func RenderFlame(w io.Writer, p FlamePage) error {
	return flameTmpl.ExecuteTemplate(w, "layout", p)
}

// RenderWaterfall writes the trace page.
func RenderWaterfall(w io.Writer, p WaterfallPage) error {
	return waterfallTmpl.ExecuteTemplate(w, "layout", p)
}

// RenderOutcomes writes the outcomes page.
func RenderOutcomes(w io.Writer, p OutcomesPage) error {
	return outcomesTmpl.ExecuteTemplate(w, "layout", p)
}

// RenderFailures writes the failure drill-down page.
func RenderFailures(w io.Writer, p FailuresPage) error {
	return failuresTmpl.ExecuteTemplate(w, "layout", p)
}

// RenderCompare writes the run-versus-baseline comparison page.
func RenderCompare(w io.Writer, p ComparePage) error {
	return compareTmpl.ExecuteTemplate(w, "layout", p)
}

// RenderLive writes the live view of an in-progress run.
func RenderLive(w io.Writer, p LivePage) error {
	return liveTmpl.ExecuteTemplate(w, "layout", p)
}

// framePos and barPos build geometry as template.CSS because html/template
// will not interpolate a computed value into a style attribute otherwise.
func framePos(f Frame) template.CSS {
	return template.CSS(fmt.Sprintf("left:%.4f%%;width:%.4f%%;bottom:%dpx", f.Left, f.Width, f.Depth*rowHeight))
}

func barPos(r Row) template.CSS {
	return template.CSS(fmt.Sprintf("left:%.4f%%;width:%.4f%%", r.Left, r.Width))
}

func humanDur(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d < time.Microsecond:
		return d.String()
	case d < time.Millisecond:
		return d.Round(100 * time.Nanosecond).String()
	case d < time.Second:
		return d.Round(time.Microsecond).String()
	case d < time.Minute:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}

func mustRead(name string) string {
	b, err := assets.ReadFile(name)
	if err != nil {
		panic(err) // embedded at build time; unreachable in a built binary
	}
	return string(b)
}
