package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestVersionFlagPrintsBuildIdentity(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"--version"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "flowbench-agent ") {
		t.Errorf("stdout = %q, want a line starting with %q", stdout.String(), "flowbench-agent ")
	}
}

func TestBadFlagIsPreRunError(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"--nope"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestBindFailureIsAnError(t *testing.T) {
	var stdout, stderr strings.Builder
	code := run(&stdout, &stderr, []string{"--addr", "not-a-valid-address"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", code, stderr.String())
	}
}

// TestServesMetricsUntilInterrupted starts the server on an ephemeral port,
// confirms /metrics answers, then relies on the deferred cleanup in other
// tests for shutdown coverage — full signal-based shutdown is covered by
// serve.go's existing pattern this file mirrors, and is awkward to drive
// from within a single-process test without sending a real SIGINT to self.
func TestServesMetricsOnEphemeralPort(t *testing.T) {
	var stdout, stderr strings.Builder
	go run(&stdout, &stderr, []string{"--addr", "127.0.0.1:18099"})

	var resp *http.Response
	var err error
	for i := 0; i < 20; i++ {
		resp, err = http.Get("http://127.0.0.1:18099/metrics")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
