package executor_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

func eachPayload(root *span.Span, fn func(*span.Payload)) {
	if root.Payload != nil {
		fn(root.Payload)
	}
	for _, c := range root.Children {
		eachPayload(c, fn)
	}
}

// TestCaptureKeepsEveryFailure checks the capture policy retains every failed
// iteration's trace, with its response body captured.
func TestCaptureKeepsEveryFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Millisecond) // pace it: keep failures under the safety ceiling
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "hit", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall,
		Call:   &ir.CallSpec{Method: "GET", URL: "/"},
		Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(200)}},
	}}}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, 8, 150*time.Millisecond),
		Flows:    []ir.Flow{flow},
		BaseURL:  srv.URL,
		Metrics:  -1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failed() == 0 || res.Failed() != len(res.Samples) {
		t.Fatalf("want every flow-run failed, got %d of %d", res.Failed(), len(res.Samples))
	}
	if len(res.Traces) != res.Failed() {
		t.Fatalf("every failure must be captured: %d traces, %d failures", len(res.Traces), res.Failed())
	}
	captured := false
	eachPayload(res.Traces[0], func(p *span.Payload) {
		if strings.Contains(p.Response, "boom") {
			captured = true
		}
	})
	if !captured {
		t.Error("a failed trace should carry the captured response body")
	}
}

// TestCaptureSamplesSuccesses checks successful traces are kept at the sample
// rate — one of every SampleEvery.
func TestCaptureSamplesSuccesses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "hit", Steps: []ir.Step{{
		ID: "call", Type: ir.StepCall, Call: &ir.CallSpec{Method: "GET", URL: "/"},
	}}}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule:    holdSchedule(ir.ModeLoad, 16, 250*time.Millisecond),
		Flows:       []ir.Flow{flow},
		BaseURL:     srv.URL,
		Metrics:     -1,
		SampleEvery: 10,
		MaxTraces:   1_000_000,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	total := len(res.Samples)
	if res.Failed() != 0 || total < 100 {
		t.Fatalf("want a clean run with many successes, got %d failed of %d", res.Failed(), total)
	}
	want := (total + 9) / 10 // ceil(total/10): kept at n = 1, 11, 21, ...
	if len(res.Traces) != want {
		t.Fatalf("sample rate off: kept %d of %d successes, want %d (1 in 10)", len(res.Traces), total, want)
	}
}

// TestCaptureRedactsEnvSecrets is the issue #19 redaction acceptance: an
// env-sourced value injected into a request must not appear in any stored
// artifact, in the request or an echoing response.
func TestCaptureRedactsEnvSecrets(t *testing.T) {
	const secret = "s3cr3t-token-value"
	t.Setenv("FLOWBENCH_TEST_SECRET", secret)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write(body) // echo the request, secret and all
	}))
	defer srv.Close()

	flow := ir.Flow{Name: "login", Steps: []ir.Step{{
		ID: "auth", Type: ir.StepCall,
		Call: &ir.CallSpec{
			Method: "POST", URL: "/login",
			Body: []byte(`{"password":"{{ env.FLOWBENCH_TEST_SECRET }}"}`),
		},
	}}}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule:    holdSchedule(ir.ModeLoad, 4, 150*time.Millisecond),
		Flows:       []ir.Flow{flow},
		BaseURL:     srv.URL,
		Metrics:     -1,
		SampleEvery: 1, // keep all so every payload is finalized and checked
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Traces) == 0 {
		t.Fatal("no traces captured")
	}

	payloads := 0
	for _, tr := range res.Traces {
		eachPayload(tr, func(p *span.Payload) {
			payloads++
			if strings.Contains(p.Request, secret) || strings.Contains(p.Response, secret) {
				t.Errorf("env secret leaked into a captured payload:\nreq=%q\nresp=%q", p.Request, p.Response)
			}
		})
	}
	if payloads == 0 {
		t.Fatal("expected captured payloads to redact")
	}
}
