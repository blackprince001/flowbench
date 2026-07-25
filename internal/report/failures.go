package report

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/span"
)

// Cause is why a flow-run failed, named in terms an author can act on. Spans
// carry no cause of their own, so it is recovered the way Kind is: from the
// span that failed, what the target answered on it, and the reason the engine
// recorded there.
type Cause string

const (
	CauseThrottled  Cause = "throttled"  // the target asked to be left alone
	CauseStatus     Cause = "status"     // it answered, and refused
	CauseAssertion  Cause = "assertion"  // it answered, and the answer was wrong
	CauseExtraction Cause = "extraction" // it answered, and the value was not there
	CauseTimeout    Cause = "timeout"    // it never answered in time
	CauseConnection Cause = "connection" // it was never reached
	CauseError      Cause = "error"      // recorded, and none of the above
)

// runsPerGroup bounds the iterations listed under one group. Every failure is
// kept as a trace, so a wholly-failing run can put thousands in one group; the
// list stops here and says how many it left out.
const runsPerGroup = 200

// FailureGroup is one step's failures of one cause — the unit this view exists
// to make countable. Label is the discriminator within the cause: the status
// code, the assertion that failed, the variable that was missing.
type FailureGroup struct {
	Key      string
	Step     string
	Cause    Cause
	Label    string
	Count    int
	Tone     string
	Detail   string // the reason the engine recorded, from the group's first iteration
	Href     string
	Selected bool
	Runs     []FailureRun
	Omitted  int
}

// FailureRun is one iteration inside a group, addressed as the waterfall
// addresses it: the kept trace's index, and the path of the span that failed.
type FailureRun struct {
	Trace    int
	Flow     string
	At       time.Duration
	Duration time.Duration
	Span     string
	Detail   string
	Outcome  span.Outcome
	Href     string
}

// FailureGroups groups every failure across the kept traces by the step it
// happened in and its cause, largest group first — so the question "what is
// actually breaking" is answered by the top row rather than by reading traces.
//
// base is this page (each group is a link to itself selected); waterfall is the
// run's waterfall, which each iteration deep-links into with its failing span
// already selected. Two links, from a count to the one iteration behind it.
func FailureGroups(traces []*span.Span, base, waterfall string) []FailureGroup {
	at := map[string]int{}
	out := make([]FailureGroup, 0)

	for i, t := range traces {
		if t == nil {
			continue
		}
		for _, f := range findings(WaterfallRows(t)) {
			key := f.key()
			g, ok := at[key]
			if !ok {
				g = len(out)
				at[key] = g
				out = append(out, FailureGroup{
					Key:    key,
					Step:   f.step,
					Cause:  f.cause,
					Label:  f.label,
					Tone:   f.tone(),
					Detail: f.detail,
				})
			}
			out[g].Count++
			if len(out[g].Runs) >= runsPerGroup {
				out[g].Omitted++
				continue
			}
			out[g].Runs = append(out[g].Runs, FailureRun{
				Trace:    i,
				Flow:     t.Name,
				At:       t.Start,
				Duration: t.Duration,
				Span:     f.row.Path,
				Detail:   f.detail,
				Outcome:  f.row.Outcome,
				Href:     fmt.Sprintf("%s?trace=%d&span=%s", waterfall, i, f.row.Path),
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	for i := range out {
		out[i].Href = base + "?group=" + url.QueryEscape(out[i].Key)
	}
	return out
}

// SelectGroup marks the group at key as selected and returns it. An unknown key
// selects nothing, so a stale link degrades to the plain grouping.
func SelectGroup(groups []FailureGroup, key string) (*FailureGroup, []FailureGroup) {
	if key == "" {
		return nil, groups
	}
	for i := range groups {
		if groups[i].Key == key {
			groups[i].Selected = true
			sel := groups[i]
			return &sel, groups
		}
	}
	return nil, groups
}

// FailingTraces counts the kept traces carrying a failure or a throttle — the
// tab's "is this worth opening" number.
func FailingTraces(traces []*span.Span) int {
	n := 0
	for _, t := range traces {
		if t != nil && isFailure(worstOutcome(t)) {
			n++
		}
	}
	return n
}

// FailureNote states what the grouping is built from, so a count is never read
// as the whole run. Capture is deliberately lopsided: every failed flow-run is
// kept as a trace, a throttled one is sampled like a success (ADR 0007), and
// past a ceiling even failures stop being kept. Where any of that bites, a
// group's count is a floor — and counts of different causes are not comparable.
func FailureNote(s collector.Samples, traces []*span.Span) string {
	kept := FailingTraces(traces)
	switch {
	case kept == 0:
		return "no failing traces were kept for this run"
	case s.Complete() && len(traces) >= s.Total:
		return fmt.Sprintf("every failure across all %d flow-runs", s.Total)
	default:
		return fmt.Sprintf("%d failing traces of %d flow-runs — failures are kept whole and throttles sampled, so a count is a floor",
			kept, s.Total)
	}
}

// finding is one span worth reporting in one trace: what failed, which authored
// step it belongs to, and why.
type finding struct {
	step   string
	cause  Cause
	label  string
	detail string
	row    Row
}

// key identifies the group a finding joins. It is the human-readable triple
// rather than a hash, so a link to a group says what it points at.
func (f finding) key() string {
	if f.label == "" {
		return f.step + " · " + string(f.cause)
	}
	return f.step + " · " + string(f.cause) + " · " + f.label
}

func (f finding) tone() string {
	if f.cause == CauseThrottled {
		return "throttled"
	}
	return "failed"
}

// findings reduces one trace's rows to the spans worth reporting: those that
// failed or were throttled and have no failed descendant of their own. An
// assertion failure marks both the assertion span and the step around it, so
// counting every failed span would count one failure twice and blame the step
// for what its child found.
//
// One finding per group per trace, first occurrence: three failed attempts of
// one retried call are one iteration failing once, not three.
func findings(rows []Row) []finding {
	byPath := make(map[string]Row, len(rows))
	covered := make(map[string]bool)
	for _, r := range rows {
		byPath[r.Path] = r
		if !isFailure(r.Outcome) {
			continue
		}
		for i := strings.LastIndex(r.Path, "."); i > 0; i = strings.LastIndex(r.Path[:i], ".") {
			covered[r.Path[:i]] = true
		}
	}

	out := make([]finding, 0, 2)
	seen := make(map[string]bool)
	for _, r := range rows {
		if !isFailure(r.Outcome) || covered[r.Path] {
			continue
		}
		st := stepOf(r, byPath)
		cause, label := causeOf(r, st)
		f := finding{step: st.Name, cause: cause, label: label, detail: detailOf(r, st), row: r}
		if seen[f.key()] {
			continue
		}
		seen[f.key()] = true
		out = append(out, f)
	}
	return out
}

// causeOf names why a span failed. The order is the point.
//
// A throttle is a throttle whatever else is true of it, so it is classified
// first and can never land in a generic error group (ADR 0006). A status the
// target refused is the cause even when an assertion is what noticed it —
// "checkout answered 503" is the finding an author acts on; "assert_status
// failed" is only how it surfaced. What is left is the failing span's own
// nature: a wrong answer, a missing value, or no answer at all.
func causeOf(r, step Row) (Cause, string) {
	status := statusOf(r, step)
	switch {
	case r.Outcome == span.OutcomeThrottled || step.Outcome == span.OutcomeThrottled || status == http.StatusTooManyRequests:
		return CauseThrottled, statusLabel(status)
	case status >= 400:
		return CauseStatus, statusLabel(status)
	case strings.HasPrefix(r.Name, "assert_"):
		return CauseAssertion, r.Name
	case r.Kind == KindLogic:
		return CauseExtraction, r.Name
	default:
		return fromDetail(detailOf(r, step))
	}
}

// transportCauses read the engine's recorded reason. A call that got no answer
// has no status to group by, and "the target was too slow" and "nothing was
// listening" are different findings with different fixes. Ordered: the first
// match wins, so the specific phrases precede the general ones.
var transportCauses = []struct {
	needle string
	cause  Cause
	label  string
}{
	{"context deadline exceeded", CauseTimeout, "deadline exceeded"},
	{"timeout exceeded", CauseTimeout, "client timeout"},
	{"i/o timeout", CauseTimeout, "i/o timeout"},
	{"timeout", CauseTimeout, "timeout"},
	{"connection refused", CauseConnection, "connection refused"},
	{"connection reset", CauseConnection, "connection reset"},
	{"no such host", CauseConnection, "no such host"},
	{"network is unreachable", CauseConnection, "network unreachable"},
	{"broken pipe", CauseConnection, "broken pipe"},
	{"certificate", CauseConnection, "certificate"},
	{"tls", CauseConnection, "tls handshake"},
	{"eof", CauseConnection, "unexpected eof"},
	{"host allow-list", CauseError, "host allow-list"},
}

func fromDetail(detail string) (Cause, string) {
	d := strings.ToLower(detail)
	for _, c := range transportCauses {
		if strings.Contains(d, c.needle) {
			return c.cause, c.label
		}
	}
	return CauseError, ""
}

// stepOf is the authored step a span belongs to: its depth-1 ancestor, since
// grouping by an assertion's own name would scatter one step's failures across
// every assertion it makes.
func stepOf(r Row, byPath map[string]Row) Row {
	parts := strings.Split(r.Path, ".")
	if len(parts) < 2 {
		return r
	}
	if st, ok := byPath[strings.Join(parts[:2], ".")]; ok {
		return st
	}
	return r
}

// detailOf is the reason recorded for a span, falling back to its step's: a
// transport failure marks the call leg but is recorded on the step around it.
func detailOf(r, step Row) string {
	if r.Payload != nil && r.Payload.Failure != "" {
		return r.Payload.Failure
	}
	if step.Payload != nil {
		return step.Payload.Failure
	}
	return ""
}

// statusOf is what the target answered on this span, or on its step — a failed
// assertion is a child of the call that carries the status.
func statusOf(r, step Row) int {
	if r.Payload != nil && r.Payload.Status != 0 {
		return r.Payload.Status
	}
	if step.Payload != nil {
		return step.Payload.Status
	}
	return 0
}

func statusLabel(status int) string {
	if status == 0 {
		return ""
	}
	return fmt.Sprintf("HTTP %d", status)
}

func isFailure(o span.Outcome) bool {
	return o == span.OutcomeFailed || o == span.OutcomeThrottled
}
