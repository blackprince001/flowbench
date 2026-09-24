package parser_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
)

const loginFlow = `flow: login
inputs:
  email: "{{ env.SHOP_USER }}"
  password:
steps:
  - id: login
    call: POST /auth/login
    body: { email: "{{ inputs.email }}", password: "{{ inputs.password }}" }
    extract: { token: $.data.access_token }
outputs: [token]
`

// writeFlows writes name → content into a fresh directory and returns it.
func writeFlows(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestUseStepLoadsTheUsedFlow(t *testing.T) {
	dir := writeFlows(t, map[string]string{
		"shared/login.flow.yaml": loginFlow,
		"checkout.flow.yaml": `flow: checkout
data: users.csv
steps:
  - id: auth
    use: ./shared/login.flow.yaml
    with: { password: "{{ user.password }}" }
  - id: create_order
    call: POST /orders
    headers: { Authorization: "Bearer {{ auth.token }}" }
`,
	})

	res, err := parser.ParseFlowFile(filepath.Join(dir, "checkout.flow.yaml"), nil)
	if err != nil {
		t.Fatalf("should parse, got:\n%v", err)
	}
	auth := res.Scenario.Flows[0].Steps[0]
	if auth.Type != ir.StepUse || auth.Use == nil || auth.Use.Flow == nil {
		t.Fatalf("auth step = %+v, want a use step with its flow loaded", auth)
	}
	if auth.Use.Path != "./shared/login.flow.yaml" || auth.Use.With["password"] != "{{ user.password }}" {
		t.Errorf("use spec = %+v", auth.Use)
	}
	used := auth.Use.Flow
	if used.Name != "login" || len(used.Steps) != 1 || len(used.Outputs) != 1 || used.Outputs[0] != "token" {
		t.Errorf("used flow = %+v", used)
	}
	if len(used.Inputs) != 2 || used.Inputs[0].Default == nil || used.Inputs[1].Default != nil {
		t.Errorf("inputs = %+v, want email with a default and password required", used.Inputs)
	}
	if p := used.Steps[0].Pos; p == nil || !strings.HasSuffix(p.File, "login.flow.yaml") || p.Line != 6 {
		t.Errorf("used step position = %v, want login.flow.yaml:6", p)
	}
}

func TestUsedFlowStillRunsAlone(t *testing.T) {
	dir := writeFlows(t, map[string]string{"login.flow.yaml": strings.Replace(loginFlow, "  password:\n", "  password: \"{{ env.SHOP_PASS }}\"\n", 1)})
	if _, err := parser.ParseFlowFile(filepath.Join(dir, "login.flow.yaml"), nil); err != nil {
		t.Fatalf("a flow with inputs and outputs should parse on its own, got:\n%v", err)
	}
}

// TestUseStepPreRunErrors is the contract from #102: every broken reference
// between a flow and the flow it uses fails before the run, pointing at a
// file and line.
func TestUseStepPreRunErrors(t *testing.T) {
	caller := func(steps string) string { return "flow: checkout\ndata: users.csv\nsteps:\n" + steps }
	useLogin := "  - id: auth\n    use: login.flow.yaml\n    with: { password: pw }\n"

	cases := []struct {
		name   string
		files  map[string]string
		wantAt string // file:line the error must name
		want   string
	}{
		{
			name:   "missing required input",
			files:  map[string]string{"c.flow.yaml": caller("  - id: auth\n    use: login.flow.yaml\n")},
			wantAt: "c.flow.yaml:4",
			want:   `requires input "password"`,
		},
		{
			name:   "unknown with key",
			files:  map[string]string{"c.flow.yaml": caller("  - id: auth\n    use: login.flow.yaml\n    with: { password: pw, pasword: pw }\n")},
			wantAt: "c.flow.yaml:4",
			want:   `with sets "pasword"`,
		},
		{
			name:   "unknown output",
			files:  map[string]string{"c.flow.yaml": caller(useLogin + "  - id: next\n    call: GET /x/{{ auth.tokn }}\n")},
			wantAt: "c.flow.yaml:7",
			want:   `declares no output "tokn"`,
		},
		{
			name: "output nothing extracts",
			files: map[string]string{
				"login.flow.yaml": strings.Replace(loginFlow, "outputs: [token]", "outputs: [token, refresh]", 1),
				"c.flow.yaml":     caller(useLogin),
			},
			wantAt: "login.flow.yaml:1",
			want:   `output "refresh" is not extracted`,
		},
		{
			name:   "use step id clashes with the data pool",
			files:  map[string]string{"c.flow.yaml": caller(strings.Replace(useLogin, "id: auth", "id: user", 1))},
			wantAt: "c.flow.yaml:4",
			want:   `clashes with an existing variable root`,
		},
		{
			name:   "use step id clashes with env",
			files:  map[string]string{"c.flow.yaml": caller(strings.Replace(useLogin, "id: auth", "id: env", 1))},
			wantAt: "c.flow.yaml:4",
			want:   `clashes with an existing variable root`,
		},
		{
			name:   "extract reuses a use step id",
			files:  map[string]string{"c.flow.yaml": caller(useLogin + "  - id: next\n    call: GET /x\n    extract: { auth: $.a }\n")},
			wantAt: "c.flow.yaml:7",
			want:   `extracts "auth", which already names`,
		},
		{
			name:   "path does not exist",
			files:  map[string]string{"c.flow.yaml": caller("  - id: auth\n    use: nope.flow.yaml\n")},
			wantAt: "c.flow.yaml:5",
			want:   `cannot read`,
		},
		{
			name:   "absolute path",
			files:  map[string]string{"c.flow.yaml": caller("  - id: auth\n    use: /etc/login.flow.yaml\n")},
			wantAt: "c.flow.yaml:5",
			want:   `must be relative`,
		},
		{
			name:   "not a flow file",
			files:  map[string]string{"c.flow.yaml": caller("  - id: auth\n    use: login.yaml\n")},
			wantAt: "c.flow.yaml:5",
			want:   `must name a .flow.yaml file`,
		},
		{
			name: "used flow reads a caller variable it was not passed",
			files: map[string]string{
				"login.flow.yaml": strings.Replace(loginFlow, "/auth/login", "/auth/login?t={{ token }}", 1),
				"c.flow.yaml":     caller(useLogin),
			},
			wantAt: "login.flow.yaml:6",
			want:   `template {{ token }} has no upstream source`,
		},
		{
			name: "used flow references an undeclared input",
			files: map[string]string{
				"login.flow.yaml": strings.Replace(loginFlow, "inputs.email", "inputs.mail", 1),
				"c.flow.yaml":     caller(useLogin),
			},
			wantAt: "login.flow.yaml:6",
			want:   `declares no input "mail"`,
		},
		{
			name: "used flow binds a data pool",
			files: map[string]string{
				"login.flow.yaml": "data: users.csv\n" + loginFlow,
				"c.flow.yaml":     caller(useLogin),
			},
			wantAt: "c.flow.yaml:4",
			want:   `takes its values through inputs, not a data pool`,
		},
		{
			name: "nested use",
			files: map[string]string{
				"login.flow.yaml": strings.Replace(loginFlow, "outputs:", "  - id: deeper\n    use: other.flow.yaml\noutputs:", 1),
				"c.flow.yaml":     caller(useLogin),
			},
			wantAt: "login.flow.yaml:11",
			want:   `nested use is #103`,
		},
		{
			name: "input default reaches past the env",
			files: map[string]string{
				"login.flow.yaml": strings.Replace(loginFlow, "{{ env.SHOP_USER }}", "{{ user.email }}", 1),
				"c.flow.yaml":     caller(useLogin),
			},
			wantAt: "login.flow.yaml:1",
			want:   `can only reference the env`,
		},
		{
			name:   "with on a step that uses nothing",
			files:  map[string]string{"c.flow.yaml": caller("  - id: a\n    call: GET /x\n    with: { a: b }\n")},
			wantAt: "c.flow.yaml:6",
			want:   `belongs on a use step`,
		},
		{
			name:   "assert on a use step",
			files:  map[string]string{"c.flow.yaml": caller(useLogin + "    assert: [ status == 200 ]\n")},
			wantAt: "c.flow.yaml:4",
			want:   `belong inside the used flow`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"login.flow.yaml": loginFlow}
			for k, v := range tc.files {
				files[k] = v
			}
			dir := writeFlows(t, files)
			_, err := parser.ParseFlowFile(filepath.Join(dir, "c.flow.yaml"), nil)
			if err == nil {
				t.Fatal("want a pre-run error, got none")
			}
			var line string
			for _, l := range strings.Split(err.Error(), "\n") {
				if strings.Contains(l, tc.want) {
					line = l
					break
				}
			}
			if line == "" {
				t.Fatalf("want an error containing %q, got:\n%v", tc.want, err)
			}
			if !strings.Contains(line, string(filepath.Separator)+tc.wantAt+":") {
				t.Errorf("error should point at %s, got:\n%s", tc.wantAt, line)
			}
		})
	}
}

func TestUsedFlowProtoPathsStayRelativeToTheirFile(t *testing.T) {
	dir := writeFlows(t, map[string]string{
		"shared/charge.flow.yaml": `flow: charge
steps:
  - id: charge
    grpc:
      proto: proto/pay.proto
      import_paths: [proto]
      method: pay.Payments/Charge
      url: grpc://localhost:9090
`,
		"c.flow.yaml": "flow: c\nsteps:\n  - id: pay\n    use: shared/charge.flow.yaml\n",
	})
	res, err := parser.ParseFlowFile(filepath.Join(dir, "c.flow.yaml"), nil)
	if err != nil {
		t.Fatalf("should parse, got:\n%v", err)
	}
	g := res.Scenario.Flows[0].Steps[0].Use.Flow.Steps[0].GRPC
	want := filepath.Join("shared", "proto", "pay.proto")
	if g.Proto != want || g.ImportPaths[0] != filepath.Join("shared", "proto") {
		t.Errorf("proto = %q imports = %v, want them under shared/", g.Proto, g.ImportPaths)
	}
}
