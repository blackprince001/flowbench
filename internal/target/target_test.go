package target_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
	"github.com/blackprince001/flowbench/internal/target"
)

// localTarget loads the config that `--target local` selects.
func localTarget(t *testing.T) *target.Target {
	t.Helper()
	path := target.Resolve("local", filepath.Join("..", "..", "tests", "targets"))
	cfg, err := parser.ParseTargetFile(path)
	if err != nil {
		t.Fatalf("load target: %v", err)
	}
	g, err := target.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func callStep(id, method, url string) ir.Step {
	return ir.Step{ID: id, Type: ir.StepCall, Call: &ir.CallSpec{Method: method, URL: url}}
}

func scenario(mode ir.Mode, steps ...ir.Step) *ir.Scenario {
	return &ir.Scenario{
		Name:    "s",
		Profile: ir.Profile{Mode: mode},
		Flows:   []ir.Flow{{Name: "f", Steps: steps}},
	}
}

func TestTargetSelectsConfigAndBaseURL(t *testing.T) {
	if got := localTarget(t).BaseURL(); got != "http://localhost:8080" {
		t.Errorf("base URL = %q", got)
	}
}

// TestExternalHostRefusedBeforeAnyRequest is the issue #8 acceptance: a flow
// calling outside the target's base URLs is refused pre-run.
func TestExternalHostRefusedBeforeAnyRequest(t *testing.T) {
	sc := scenario(ir.ModeIntegration,
		callStep("local_ok", "GET", "/health"),
		callStep("exfil", "POST", "http://evil.example/steal"),
	)
	err := localTarget(t).Check(sc)
	if err == nil {
		t.Fatal("reaching an external host must be refused")
	}
	if !strings.Contains(err.Error(), "evil.example") || !strings.Contains(err.Error(), "exfil") {
		t.Errorf("error should name the offending step and host: %v", err)
	}
}

func TestAllowedRelativeAndDynamicPass(t *testing.T) {
	sc := scenario(ir.ModeIntegration,
		callStep("rel", "GET", "/health"),
		callStep("abs", "GET", "http://localhost:8080/orders"),
		callStep("dyn", "GET", "http://{{ env.HOST }}/x"), // host known only at run time
	)
	if err := localTarget(t).Check(sc); err != nil {
		t.Errorf("allowed, relative, and dynamic-host calls should pass pre-run: %v", err)
	}
}

func TestAllowsRuntime(t *testing.T) {
	g := localTarget(t)
	cases := map[string]bool{
		"/relative/path":                true,
		"http://localhost:8080/orders":  true,
		"https://localhost:8080/orders": false, // scheme mismatch
		"http://evil.example/x":         false,
	}
	for u, want := range cases {
		got, err := g.Allows(u)
		if err != nil {
			t.Errorf("Allows(%q): %v", u, err)
			continue
		}
		if got != want {
			t.Errorf("Allows(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestDisallowedModeRefused(t *testing.T) {
	cfg := &ir.TargetConfig{
		Name:            "prod",
		BaseURLs:        []string{"http://localhost:8080"},
		DisallowedModes: []ir.Mode{ir.ModeStress},
	}
	g, err := target.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sc := scenario(ir.ModeStress, callStep("s1", "GET", "/x"))
	if err := g.Check(sc); err == nil || !strings.Contains(err.Error(), "stress") {
		t.Errorf("stress mode should be refused, got %v", err)
	}
}
