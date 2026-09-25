package parser_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/parser"
)

// usesFlow is a flow whose only step uses another file.
func usesFlow(name, stepID, target string) string {
	return fmt.Sprintf("flow: %s\nsteps:\n  - id: %s\n    use: %s\n", name, stepID, target)
}

const leafFlow = `flow: leaf
steps:
  - id: ping
    call: GET /ping
`

func parseEntry(t *testing.T, files map[string]string, entry string) (*parser.Result, error) {
	t.Helper()
	dir := writeFlows(t, files)
	return parser.ParseFlowFile(filepath.Join(dir, entry), nil)
}

func TestNestedUseLoadsEveryLevel(t *testing.T) {
	res, err := parseEntry(t, map[string]string{
		"a.flow.yaml": usesFlow("a", "auth", "b.flow.yaml"),
		"b.flow.yaml": usesFlow("b", "token", "c.flow.yaml"),
		"c.flow.yaml": leafFlow,
	}, "a.flow.yaml")
	if err != nil {
		t.Fatalf("A uses B uses C should parse, got:\n%v", err)
	}
	b := res.Scenario.Flows[0].Steps[0].Use.Flow
	c := b.Steps[0].Use.Flow
	if b.Name != "b" || c == nil || c.Name != "leaf" || c.Steps[0].ID != "ping" {
		t.Fatalf("want a → b → leaf loaded, got b=%+v c=%+v", b, c)
	}
}

func TestUseCycleIsAPreRunErrorNamingTheWholeCycle(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "a flow that uses itself",
			files: map[string]string{"a.flow.yaml": usesFlow("a", "again", "a.flow.yaml")},
			want:  "use cycle: a.flow.yaml → a.flow.yaml",
		},
		{
			name: "two flows that use each other",
			files: map[string]string{
				"a.flow.yaml": usesFlow("a", "auth", "b.flow.yaml"),
				"b.flow.yaml": usesFlow("b", "back", "a.flow.yaml"),
			},
			want: "use cycle: a.flow.yaml → b.flow.yaml → a.flow.yaml",
		},
		{
			name: "a cycle that does not include the entry flow",
			files: map[string]string{
				"a.flow.yaml": usesFlow("a", "auth", "b.flow.yaml"),
				"b.flow.yaml": usesFlow("b", "next", "c.flow.yaml"),
				"c.flow.yaml": usesFlow("c", "back", "b.flow.yaml"),
			},
			want: "use cycle: b.flow.yaml → c.flow.yaml → b.flow.yaml",
		},
		{
			name: "a cycle through a subdirectory",
			files: map[string]string{
				"a.flow.yaml":        usesFlow("a", "auth", "shared/b.flow.yaml"),
				"shared/b.flow.yaml": usesFlow("b", "back", "../a.flow.yaml"),
			},
			want: "use cycle: a.flow.yaml → shared/b.flow.yaml → a.flow.yaml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEntry(t, tc.files, "a.flow.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got: %v", tc.want, err)
			}
		})
	}
}

// chain builds n flows f0 → f1 → … → f(n-1) → leaf, so the entry flow reaches
// the leaf through n use steps.
func chain(n int) map[string]string {
	files := map[string]string{"leaf.flow.yaml": leafFlow}
	for i := 0; i < n; i++ {
		next := "leaf.flow.yaml"
		if i < n-1 {
			next = fmt.Sprintf("f%d.flow.yaml", i+1)
		}
		files[fmt.Sprintf("f%d.flow.yaml", i)] = usesFlow(fmt.Sprintf("f%d", i), "next", next)
	}
	return files
}

func TestUseDepthIsBounded(t *testing.T) {
	const limit = 8
	if _, err := parseEntry(t, chain(limit), "f0.flow.yaml"); err != nil {
		t.Fatalf("%d levels of use should parse, got:\n%v", limit, err)
	}
	_, err := parseEntry(t, chain(limit+1), "f0.flow.yaml")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("more than %d levels", limit)) {
		t.Fatalf("%d levels should fail naming the limit, got: %v", limit+1, err)
	}
}

func TestErrorInAUsedFlowNamesItsFileAndTheChain(t *testing.T) {
	_, err := parseEntry(t, map[string]string{
		"a.flow.yaml": usesFlow("a", "auth", "b.flow.yaml"),
		"b.flow.yaml": usesFlow("b", "token", "c.flow.yaml"),
		"c.flow.yaml": "flow: c\nsteps:\n  - id: bad\n    call: GET /x\n    extract: { t: nope }\n",
	}, "a.flow.yaml")
	if err == nil {
		t.Fatal("want a pre-run error, got none")
	}
	var line string
	for _, l := range strings.Split(err.Error(), "\n") {
		if strings.Contains(l, "c.flow.yaml") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("want an error in c.flow.yaml, got:\n%v", err)
	}
	if !strings.Contains(line, string(filepath.Separator)+"c.flow.yaml:5:") {
		t.Errorf("error should point at c.flow.yaml:5:col, got:\n%s", line)
	}
	if !strings.HasSuffix(line, "(used via a:auth → b:token)") {
		t.Errorf("error should end with the chain a:auth → b:token, got:\n%s", line)
	}
}

func TestErrorInAUsedFlowStepNamesTheChainWhenNamedLate(t *testing.T) {
	// `flow:` after `steps:` — the chain still carries the caller's name.
	_, err := parseEntry(t, map[string]string{
		"a.flow.yaml": "steps:\n  - id: auth\n    use: b.flow.yaml\nflow: a\n",
		"b.flow.yaml": "flow: b\nsteps:\n  - id: bad\n    call: nonsense\n",
	}, "a.flow.yaml")
	if err == nil || !strings.Contains(err.Error(), "(used via a:auth)") {
		t.Fatalf("want the chain to name flow a, got: %v", err)
	}
}

func TestSameFileUsedTwiceIsCompiledOnce(t *testing.T) {
	res, err := parseEntry(t, map[string]string{
		"a.flow.yaml":           "flow: a\nsteps:\n  - id: first\n    use: shared/leaf.flow.yaml\n  - id: second\n    use: ./shared/../shared/leaf.flow.yaml\n",
		"shared/leaf.flow.yaml": leafFlow,
	}, "a.flow.yaml")
	if err != nil {
		t.Fatalf("should parse, got:\n%v", err)
	}
	steps := res.Scenario.Flows[0].Steps
	if steps[0].Use.Flow != steps[1].Use.Flow {
		t.Error("both use steps should share one compiled flow")
	}
}

func TestErrorInAFileUsedTwiceIsReportedOnce(t *testing.T) {
	_, err := parseEntry(t, map[string]string{
		"a.flow.yaml":   "flow: a\nsteps:\n  - id: first\n    use: bad.flow.yaml\n  - id: second\n    use: bad.flow.yaml\n",
		"bad.flow.yaml": "flow: bad\nsteps:\n  - id: s\n    call: nonsense\n",
	}, "a.flow.yaml")
	if err == nil {
		t.Fatal("want an error")
	}
	if n := strings.Count(err.Error(), "bad.flow.yaml:"); n != 1 {
		t.Errorf("the child's error should appear once, appeared %d times:\n%v", n, err)
	}
}

func TestNestedUsedFlowProtoPathsStayRelativeToTheirFile(t *testing.T) {
	res, err := parseEntry(t, map[string]string{
		"a.flow.yaml":        usesFlow("a", "mid", "shared/b.flow.yaml"),
		"shared/b.flow.yaml": usesFlow("b", "pay", "pay/charge.flow.yaml"),
		"shared/pay/charge.flow.yaml": `flow: charge
steps:
  - id: charge
    grpc:
      proto: proto/pay.proto
      method: pay.Payments/Charge
      url: grpc://localhost:9090
`,
	}, "a.flow.yaml")
	if err != nil {
		t.Fatalf("should parse, got:\n%v", err)
	}
	g := res.Scenario.Flows[0].Steps[0].Use.Flow.Steps[0].Use.Flow.Steps[0].GRPC
	if want := filepath.Join("shared", "pay", "proto", "pay.proto"); g.Proto != want {
		t.Errorf("proto = %q, want %q", g.Proto, want)
	}
}
