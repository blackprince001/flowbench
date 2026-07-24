package report

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"time"
)

//go:embed assets/report.css assets/flame.js templates/*.html
var assets embed.FS

// rowHeight is the flame-graph row pitch: 34px of frame plus a 2px gap, so
// depth maps to a bottom offset by multiplication alone.
const rowHeight = 36

// Shell is the chrome every page wears: the sidebar's run list, the breadcrumb
// trail, and — inside a run — the tabs between its views. Runs are the only
// entity the server has, so the sidebar navigates between them rather than
// between sections.
type Shell struct {
	Title  string
	Crumbs []Crumb
	Nav    []NavRun
	Tabs   []Tab
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

// StripView is the outcome strip plus the label closing its axis.
type StripView struct {
	Cells []StripCell
	End   string
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

// RunPage is a run's overview: the numbers, and a way into each detail view.
type RunPage struct {
	Shell
	RunHead
	Tiles   []Tile
	Tallies []Tally
	Strip   StripView
	Peak    string
	Gates   []Gate
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
	compareTmpl   = parse("templates/compare.html")
)

func parse(page string) *template.Template {
	return template.Must(template.New("").Funcs(funcs).ParseFS(
		assets, "templates/layout.html", "templates/partials.html", page))
}

var funcs = template.FuncMap{
	"css":       func() template.CSS { return template.CSS(mustRead("assets/report.css")) },
	"flameJS":   func() template.JS { return template.JS(mustRead("assets/flame.js")) },
	"dur":       humanDur,
	"framePos":  framePos,
	"barPos":    barPos,
	"segHeight": func(v float64) template.CSS { return template.CSS(fmt.Sprintf("height:%.4f%%", v)) },
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

// RenderCompare writes the run-versus-baseline comparison page.
func RenderCompare(w io.Writer, p ComparePage) error {
	return compareTmpl.ExecuteTemplate(w, "layout", p)
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
