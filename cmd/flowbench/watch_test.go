package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
)

func TestLivePageServerRendersFiguresAndAbort(t *testing.T) {
	lr := &liveRun{scenario: "checkout", mode: "stress", target: "local-stub"}
	lr.update(executor.Progress{At: 2 * time.Second, ActiveVUs: 40, PeakVUs: 50, Completed: 120, Failed: 3, Throttled: 12})
	ls := newLiveServer(lr, func() {})

	rec := httptest.NewRecorder()
	ls.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()

	for _, want := range []string{
		"Watching checkout", "stress", "local-stub",
		`id="live-vus"`, "Abort run", `action="/abort"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("live page missing %q", want)
		}
	}
	// The figures are server-rendered, so they read before any script runs.
	if !strings.Contains(body, ">40<") {
		t.Error("the active-VU count should be server-rendered, not left to JS")
	}
}

func TestAbortCancelsTheRun(t *testing.T) {
	cancelled := false
	ls := newLiveServer(&liveRun{}, func() { cancelled = true })

	rec := httptest.NewRecorder()
	ls.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/abort", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("abort returned %d, want 202", rec.Code)
	}
	if !cancelled {
		t.Error("POST /abort is the one write path — it must cancel the run's context")
	}
}

func TestLiveStreamPushesSnapshotsThenDone(t *testing.T) {
	lr := &liveRun{}
	lr.update(executor.Progress{At: time.Second, ActiveVUs: 10, PeakVUs: 10, Completed: 50})
	ls := newLiveServer(lr, func() {})

	srv := httptest.NewServer(ls)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/live/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	// The first frame carries a JSON snapshot the live.js client parses.
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if !strings.HasPrefix(line, "data:") || !strings.Contains(line, `"vus":10`) {
		t.Errorf("first frame = %q, want a data: snapshot", line)
	}
	cancel()
	resp.Body.Close()

	// Once the run is stored, a subscriber gets the terminating event with the URL
	// to follow into the completed run.
	ls.done.Store(true)
	ls.runURL = "/p/x/runs/y/dashboard"
	rec := httptest.NewRecorder()
	ls.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/live/stream", nil))
	if out := rec.Body.String(); !strings.Contains(out, "event: done") || !strings.Contains(out, ls.runURL) {
		t.Errorf("done stream = %q, want a done event carrying the run URL", out)
	}
}
