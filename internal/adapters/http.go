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
	"strings"
	"sync"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
	"github.com/blackprince001/flowbench/internal/version"
)

const defaultTimeout = 30 * time.Second

const maxRedirects = 10

// userAgent is resolved once: every request on every VU carries it, so it is
// not worth rebuilding ten thousand times a second.
var userAgent = version.UserAgent()

type Session struct {
	client *http.Client
}

type SessionOptions struct {
	Timeout time.Duration
}

func NewSession(opts SessionOptions) *Session {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	jar, _ := cookiejar.New(nil)

	// dedicated transport per session so connection reuse is isolated per VU
	transport := http.DefaultTransport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = dt.Clone()
	}

	return &Session{client: &http.Client{Jar: jar, Timeout: opts.Timeout, Transport: transport}}
}

type Resolver func(ref string) (string, error)

type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Query   map[string]string
	Body    []byte
}

type Response struct {
	Status  int
	Headers http.Header
	Body    []byte
}

// FinalURL is the request as it will be sent: URL with Query merged in. The
// transport and the HMAC signer both build it here, so a signature always
// covers the URL that actually goes on the wire.
func (r *Request) FinalURL() (*url.URL, error) {
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, fmt.Errorf("parse url %q: %w", r.URL, err)
	}
	if len(r.Query) > 0 {
		q := u.Query()
		for k, v := range r.Query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	return u, nil
}

// SetHeader sets a header, allocating the map on first use.
func (r *Request) SetHeader(name, value string) {
	if r.Headers == nil {
		r.Headers = make(map[string]string, 1)
	}
	r.Headers[name] = value
}

// declares reports whether the flow set this header itself, in whatever case
// it wrote it. The map is keyed as authored while header names are not
// case-sensitive, so `content-type` has to count as declaring Content-Type —
// otherwise a default would arrive alongside it as a second value.
func (r *Request) declares(name string) bool {
	for k := range r.Headers {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// SetQuery sets a query parameter, allocating the map on first use.
func (r *Request) SetQuery(name, value string) {
	if r.Query == nil {
		r.Query = make(map[string]string, 1)
	}
	r.Query[name] = value
}

// AddCookie sets one cookie in the Cookie header, keeping the others and
// replacing any pair of the same name. Keeping the others is why a declared
// cookie rides alongside the ones the step's own headers carry; replacing by
// name is why re-applying on a retry does not send it twice.
func (r *Request) AddCookie(name, value string) {
	var pairs []string
	for _, pair := range strings.Split(r.Headers["Cookie"], ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if k, _, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(k) == name {
			continue
		}
		pairs = append(pairs, pair)
	}
	r.SetHeader("Cookie", strings.Join(append(pairs, name+"="+value), "; "))
}

// BuildRequest expands templates; body values are JSON-escaped so a quote in
// fixture data cannot corrupt the document.
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

		// The type is known rather than guessed: the parser marshals every
		// body to JSON and validation refuses anything that isn't. Saying so
		// is what a server needs to parse it at all. A declared header wins,
		// and an empty one suppresses this entirely — which is how a flow
		// tests what its target does with no Content-Type.
		if !req.declares("Content-Type") {
			req.SetHeader("Content-Type", "application/json")
		}
	}
	return req, nil
}

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

// Do executes one call step. anchor is the iteration start; all span offsets
// are relative to it. The span tree is returned even on failure.
func (s *Session) Do(ctx context.Context, stepID string, req *Request, anchor time.Time) (*Response, *span.Span, error) {
	step := span.New(stepID, time.Since(anchor))

	httpReq, err := s.newHTTPRequest(ctx, req)
	if err != nil {
		step.Outcome = span.OutcomeFailed
		step.Duration = time.Since(anchor) - step.Start
		return nil, step, err
	}

	rec := &legRecorder{anchor: anchor, step: step}
	// Freeze the span tree once Do returns: httptrace callbacks from speculative
	// dials can fire afterward, and the pool folds/captures/stores the tree
	// concurrently. finish makes those late callbacks no-ops.
	defer rec.finish()
	httpReq = httpReq.WithContext(httptrace.WithClientTrace(httpReq.Context(), rec.trace()))

	// shallow copy: shares jar and transport, but this call gets its own
	// redirect hook so legs close at hop boundaries
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
	u, err := req.FinalURL()
	if err != nil {
		return nil, err
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
		if v == "" {
			// An empty value means "send no such header". Setting it would
			// put a blank one on the wire instead, so the key is left out —
			// except for User-Agent, where Go substitutes its own default
			// unless the key is present, and present-but-empty is the only
			// way to say none at all.
			if strings.EqualFold(k, "User-Agent") {
				httpReq.Header.Set("User-Agent", "")
			}
			continue
		}
		httpReq.Header.Set(k, v)
	}
	if !req.declares("User-Agent") {
		httpReq.Header.Set("User-Agent", userAgent)
	}
	return httpReq, nil
}

// legRecorder turns httptrace callbacks into per-leg phase spans. Callbacks
// arrive on other goroutines, so every mutation holds the mutex.
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
	done      bool // set once Do returns; late callbacks stop touching the tree
}

func (r *legRecorder) now() time.Duration { return time.Since(r.anchor) }

// ensureLeg must be called with the mutex held. Once Do has returned it hands
// back a detached span so a late callback mutates a throwaway, never the tree
// the pool is reading.
func (r *legRecorder) ensureLeg() *span.Span {
	if r.done {
		return span.New("http_call", r.now())
	}
	if r.leg == nil {
		r.leg = r.step.Child("http_call", r.now())
		r.dnsStart, r.connStart, r.tlsStart, r.wrote = 0, 0, 0, 0
		r.firstByte, r.hasFirst = 0, false
	}
	return r.leg
}

// finish freezes the recorder as Do returns. Acquiring the mutex waits out any
// in-flight callback, and the done flag makes any that follow no-ops, so the
// span tree is complete and immutable before the pool folds, captures, or
// stores it.
func (r *legRecorder) finish() {
	r.mu.Lock()
	r.done = true
	r.mu.Unlock()
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

// endTransfer closes the final leg's transfer phase once the body is read.
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
