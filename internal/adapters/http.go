package adapters

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"sync"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

const defaultTimeout = 30 * time.Second

// maxRedirects mirrors net/http's default so the limit is explicit here.
const maxRedirects = 10

// Session is one VU's HTTP identity: its own cookie jar and connection
// reuse, per ADR 0001's isolated-VU model.
type Session struct {
	client *http.Client
}

// SessionOptions tunes a session; the zero value is a sensible default.
type SessionOptions struct {
	// Timeout bounds one call end to end (connect through body read).
	Timeout time.Duration
}

// NewSession builds a session with a fresh cookie jar.
func NewSession(opts SessionOptions) *Session {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	jar, _ := cookiejar.New(nil) // no PublicSuffixList: internal tool, per-VU jar

	// Use a dedicated transport per session so connection reuse is isolated per VU.
	var transport http.RoundTripper = http.DefaultTransport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = dt.Clone()
	}

	return &Session{client: &http.Client{Jar: jar, Timeout: opts.Timeout, Transport: transport}}
}

// Resolver resolves a template reference ("token", "user.email",
// "env.API_HOST") to its value. It is the templating hook the executor
// supplies; adapters never know where variables come from.
type Resolver func(ref string) (string, error)

// Request is a fully resolved HTTP call: templates already expanded,
// ready to send.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Query   map[string]string
	Body    []byte
}

// Response carries what extraction and assertion need from a call.
type Response struct {
	Status  int
	Headers http.Header
	Body    []byte
}

// BuildRequest expands an ir.CallSpec's templates into a concrete Request.
// URL, header, and query values expand verbatim; values injected into the
// JSON body are JSON-escaped so a quote in fixture data cannot corrupt the
// document.
func BuildRequest(spec *ir.CallSpec, resolve Resolver) (*Request, error) {
	req := &Request{Method: spec.Method}

	u, err := ir.ExpandTemplates(spec.URL, resolve)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	req.URL = u

	if len(spec.Headers) > 0 {
		req.Headers = make(map[string]string, len(spec.Headers))
		for k, v := range spec.Headers {
			if req.Headers[k], err = ir.ExpandTemplates(v, resolve); err != nil {
				return nil, fmt.Errorf("header %s: %w", k, err)
			}
		}
	}
	if len(spec.Query) > 0 {
		req.Query = make(map[string]string, len(spec.Query))
		for k, v := range spec.Query {
			if req.Query[k], err = ir.ExpandTemplates(v, resolve); err != nil {
				return nil, fmt.Errorf("query %s: %w", k, err)
			}
		}
	}
	if len(spec.Body) > 0 {
		body, err := ir.ExpandTemplates(string(spec.Body), jsonEscaped(resolve))
		if err != nil {
			return nil, fmt.Errorf("body: %w", err)
		}
		req.Body = []byte(body)
	}
	return req, nil
}

// jsonEscaped wraps a resolver so resolved values are safe inside JSON
// string literals.
func jsonEscaped(resolve Resolver) Resolver {
	return func(ref string) (string, error) {
		v, err := resolve(ref)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b[1 : len(b)-1]), nil
	}
}

// Do executes one call step and returns the response plus the step's span
// tree: the step span with one "http_call" child per request leg (redirects
// are separate legs), each leg carrying the protocol phases that actually
// occurred — dns, connect, tls, ttfb (request written to first response
// byte), transfer (first byte to body read) — per ADR 0007. anchor is the
// iteration's start time; all span offsets are relative to it. The span
// tree is returned even on failure, with outcomes marking what broke.
func (s *Session) Do(ctx context.Context, stepID string, req *Request, anchor time.Time) (*Response, *span.Span, error) {
	step := span.New(stepID, time.Since(anchor))

	httpReq, err := s.newHTTPRequest(ctx, req)
	if err != nil {
		step.Outcome = span.OutcomeFailed
		step.Duration = time.Since(anchor) - step.Start
		return nil, step, err
	}

	rec := &legRecorder{anchor: anchor, step: step}
	httpReq = httpReq.WithContext(httptrace.WithClientTrace(httpReq.Context(), rec.trace()))

	// Shallow copy: shares jar and transport, but this call gets its own
	// redirect hook so legs close at hop boundaries.
	client := *s.client
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		rec.closeLeg(span.OutcomeOK)
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		rec.closeLeg(span.OutcomeFailed)
		step.Outcome = span.OutcomeFailed
		step.Duration = time.Since(anchor) - step.Start
		return nil, step, fmt.Errorf("%s %s: %w", req.Method, req.URL, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()

	rec.endTransfer(time.Since(anchor))
	if readErr != nil {
		rec.closeLeg(span.OutcomeFailed)
		step.Outcome = span.OutcomeFailed
		step.Duration = time.Since(anchor) - step.Start
		return nil, step, fmt.Errorf("read response body: %w", readErr)
	}
	rec.closeLeg(span.OutcomeOK)
	step.Duration = time.Since(anchor) - step.Start

	return &Response{Status: resp.StatusCode, Headers: resp.Header, Body: body}, step, nil
}

func (s *Session) newHTTPRequest(ctx context.Context, req *Request) (*http.Request, error) {
	if req.Method == "" || req.URL == "" {
		return nil, errors.New("request needs a method and a url")
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, fmt.Errorf("parse url %q: %w", req.URL, err)
	}
	if len(req.Query) > 0 {
		q := u.Query()
		for k, v := range req.Query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	return httpReq, nil
}

// legRecorder turns httptrace callbacks into per-leg phase spans. Callbacks
// can arrive on other goroutines, so every mutation holds the mutex. A leg
// opens lazily on the first event after the previous leg closed, which is
// how redirect hops become separate "http_call" spans.
type legRecorder struct {
	mu     sync.Mutex
	anchor time.Time
	step   *span.Span

	leg       *span.Span
	dnsStart  time.Duration
	connStart time.Duration
	tlsStart  time.Duration
	wrote     time.Duration
	firstByte time.Duration
	hasFirst  bool
}

func (r *legRecorder) now() time.Duration { return time.Since(r.anchor) }

// ensureLeg must be called with the mutex held.
func (r *legRecorder) ensureLeg() *span.Span {
	if r.leg == nil {
		r.leg = r.step.Child("http_call", r.now())
		r.dnsStart, r.connStart, r.tlsStart, r.wrote = 0, 0, 0, 0
		r.firstByte, r.hasFirst = 0, false
	}
	return r.leg
}

func (r *legRecorder) closeLeg(outcome span.Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leg == nil {
		return
	}
	r.leg.Duration = r.now() - r.leg.Start
	r.leg.Outcome = outcome
	r.leg = nil
}

// endTransfer closes the final leg's transfer phase once the body is read;
// redirect legs get no transfer span because the client discards their
// bodies.
func (r *legRecorder) endTransfer(at time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leg == nil || !r.hasFirst {
		return
	}
	r.leg.Child("transfer", r.firstByte).Duration = at - r.firstByte
	r.hasFirst = false
}

func (r *legRecorder) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ensureLeg()
		},
		DNSStart: func(httptrace.DNSStartInfo) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ensureLeg()
			r.dnsStart = r.now()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ensureLeg().Child("dns", r.dnsStart).Duration = r.now() - r.dnsStart
		},
		ConnectStart: func(string, string) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ensureLeg()
			if r.connStart == 0 {
				r.connStart = r.now()
			}
		},
		ConnectDone: func(string, string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ensureLeg().Child("connect", r.connStart).Duration = r.now() - r.connStart
		},
		TLSHandshakeStart: func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ensureLeg()
			r.tlsStart = r.now()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ensureLeg().Child("tls", r.tlsStart).Duration = r.now() - r.tlsStart
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.ensureLeg()
			r.wrote = r.now()
		},
		GotFirstResponseByte: func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			leg := r.ensureLeg()
			now := r.now()
			leg.Child("ttfb", r.wrote).Duration = now - r.wrote
			r.firstByte, r.hasFirst = now, true
		},
	}
}
