package span_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/span"
)

func scrub(secret string) func([]byte) []byte {
	return func(b []byte) []byte {
		return bytes.ReplaceAll(b, []byte(secret), []byte("[redacted]"))
	}
}

// A span's outcome says `failed`; the status says what actually came back.
func TestFinalizeCapturesCallIdentity(t *testing.T) {
	s := span.New("login", 0)
	s.SetRaw([]byte(`{"u":"ada"}`), []byte(`{"error":"boom"}`))
	s.SetCall("POST", "http://localhost:8080/login", 503, "")

	span.Finalize(s, scrub("nothing"), 4096)
	p := s.Payload
	if p == nil {
		t.Fatal("no payload captured")
	}
	if p.Status != 503 || p.Method != "POST" || !strings.HasSuffix(p.URL, "/login") {
		t.Errorf("call identity not captured: %+v", p)
	}
	if p.ReqBytes != 11 || p.RespBytes != 16 {
		t.Errorf("byte counts wrong: %d sent, %d received", p.ReqBytes, p.RespBytes)
	}
}

// A throttle's Retry-After is the server stating how long it wanted to be left
// alone, so it survives capture (ADR 0006).
func TestFinalizeKeepsRetryAfter(t *testing.T) {
	s := span.New("checkout", 0)
	s.SetCall("POST", "/checkout", 429, "1")
	span.Finalize(s, scrub("x"), 0)

	if s.Payload == nil || s.Payload.RetryAfter != "1" || s.Payload.Status != 429 {
		t.Errorf("throttle detail lost: %+v", s.Payload)
	}
}

// A credential can ride in a URL's query, and unlike a body the URL is never
// size-capped away — so redaction is the only thing protecting it.
func TestFinalizeRedactsTheURL(t *testing.T) {
	const secret = "hunter2-do-not-leak"
	s := span.New("login", 0)
	s.SetCall("GET", "http://h/login?token="+secret, 200, "")
	s.SetRaw(nil, []byte(secret))

	span.Finalize(s, scrub(secret), 4096)
	if strings.Contains(s.Payload.URL, secret) {
		t.Errorf("secret survived in the URL: %s", s.Payload.URL)
	}
	if !strings.Contains(s.Payload.URL, "[redacted]") {
		t.Errorf("URL not scrubbed: %s", s.Payload.URL)
	}
	if strings.Contains(s.Payload.Response, secret) {
		t.Error("secret survived in the response body")
	}
}

// A capture-disabled span records nothing at all.
func TestFinalizeCapturesNothingWithoutACall(t *testing.T) {
	s := span.New("assert_status", 0)
	span.Finalize(s, scrub("x"), 4096)
	if s.Payload != nil {
		t.Errorf("a span with no call should carry no payload, got %+v", s.Payload)
	}
}
