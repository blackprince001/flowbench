package report_test

import (
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/report"
)

// join reassembles one side of a row, so a test can assert that the pieces put
// the line back exactly as it went in — a diff that quietly normalizes its
// input would hide the change it exists to show.
func join(pieces []report.Piece) string {
	var b strings.Builder
	for _, p := range pieces {
		b.WriteString(p.Text)
	}
	return b.String()
}

func marked(pieces []report.Piece) string {
	var b strings.Builder
	for _, p := range pieces {
		if p.Changed {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func TestIdenticalTextIsReportedAsIdentical(t *testing.T) {
	rows, stat := report.Diff("refund_request", "refund_request")
	if !stat.Identical {
		t.Fatalf("identical completions should report Identical, got %+v", stat)
	}
	if len(rows) != 1 || rows[0].Kind != "same" {
		t.Fatalf("want one same row, got %+v", rows)
	}
	// The acceptance's "rerun at temperature 0 shows an empty diff" is this.
	if stat.Added+stat.Removed+stat.Changed != 0 {
		t.Fatalf("identical text counted changes: %+v", stat)
	}
}

func TestOneWordEditMarksOnlyThatWord(t *testing.T) {
	rows, stat := report.Diff(
		"This is a refund_request. The customer was charged twice.",
		"This is a billing_dispute. The customer was charged twice.")
	if stat.Changed != 1 || stat.Added != 0 || stat.Removed != 0 {
		t.Fatalf("want one changed line, got %+v", stat)
	}
	row := rows[0]
	if got := marked(row.Left); got != "refund_request." {
		t.Errorf("left mark = %q, want the changed word only", got)
	}
	if got := marked(row.Right); got != "billing_dispute." {
		t.Errorf("right mark = %q, want the changed word only", got)
	}
	// Word granularity must not cost fidelity: the pieces rejoin exactly.
	if join(row.Left) != "This is a refund_request. The customer was charged twice." {
		t.Errorf("left pieces do not rejoin into the original line: %q", join(row.Left))
	}
}

func TestAddedAndRemovedLinesKeepTheirSideAndNumber(t *testing.T) {
	rows, stat := report.Diff("one\ntwo", "one\ntwo\nthree")
	if stat.Added != 1 || stat.Removed != 0 || stat.Changed != 0 {
		t.Fatalf("want one added line, got %+v", stat)
	}
	last := rows[len(rows)-1]
	if last.Kind != "added" || last.LeftNo != 0 || last.RightNo != 3 {
		t.Fatalf("added row should have no left line number: %+v", last)
	}
	if join(last.Left) != "" || join(last.Right) != "three" {
		t.Fatalf("added row sides = %q / %q", join(last.Left), join(last.Right))
	}
}

func TestBlocksOfEditsPairUpRatherThanStacking(t *testing.T) {
	// Three lines replaced by three: the useful reading is three changed rows
	// with word marks, not three deletions followed by three insertions.
	rows, stat := report.Diff("a1\nb1\nc1", "a2\nb2\nc2")
	if stat.Changed != 3 || stat.Added != 0 || stat.Removed != 0 {
		t.Fatalf("want three paired rows, got %+v", stat)
	}
	for _, r := range rows {
		if r.LeftNo == 0 || r.RightNo == 0 {
			t.Fatalf("paired row is missing a side: %+v", r)
		}
	}
}

func TestJSONDiffNamesEachChangedPath(t *testing.T) {
	changes, ok := report.JSONDiff(
		`{"label":"refund","confidence":0.8,"tags":["billing"]}`,
		`{"label":"billing_dispute","confidence":0.8,"tags":["billing","duplicate"]}`)
	if !ok {
		t.Fatal("both sides are JSON; JSONDiff should say so")
	}
	got := map[string]report.JSONChange{}
	for _, c := range changes {
		got[c.Path] = c
	}
	if c := got["$.label"]; c.Kind != "changed" || c.Old != `"refund"` || c.New != `"billing_dispute"` {
		t.Errorf("$.label = %+v", c)
	}
	if c := got["$.tags[1]"]; c.Kind != "added" || c.New != `"duplicate"` {
		t.Errorf("$.tags[1] = %+v", c)
	}
	if _, unchanged := got["$.confidence"]; unchanged {
		t.Error("an unchanged field should not be reported")
	}
}

func TestJSONDiffDeclinesWhenOnlyOneSideIsJSON(t *testing.T) {
	if _, ok := report.JSONDiff(`{"label":"refund"}`, "refund"); ok {
		t.Fatal("a JSON side against prose is not a structural change")
	}
}

func TestPrettyReindentsOnlyWhenBothSidesAreJSON(t *testing.T) {
	a, b := report.Pretty(`{"role":"system"}`, `{"role":"user"}`)
	if !strings.Contains(a, "\n") || !strings.Contains(b, "\n") {
		t.Fatalf("both JSON sides should be re-indented: %q / %q", a, b)
	}
	// One-sided pretty-printing would manufacture a difference on every line.
	c, d := report.Pretty(`{"role":"system"}`, "plain text")
	if strings.Contains(c, "\n") || d != "plain text" {
		t.Fatalf("mixed pair should be left alone: %q / %q", c, d)
	}
}
