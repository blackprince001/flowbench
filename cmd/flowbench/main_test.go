package main

import (
	"strings"
	"testing"
)

func TestVersionCommandPrintsBuildIdentity(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"version"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "flowbench ") {
		t.Errorf("stdout = %q, want a line starting with %q", stdout.String(), "flowbench ")
	}
}

func TestUnknownCommandFailsWithUsage(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"frobnicate"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr = %q, want it to name the unknown command", stderr.String())
	}
}

func TestNoArgsFailsWithUsage(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := run(&stdout, &stderr, nil); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}
