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

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

func phases(leg *span.Span) map[string]*span.Span {
	m := map[string]*span.Span{}
	for _, c := range leg.Children {
		m[c.Name] = c
	}
	return m
}

func TestCallEmitsPerPhaseSpanTree(t *testing.T) {
	const serverDelay = 30 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(serverDelay)
		w.Write([]byte(`{"data":{"access_token":"tok-123"}}`))
	}))
	defer srv.Close()

	sess := adapters.NewSession(adapters.SessionOptions{})
	anchor := time.Now()
	resp, step, err := sess.Do(context.Background(), "login",
		&adapters.Request{Method: "POST", URL: srv.URL + "/auth/login"}, anchor)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if resp.Status != 200 || !strings.Contains(string(resp.Body), "tok-123") {
		t.Errorf("response = %d %s", resp.Status, resp.Body)
	}

	if step.Name != "login" || step.Outcome != span.OutcomeOK || step.Duration <= 0 {
		t.Errorf("step span = %+v", step)
	}
	if len(step.Children) != 1 || step.Children[0].Name != "http_call" {
		t.Fatalf("want one http_call leg, got %+v", step.Children)
	}
	leg := step.Children[0]
	ph := phases(leg)

	for _, name := range []string{"connect", "ttfb", "transfer"} {
		if ph[name] == nil {
			t.Fatalf("missing %q phase; leg children = %+v", name, leg.Children)
		}
	}
	if ph["tls"] != nil {
		t.Error("plain http should not have a tls phase")
	}

	if ttfb := ph["ttfb"].Duration; ttfb < serverDelay-10*time.Millisecond {
		t.Errorf("ttfb = %v, want at least ~%v (server sleeps before responding)", ttfb, serverDelay)
	}
	if ph["connect"].Start > ph["ttfb"].Start || ph["ttfb"].Start > ph["transfer"].Start {
		t.Errorf("phases out of causal order: connect@%v ttfb@%v transfer@%v",
			ph["connect"].Start, ph["ttfb"].Start, ph["transfer"].Start)
	}
	if leg.Duration > step.Duration {
		t.Errorf("leg (%v) cannot outlast its step (%v)", leg.Duration, step.Duration)
	}
	if step.SelfTime() < 0 || leg.SelfTime() < 0 {
		t.Error("self-time must never be negative")
	}
}

func TestDNSPhaseAppearsForHostnames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	u := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)

	sess := adapters.NewSession(adapters.SessionOptions{})
	_, step, err := sess.Do(context.Background(), "ping",
		&adapters.Request{Method: "GET", URL: u}, time.Now())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ph := phases(step.Children[0]); ph["dns"] == nil {
		t.Errorf("hostname target should record a dns phase, got %+v", step.Children[0].Children)
	}
}

func TestRedirectLegsAreSeparateSpans(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/b", http.StatusFound)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("landed"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sess := adapters.NewSession(adapters.SessionOptions{})
	resp, step, err := sess.Do(context.Background(), "hop",
		&adapters.Request{Method: "GET", URL: srv.URL + "/a"}, time.Now())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != 200 || string(resp.Body) != "landed" {
		t.Errorf("response = %d %s", resp.Status, resp.Body)
	}

	if len(step.Children) != 2 {
		t.Fatalf("want one http_call leg per hop, got %+v", step.Children)
	}
	first, second := step.Children[0], step.Children[1]
	if first.Name != "http_call" || second.Name != "http_call" {
		t.Errorf("legs = %q, %q", first.Name, second.Name)
	}
	if phases(first)["ttfb"] == nil || phases(second)["ttfb"] == nil {
		t.Error("every leg should record a ttfb phase")
	}
	if phases(second)["transfer"] == nil {
		t.Error("the final leg should record the transfer phase")
	}
	if first.Start > second.Start {
		t.Errorf("legs out of order: %v then %v", first.Start, second.Start)
	}
}

func TestCookieJarIsPerSession(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "abc"})
	})
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sid"); err == nil && c.Value == "abc" {
			w.Write([]byte("yes"))
			return
		}
		w.Write([]byte("no"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	loggedIn := adapters.NewSession(adapters.SessionOptions{})
	if _, _, err := loggedIn.Do(ctx, "set", &adapters.Request{Method: "GET", URL: srv.URL + "/set"}, time.Now()); err != nil {
		t.Fatalf("set: %v", err)
	}
	resp, _, err := loggedIn.Do(ctx, "check", &adapters.Request{Method: "GET", URL: srv.URL + "/check"}, time.Now())
	if err != nil || string(resp.Body) != "yes" {
		t.Errorf("same session should carry its cookie, got %s (%v)", resp.Body, err)
	}

	fresh := adapters.NewSession(adapters.SessionOptions{})
	resp, _, err = fresh.Do(ctx, "check", &adapters.Request{Method: "GET", URL: srv.URL + "/check"}, time.Now())
	if err != nil || string(resp.Body) != "no" {
		t.Errorf("a fresh session must not share cookies, got %s (%v)", resp.Body, err)
	}
}

func TestTransportErrorMarksStepFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := srv.URL
	srv.Close()

	sess := adapters.NewSession(adapters.SessionOptions{})
	resp, step, err := sess.Do(context.Background(), "login",
		&adapters.Request{Method: "GET", URL: dead}, time.Now())
	if err == nil || resp != nil {
		t.Fatalf("want a transport error, got resp=%+v err=%v", resp, err)
	}
	if step == nil || step.Outcome != span.OutcomeFailed || step.Duration <= 0 {
		t.Errorf("failed call should still return a finished, failed step span: %+v", step)
	}
}

func TestBuildRequestExpandsTemplates(t *testing.T) {
	vals := map[string]string{
		"order_id": "ord-1",
		"token":    "tok-9",
		"flag":     "on",
		"note":     `say "hi"`, // JSON-escaped inside the body
	}
	resolve := func(ref string) (string, error) {
		if v, ok := vals[ref]; ok {
			return v, nil
		}
		return "", errors.New("unknown ref")
	}

	spec := &ir.CallSpec{
		Method:  "POST",
		URL:     "/orders/{{ order_id }}/pay",
		Headers: map[string]string{"Authorization": "Bearer {{ token }}"},
		Query:   map[string]string{"debug": "{{ flag }}"},
		Body:    json.RawMessage(`{"note":"{{ note }}"}`),
	}
	req, err := adapters.BuildRequest(spec, resolve)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if req.URL != "/orders/ord-1/pay" || req.Headers["Authorization"] != "Bearer tok-9" || req.Query["debug"] != "on" {
		t.Errorf("request = %+v", req)
	}

	if !json.Valid(req.Body) {
		t.Fatalf("body with a quoted value must stay valid JSON: %s", req.Body)
	}
	var body struct{ Note string }
	if err := json.Unmarshal(req.Body, &body); err != nil || body.Note != `say "hi"` {
		t.Errorf("body note = %q (%v)", body.Note, err)
	}

	if _, err := adapters.BuildRequest(&ir.CallSpec{Method: "GET", URL: "/x/{{ nope }}"}, resolve); err == nil {
		t.Error("unresolved template should fail BuildRequest")
	}
}
