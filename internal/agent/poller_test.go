package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPollCollectsSamplesOnInterval(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Millisecond)
	defer cancel()

	series := Poll(ctx, srv.Listener.Addr().String(), 10*time.Millisecond)
	if len(series) < 3 {
		t.Fatalf("got %d samples over ~55ms at 10ms interval, want at least 3", len(series))
	}
	for i, s := range series {
		if s.NumCPU < 1 {
			t.Errorf("sample %d: NumCPU = %d, want >= 1", i, s.NumCPU)
		}
	}
	// At should be non-decreasing and roughly track the ticker.
	for i := 1; i < len(series); i++ {
		if series[i].At < series[i-1].At {
			t.Errorf("At went backwards at sample %d: %v -> %v", i, series[i-1].At, series[i].At)
		}
	}
}

func TestPollZeroIntervalReturnsNilOnCtxDone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	series := Poll(ctx, "127.0.0.1:1", 0)
	if series != nil {
		t.Errorf("series = %v, want nil for a zero interval", series)
	}
}

// TestPollFailOpenAgainstUnreachableAddr is the poller-level version of
// issue #32's acceptance criterion: an agent that never answers (here,
// nothing listening at all) never blocks or panics the poller -- it just
// accumulates zero samples and returns cleanly when ctx ends.
func TestPollFailOpenAgainstUnreachableAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	done := make(chan []PolledSample, 1)
	go func() { done <- Poll(ctx, "127.0.0.1:1", 10*time.Millisecond) }()

	select {
	case series := <-done:
		if len(series) != 0 {
			t.Errorf("got %d samples against an unreachable address, want 0", len(series))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not return promptly against an unreachable address")
	}
}

// TestPollFailOpenAgainstAgentDyingMidRun kills the agent partway through
// polling and confirms Poll keeps running to ctx end rather than blocking or
// erroring, with only the samples collected before the kill.
func TestPollFailOpenAgainstAgentDyingMidRun(t *testing.T) {
	srv := httptest.NewServer(Handler())
	addr := srv.Listener.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	go func() {
		time.Sleep(25 * time.Millisecond)
		srv.Close() // the agent "dies"
	}()

	series := Poll(ctx, addr, 10*time.Millisecond)
	if len(series) == 0 {
		t.Fatal("expected at least one sample collected before the agent died")
	}
	if len(series) >= 8 {
		t.Errorf("got %d samples over 80ms at 10ms interval after an early kill; expected the kill to cut it short", len(series))
	}
}

func TestScrapeRejectsNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	_, ok := scrape(context.Background(), client, srv.URL)
	if ok {
		t.Error("scrape should reject a non-200 response")
	}
}

func TestScrapeRejectsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	_, ok := scrape(context.Background(), client, srv.URL)
	if ok {
		t.Error("scrape should reject malformed JSON")
	}
}

func TestScrapeDecodesValidSample(t *testing.T) {
	want := Sample{NumCPU: 4, LoadAvg1: 1.5}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	got, ok := scrape(context.Background(), client, srv.URL)
	if !ok {
		t.Fatal("scrape should succeed against a valid response")
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
