package target_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A WebSocket connection opens with an HTTP request to the same host, so a
// target that already allows the host allows a socket on it — pre-run and at
// request time — without having to list it twice.
func TestWebSocketSchemesFoldIntoTheirHTTPOrigin(t *testing.T) {
	g := localTarget(t)
	cases := map[string]bool{
		"ws://localhost:8080/feed":  true,
		"/feed":                     true,
		"wss://localhost:8080/feed": false, // wss is https, which the target does not allow
		"ws://evil.example/feed":    false,
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

	allowed := scenario(ir.ModeIntegration, wsStep("open", "ws://localhost:8080/feed"))
	if err := g.Check(allowed); err != nil {
		t.Errorf("a ws:// URL on an allowed host should pass pre-run: %v", err)
	}
	refused := scenario(ir.ModeIntegration, wsStep("open", "ws://evil.example/feed"))
	if err := g.Check(refused); err == nil {
		t.Error("a ws:// URL outside the allow-list should be refused pre-run")
	}
}

func wsStep(id, url string) ir.Step {
	return ir.Step{ID: id, Type: ir.StepWS, WS: &ir.WSSpec{URL: url}}
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

// A call budget belongs to the target: without one, a host that accepts a
// connection and then says nothing holds a VU for the adapter's default.
func TestRequestTimeoutIsCarried(t *testing.T) {
	tg, err := target.New(&ir.TargetConfig{
		Name:           "svc",
		BaseURLs:       []string{"http://localhost:8080"},
		RequestTimeout: ir.Duration(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := tg.RequestTimeout(); got != 2*time.Second {
		t.Errorf("request timeout = %s, want 2s", got)
	}

	// Unset leaves the adapter's own default in place rather than forcing zero.
	plain, err := target.New(&ir.TargetConfig{Name: "svc", BaseURLs: []string{"http://localhost:8080"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.RequestTimeout(); got != 0 {
		t.Errorf("an undeclared timeout should stay zero, got %s", got)
	}
}
