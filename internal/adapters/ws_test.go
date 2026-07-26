package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/ir"
)

func wsResolver(vars map[string]string) adapters.Resolver {
	return func(ref string) (string, error) {
		v, ok := vars[ref]
		if !ok {
			return "", errors.New("no such reference: " + ref)
		}
		return v, nil
	}
}

// The handshake is an ordinary GET, which is what lets auth, the allow-list
// and the phase spans apply to it unchanged.
func TestBuildWSOpen(t *testing.T) {
	req, err := adapters.BuildWSOpen(&ir.WSSpec{
		URL:     "/feed/{{ room }}",
		Headers: map[string]string{"X-Client": "flowbench/{{ version }}"},
	}, wsResolver(map[string]string{"room": "eu-1", "version": "0.1"}))
	if err != nil {
		t.Fatalf("building handshake: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}
	if req.URL != "/feed/eu-1" {
		t.Errorf("url = %q", req.URL)
	}
	if got := req.Headers["X-Client"]; got != "flowbench/0.1" {
		t.Errorf("header = %q", got)
	}
}

// Frames are templated through the JSON-escaping resolver, so a value full of
// quotes and braces lands as data rather than as syntax.
func TestBuildWSFrameEscapesValues(t *testing.T) {
	frame, err := adapters.BuildWSFrame(&ir.WSSpec{
		Send: json.RawMessage(`{"op":"subscribe","note":"{{ note }}"}`),
	}, wsResolver(map[string]string{"note": `he said "stop", then {"op":"drop"}`}))
	if err != nil {
		t.Fatalf("building frame: %v", err)
	}
	var decoded struct {
		Op   string `json:"op"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatalf("frame is not valid JSON: %s (%v)", frame, err)
	}
	if decoded.Op != "subscribe" {
		t.Errorf("op = %q: the injected value rewrote the frame (%s)", decoded.Op, frame)
	}
	if !strings.Contains(decoded.Note, `"stop"`) {
		t.Errorf("note = %q, want the quotes preserved as data", decoded.Note)
	}
}

// A peer closing the conversation is not the transport breaking, and the code
// is what separates "try again later" from "I crashed".
func TestAsWSCloseError(t *testing.T) {
	closed := websocket.CloseError{Code: websocket.StatusTryAgainLater, Reason: "at capacity"}
	got, ok := adapters.AsWSCloseError(closed)
	if !ok {
		t.Fatal("a CloseError should be recognised as one")
	}
	if got.Code != adapters.StatusTryAgainLater {
		t.Errorf("code = %d, want %d", got.Code, adapters.StatusTryAgainLater)
	}
	if !strings.Contains(got.Error(), "try again later") || !strings.Contains(got.Error(), "at capacity") {
		t.Errorf("message = %q, want the code's name and the peer's reason", got.Error())
	}

	if _, ok := adapters.AsWSCloseError(errors.New("connection reset")); ok {
		t.Error("a transport error is not a close")
	}
}

// A handshake that never upgrades still answers, and the caller needs that
// answer to tell a 429 from a 401.
func TestDialWSReturnsTheRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		http.Error(w, "too many connections", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s := adapters.NewSession(adapters.SessionOptions{})
	sess, resp, sp, err := s.DialWS(context.Background(), "ws_open",
		&adapters.Request{Method: http.MethodGet, URL: srv.URL}, nil, time.Now())
	if err == nil {
		t.Fatal("the upgrade was refused; DialWS should report it")
	}
	if sess != nil {
		t.Error("no session should come back from a refused handshake")
	}
	if resp == nil || resp.Status != http.StatusTooManyRequests {
		t.Fatalf("handshake response = %+v, want the 429", resp)
	}
	if got := resp.Headers.Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After = %q, want it kept", got)
	}
	if sp == nil || sp.Name != "ws_open" {
		t.Errorf("span = %+v, want a failed ws_open span", sp)
	}
}
