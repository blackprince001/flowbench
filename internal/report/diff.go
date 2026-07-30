package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The diff engine behind the prompt view (issue #45). Built rather than
// adopted, for the reason ADR 0014 gives for the flame graph and the
// waterfall: the input is two strings the run store already holds, the output
// is server-rendered HTML sharing this stylesheet, and a JavaScript diff
// component would be a dependency, a build step and a second visual vocabulary
// for the sake of an algorithm that is a page long.
//
// Two granularities, because one is not enough to read a completion by. Lines
// pair the two sides so they can sit beside each other; within a paired line,
// words are diffed again so a one-word edit reads as one word rather than as a
// whole paragraph rewritten. A completion is usually prose, and prose is where
// line-only diffs are at their worst.

// diffLimit bounds the quadratic table. Beyond it the two sides are reported as
// wholly replaced rather than aligned — an honest coarse answer instead of a
// page that stops responding on a large capture.
const diffLimit = 3000

// DiffRow is one row of a diff: a line from each side, or one side and a gap.
// Kind is what happened to it, and the pieces carry the word-level detail for
// a row that changed.
type DiffRow struct {
	Kind  string // same | changed | added | removed
	Left  []Piece
	Right []Piece
	// LeftNo/RightNo are 1-based line numbers, zero where that side has no
	// line — the gap opposite an insertion.
	LeftNo  int
	RightNo int
}

// Piece is a run of text within a line, marked when it is part of what
// changed. A row with a single unmarked piece is a line that only moved.
type Piece struct {
	Text    string
	Changed bool
}

// DiffStat counts what a diff found, so a reader knows the size of a change
// before reading it.
type DiffStat struct {
	Added     int
	Removed   int
	Changed   int
	Identical bool
}

// Diff aligns two texts line by line and marks the words that differ within
// each aligned pair.
func Diff(left, right string) ([]DiffRow, DiffStat) {
	a, b := splitLines(left), splitLines(right)
	if left == right {
		rows := make([]DiffRow, 0, len(a))
		for i, line := range a {
			rows = append(rows, DiffRow{
				Kind:  "same",
				Left:  []Piece{{Text: line}},
				Right: []Piece{{Text: line}},
				//nolint:gosec // line numbers, bounded by the split above
				LeftNo: i + 1, RightNo: i + 1,
			})
		}
		return rows, DiffStat{Identical: true}
	}
	if len(a) > diffLimit || len(b) > diffLimit {
		return coarse(a, b), DiffStat{Added: len(b), Removed: len(a)}
	}

	var rows []DiffRow
	var stat DiffStat
	ln, rn := 0, 0
	// Each hunk is a run of equal lines or a run of removals and insertions;
	// pairing the two runs positionally is what turns "3 gone, 3 arrived" into
	// three changed lines a reader can compare word by word.
	for _, h := range hunks(a, b) {
		switch h.kind {
		case "same":
			for _, line := range h.left {
				ln, rn = ln+1, rn+1
				rows = append(rows, DiffRow{Kind: "same", Left: []Piece{{Text: line}}, Right: []Piece{{Text: line}}, LeftNo: ln, RightNo: rn})
			}
		default:
			paired := min(len(h.left), len(h.right))
			for i := 0; i < paired; i++ {
				ln, rn = ln+1, rn+1
				lp, rp := words(h.left[i], h.right[i])
				rows = append(rows, DiffRow{Kind: "changed", Left: lp, Right: rp, LeftNo: ln, RightNo: rn})
				stat.Changed++
			}
			for _, line := range h.left[paired:] {
				ln++
				rows = append(rows, DiffRow{Kind: "removed", Left: []Piece{{Text: line, Changed: true}}, LeftNo: ln})
				stat.Removed++
			}
			for _, line := range h.right[paired:] {
				rn++
				rows = append(rows, DiffRow{Kind: "added", Right: []Piece{{Text: line, Changed: true}}, RightNo: rn})
				stat.Added++
			}
		}
	}
	return rows, stat
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

type hunk struct {
	kind        string // same | change
	left, right []string
}

// hunks walks the LCS backtrace into runs, so a block of consecutive edits
// arrives as one hunk rather than as interleaved single lines.
func hunks(a, b []string) []hunk {
	var out []hunk
	add := func(kind, left, right string, hasLeft, hasRight bool) {
		if n := len(out); n > 0 && out[n-1].kind == kind {
			if hasLeft {
				out[n-1].left = append(out[n-1].left, left)
			}
			if hasRight {
				out[n-1].right = append(out[n-1].right, right)
			}
			return
		}
		h := hunk{kind: kind}
		if hasLeft {
			h.left = append(h.left, left)
		}
		if hasRight {
			h.right = append(h.right, right)
		}
		out = append(out, h)
	}

	table := lcs(a, b)
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			add("same", a[i], b[j], true, true)
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			add("change", a[i], "", true, false)
			i++
		default:
			add("change", "", b[j], false, true)
			j++
		}
	}
	for ; i < len(a); i++ {
		add("change", a[i], "", true, false)
	}
	for ; j < len(b); j++ {
		add("change", "", b[j], false, true)
	}
	return out
}

// lcs is the longest-common-subsequence length table, computed from the end so
// the forward walk above can read it directly.
func lcs(a, b []string) [][]int {
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	return table
}

// words diffs one aligned pair at word granularity. The pieces always rejoin
// into the original line: a diff that silently normalized spacing would hide a
// change it exists to show.
func words(left, right string) ([]Piece, []Piece) {
	a, b := tokenize(left), tokenize(right)
	if len(a) > diffLimit || len(b) > diffLimit {
		return []Piece{{Text: left, Changed: true}}, []Piece{{Text: right, Changed: true}}
	}
	table := lcs(a, b)

	var lp, rp []Piece
	push := func(dst *[]Piece, text string, changed bool) {
		if n := len(*dst); n > 0 && (*dst)[n-1].Changed == changed {
			(*dst)[n-1].Text += text
			return
		}
		*dst = append(*dst, Piece{Text: text, Changed: changed})
	}

	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			push(&lp, a[i], false)
			push(&rp, b[j], false)
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			push(&lp, a[i], true)
			i++
		default:
			push(&rp, b[j], true)
			j++
		}
	}
	for ; i < len(a); i++ {
		push(&lp, a[i], true)
	}
	for ; j < len(b); j++ {
		push(&rp, b[j], true)
	}
	return lp, rp
}

// tokenize splits a line into alternating runs of word and whitespace, with
// the whitespace its own token rather than riding on a neighbour. That is what
// keeps a highlight tight to the word: attached to either side, a one-word
// edit would light up the space before or after it as well, and an indented
// line would look changed to its margin.
func tokenize(s string) []string {
	var out []string
	start := 0
	first, prevSpace := true, false
	for i, r := range s {
		space := isSpace(r)
		if first {
			first, prevSpace = false, space
			continue
		}
		if space != prevSpace {
			out = append(out, s[start:i])
			start, prevSpace = i, space
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' }

func coarse(a, b []string) []DiffRow {
	rows := make([]DiffRow, 0, len(a)+len(b))
	for i, line := range a {
		rows = append(rows, DiffRow{Kind: "removed", Left: []Piece{{Text: line, Changed: true}}, LeftNo: i + 1})
	}
	for i, line := range b {
		rows = append(rows, DiffRow{Kind: "added", Right: []Piece{{Text: line, Changed: true}}, RightNo: i + 1})
	}
	return rows
}

// Pretty re-indents a pair for display, and only when *both* sides are JSON:
// a chat prompt is recorded as one line of JSON several hundred characters
// long, and a line diff over one line can only say "this line changed". Broken
// into fields it becomes a diff with something to align.
//
// Both or neither, because re-indenting one side alone would manufacture a
// difference on every line of a comparison where only the other side is JSON.
// The stored value is untouched; this is how it is shown.
func Pretty(a, b string) (string, string) {
	pa, aok := prettyJSON(a)
	pb, bok := prettyJSON(b)
	if !aok || !bok {
		return a, b
	}
	return pa, pb
}

// PrettyOne re-indents a single value when it is JSON — for a panel showing one
// side, where there is no pair to keep consistent.
func PrettyOne(s string) string {
	if p, ok := prettyJSON(s); ok {
		return p
	}
	return s
}

func prettyJSON(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return s, false // a bare string or number is already its own best form
	}
	var v any
	if json.Unmarshal([]byte(trimmed), &v) != nil {
		return s, false
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s, false
	}
	return string(out), true
}

// JSONChange is one difference between two JSON documents, addressed by the
// path folding and extraction already speak (`$.choices[0].role`).
type JSONChange struct {
	Path string
	Kind string // added | removed | changed
	Old  string
	New  string
}

// JSONDiff compares two completions structurally, and reports whether it could:
// a structural diff is only offered when both sides really are JSON, since a
// model that answered with prose on one side has not changed a field, it has
// changed everything. The path form matches the JSONPath subset (ADR 0011), so
// a difference names a place an assertion could be written about.
func JSONDiff(left, right string) ([]JSONChange, bool) {
	var a, b any
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil {
		return nil, false
	}
	var out []JSONChange
	walkJSON("$", a, b, &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, true
}

func walkJSON(path string, a, b any, out *[]JSONChange) {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			av, aok := am[k]
			bv, bok := bm[k]
			child := path + "." + k
			switch {
			case !aok:
				*out = append(*out, JSONChange{Path: child, Kind: "added", New: scalar(bv)})
			case !bok:
				*out = append(*out, JSONChange{Path: child, Kind: "removed", Old: scalar(av)})
			default:
				walkJSON(child, av, bv, out)
			}
		}
		return
	}

	as, aIsArr := a.([]any)
	bs, bIsArr := b.([]any)
	if aIsArr && bIsArr {
		for i := 0; i < max(len(as), len(bs)); i++ {
			child := fmt.Sprintf("%s[%d]", path, i)
			switch {
			case i >= len(as):
				*out = append(*out, JSONChange{Path: child, Kind: "added", New: scalar(bs[i])})
			case i >= len(bs):
				*out = append(*out, JSONChange{Path: child, Kind: "removed", Old: scalar(as[i])})
			default:
				walkJSON(child, as[i], bs[i], out)
			}
		}
		return
	}

	if scalar(a) != scalar(b) {
		*out = append(*out, JSONChange{Path: path, Kind: "changed", Old: scalar(a), New: scalar(b)})
	}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scalar renders a JSON value for display: compact, and quoted where the
// quoting is what distinguishes it (the string "1" from the number 1).
func scalar(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
