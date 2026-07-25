package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
)

func TestTargetCommandPrintsResolvedConfig(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"target", "local", "--targets-dir", "../../tests/targets"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr.String())
	}

	var cfg ir.TargetConfig
	if err := json.Unmarshal([]byte(stdout.String()), &cfg); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	if cfg.Name != "local" || len(cfg.BaseURLs) != 1 || cfg.BaseURLs[0] != "http://localhost:8080" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg.MaxVUs != 200 || cfg.MaxRPS != 500 {
		t.Errorf("ceilings not carried through: %+v", cfg)
	}
}

func TestTargetCommandFlagBeforeOrAfterName(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"target", "--targets-dir", "../../tests/targets", "local"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"name": "local"`) {
		t.Errorf("stdout = %q, want it to contain the resolved name", stdout.String())
	}
}

func TestTargetCommandUnknownNameFails(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"target", "nope", "--targets-dir", "../../tests/targets"})
	if code != exitPreRun {
		t.Fatalf("exit code = %d, want %d", code, exitPreRun)
	}
	if !strings.Contains(stderr.String(), "nope") {
		t.Errorf("stderr = %q, want it to name the missing target", stderr.String())
	}
}

func TestTargetCommandDefaultsLocalWithoutFile(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"target", "local", "--targets-dir", t.TempDir()})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr.String())
	}
	var cfg ir.TargetConfig
	if err := json.Unmarshal([]byte(stdout.String()), &cfg); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	if cfg.Name != "local" || len(cfg.BaseURLs) != 1 || cfg.BaseURLs[0] != "http://localhost:8080" {
		t.Errorf("unexpected implicit-local config: %+v", cfg)
	}
}

func TestTargetCommandMissingNameFailsWithUsage(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"target"})
	if code != exitPreRun {
		t.Fatalf("exit code = %d, want %d", code, exitPreRun)
	}
	if !strings.Contains(stderr.String(), "usage: flowbench target") {
		t.Errorf("stderr = %q, want the usage line", stderr.String())
	}
}
