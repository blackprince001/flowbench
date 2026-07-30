package adapters

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// gRPC is the first adapter that does not ride on the HTTP one. It brings its
// own HTTP/2 client, so the `*http.Client`, the cookie jar and the httptrace
// hooks have nothing to say about it — which is why this file re-derives the
// one thing that mattered about them, the phase spans, from gRPC's own stats
// handler.
//
// Everything above the wire is deliberately unchanged. The request is an
// ordinary adapters.Request, so templating, the host allow-list and every auth
// scheme decorate it exactly as they decorate a call step's — gRPC metadata is
// HTTP/2 headers, and the method is the `:path`, so an `Authorization` header
// or an HMAC signature over `{path}` means on this wire what it means on the
// other one. The response is converted to JSON, so extraction and assertions
// are the same JSONPath evaluator, and the status is an ordinary status.
//
// Only unary calls. Streaming is out of v1 by decision (ADR 0019, spike #29),
// which also records how the three stream kinds would map if demand appears.

// grpcConnectTimeout bounds waiting for a channel to come up. It is separate
// from the call timeout because it is paid once per VU per address, and a
// target that will not accept a connection should be reported as unreachable
// rather than as a slow call.
const grpcConnectTimeout = 10 * time.Second

// GRPCResponse is what a unary call answered: the status in both forms the rest
// of the engine needs, the response message as JSON, and the metadata.
type GRPCResponse struct {
	// Code is the numeric gRPC status — 0 is OK. It is what `status ==`
	// assertions, `throttle.statuses` and `retry.on_status` compare against,
	// because those are ints everywhere else in the IR too.
	Code codes.Code

	// Message is the status message, empty on OK.
	Message string

	// Body is the response message rendered as JSON, so JSONPath extraction and
	// body assertions work on it unchanged. A non-OK call carries no message,
	// so this is nil and the status is the whole answer.
	Body []byte

	// Headers are the response metadata, headers and trailers together, as
	// http.Header so `header.x` reads the same as it does on an HTTP step.
	Headers http.Header

	// RetryAfter is the server asking to be left alone, if it said so.
	RetryAfter string
}

// StatusText is the status in gRPC's own vocabulary — RESOURCE_EXHAUSTED, not
// the number 8 and emphatically not "HTTP 8".
func (r *GRPCResponse) StatusText() string { return GRPCCodeName(r.Code) }

// GRPCCall is one resolved unary call: where it goes, which method it invokes,
// and the request that carries it.
type GRPCCall struct {
	// Method is the resolved schema, from the run's ProtoRegistry.
	Method *GRPCMethod

	// Request holds the address (as the URL), the JSON message (as the body),
	// and the metadata (as the headers). It is an ordinary Request because
	// everything that decorates one should reach a gRPC call unchanged.
	Request *Request
}

// BuildGRPCRequest turns a step into the request that carries it. The message
// is templated with the JSON-escaping resolver call bodies use, so a quote in
// an extracted value cannot corrupt the document it is about to become.
//
// The URL is left as the step wrote it; the executor resolves it against the
// target's base address and appends the method, since the method is the path.
func BuildGRPCRequest(spec *ir.GRPCSpec, resolve Resolver) (*Request, error) {
	address, err := ir.ExpandTemplates(spec.URL, resolve)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}

	req := &Request{Method: http.MethodPost, URL: address}
	req.SetHeader("Content-Type", "application/grpc")
	for k, v := range spec.Headers {
		value, err := ir.ExpandTemplates(v, resolve)
		if err != nil {
			return nil, fmt.Errorf("metadata %s: %w", k, err)
		}
		req.SetHeader(k, value)
	}

	if len(spec.Message) > 0 {
		body, err := ir.ExpandTemplates(string(spec.Message), jsonEscaped(resolve))
		if err != nil {
			return nil, fmt.Errorf("message: %w", err)
		}
		req.Body = []byte(body)
	}
	return req, nil
}

// GRPCConns is one VU's gRPC channels, one per address.
//
// Per VU rather than per run, matching the dedicated transport each HTTP
// session gets: a channel multiplexes every call a VU makes over one HTTP/2
// connection, and sharing one across 10k VUs would measure that channel's
// stream limit rather than the target.
type GRPCConns struct {
	timeout time.Duration

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

func NewGRPCConns(timeout time.Duration) *GRPCConns {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &GRPCConns{timeout: timeout, conns: map[string]*grpc.ClientConn{}}
}

// Close releases every channel. Teardown, not measurement: it runs when the VU
// ends, however it ended.
func (c *GRPCConns) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.conns {
		conn.Close()
	}
	c.conns = nil
}

// Invoke performs one unary call. anchor is the iteration start; all span
// offsets are relative to it, and the span tree comes back even on failure.
//
// A non-OK status is a response, not an error — the status is data the flow's
// own assertions judge, exactly as an HTTP 500 is. The exception is the set of
// codes that mean the call never reached an application handler at all; those
// come back as an error, because a target that is not there is a failed call
// and not a target with an opinion.
func (c *GRPCConns) Invoke(ctx context.Context, stepID string, call *GRPCCall, anchor time.Time) (*GRPCResponse, *span.Span, error) {
	sp := span.New(stepID, time.Since(anchor))
	fail := func(err error) (*GRPCResponse, *span.Span, error) {
		sp.Outcome = span.OutcomeFailed
		sp.Duration = time.Since(anchor) - sp.Start
		return nil, sp, err
	}

	if len(call.Request.Query) > 0 {
		// An api_key auth scheme set to ride in the query has nowhere to go: a
		// gRPC call has no query string. Better to say so than to drop the
		// credential and let the target answer UNAUTHENTICATED.
		return fail(fmt.Errorf("gRPC has no query string, so %d query parameter(s) cannot be sent; put the credential in metadata instead", len(call.Request.Query)))
	}

	address, secure, err := grpcAddress(call.Request.URL)
	if err != nil {
		return fail(err)
	}

	input := dynamicpb.NewMessage(call.Method.Input)
	if len(call.Request.Body) > 0 {
		opts := protojson.UnmarshalOptions{Resolver: call.Method.Resolver}
		if err := opts.Unmarshal(call.Request.Body, input); err != nil {
			return fail(fmt.Errorf("message does not fit %s: %w", call.Method.Input.FullName(), err))
		}
	}

	leg := sp.Child("grpc_call", time.Since(anchor))
	conn, err := c.channel(ctx, address, secure, anchor, leg)
	if err != nil {
		leg.Outcome = span.OutcomeFailed
		leg.Duration = time.Since(anchor) - leg.Start
		return fail(err)
	}

	phases := &grpcPhases{anchor: anchor, leg: leg}
	defer phases.finish()
	callCtx := context.WithValue(ctx, grpcPhaseKey{}, phases)
	if md := outgoingMetadata(call.Request.Headers); md.Len() > 0 {
		callCtx = metadata.NewOutgoingContext(callCtx, md)
	}
	callCtx, cancel := context.WithTimeout(callCtx, c.timeout)
	defer cancel()

	var header, trailer metadata.MD
	output := dynamicpb.NewMessage(call.Method.Output)
	invokeErr := conn.Invoke(callCtx, call.Method.Path, input, output,
		grpc.Header(&header), grpc.Trailer(&trailer))

	st, _ := status.FromError(invokeErr)
	resp := &GRPCResponse{
		Code:    st.Code(),
		Message: st.Message(),
		Headers: grpcHeaders(header, trailer),
	}
	resp.RetryAfter = grpcRetryAfter(resp.Headers)

	if invokeErr == nil {
		// EmitDefaultValues is what makes assertions about values rather than
		// about presence. Without it protojson drops a field holding its zero,
		// so `$.amountCents` on a zero-amount charge would fail extraction with
		// "found nothing" — when the field is there and it is 0. It emits
		// defaults only for fields that have no presence in the first place, so
		// an unset `optional` or message field still reads as absent and
		// `not_exists` keeps meaning something.
		body, err := protojson.MarshalOptions{
			Resolver:          call.Method.Resolver,
			EmitDefaultValues: true,
		}.Marshal(output)
		if err != nil {
			leg.Outcome = span.OutcomeFailed
			leg.Duration = time.Since(anchor) - leg.Start
			return fail(fmt.Errorf("rendering %s as JSON: %w", call.Method.Output.FullName(), err))
		}
		resp.Body = body
	}

	if unreachable(st.Code()) {
		leg.Outcome = span.OutcomeFailed
		leg.Duration = time.Since(anchor) - leg.Start
		sp.Outcome = span.OutcomeFailed
		sp.Duration = time.Since(anchor) - sp.Start
		return resp, sp, fmt.Errorf("grpc %s%s: %s: %s",
			address, call.Method.Path, GRPCCodeName(st.Code()), st.Message())
	}

	leg.Duration = time.Since(anchor) - leg.Start
	sp.Duration = time.Since(anchor) - sp.Start
	return resp, sp, nil
}

// unreachable reports whether a status means the call never reached an
// application handler.
//
// These are the codes gRPC generates for the channel and the deadline rather
// than for the service — nothing was listening, or we gave up waiting — so
// they are failed calls, not answers. Every other non-OK code is a verdict the
// server chose to send, and a verdict is data (the same reading HTTP gets: a
// 500 is a response, a refused connection is not).
func unreachable(c codes.Code) bool {
	switch c {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return true
	}
	return false
}

// channel returns the VU's connection to address, opening it on first use.
//
// Connecting is timed and spanned separately because gRPC channels are lazy:
// without this the first call on a VU would silently carry the dial, and the
// step's latency would say the target was slow when it was only new.
func (c *GRPCConns) channel(ctx context.Context, address string, secure bool, anchor time.Time, leg *span.Span) (*grpc.ClientConn, error) {
	c.mu.Lock()
	if c.conns == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("grpc channels are closed")
	}
	if conn, ok := c.conns[address]; ok {
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()

	creds := insecure.NewCredentials()
	if secure {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(creds),
		grpc.WithStatsHandler(grpcStatsHandler{}),
	)
	if err != nil {
		return nil, fmt.Errorf("grpc channel to %s: %w", address, err)
	}

	start := time.Since(anchor)
	c.waitReady(ctx, conn)
	leg.Child("connect", start).Duration = time.Since(anchor) - start

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conns == nil {
		conn.Close()
		return nil, fmt.Errorf("grpc channels are closed")
	}
	// Another goroutine cannot race us here — a GRPCConns belongs to one VU —
	// but the map may have been replaced by Close, which the check above covers.
	c.conns[address] = conn
	return conn, nil
}

// waitReady drives the channel up and waits for it, bounded.
//
// A channel that lands in TRANSIENT_FAILURE is not reported here on purpose:
// the state carries no reason, and the call that follows fails with the real
// one ("connection refused", "no such host") — which is what the failure
// drill-down groups by.
func (c *GRPCConns) waitReady(ctx context.Context, conn *grpc.ClientConn) {
	ctx, cancel := context.WithTimeout(ctx, grpcConnectTimeout)
	defer cancel()
	conn.Connect()
	for {
		state := conn.GetState()
		if state == connectivity.Ready || state == connectivity.TransientFailure {
			return
		}
		if !conn.WaitForStateChange(ctx, state) {
			return
		}
	}
}

// grpcAddress splits a resolved `grpc://host:port` into what the client needs.
func grpcAddress(raw string) (address string, secure bool, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, fmt.Errorf("parse grpc address %q: %w", raw, err)
	}
	switch u.Scheme {
	case "grpc", "http":
		secure = false
	case "grpcs", "https":
		secure = true
	default:
		return "", false, fmt.Errorf("grpc address %q needs a grpc:// or grpcs:// scheme", raw)
	}
	if u.Host == "" {
		return "", false, fmt.Errorf("grpc address %q names no host", raw)
	}
	return u.Host, secure, nil
}

// outgoingMetadata turns the request's headers into gRPC metadata. Keys are
// lowercased because that is what they are on an HTTP/2 wire, and a server
// looking one up will look for the lowercase form.
func outgoingMetadata(headers map[string]string) metadata.MD {
	md := metadata.MD{}
	for k, v := range headers {
		key := strings.ToLower(k)
		if key == "content-type" {
			continue // the client owns it; overriding it breaks the framing
		}
		md.Set(key, v)
	}
	return md
}

// grpcHeaders merges response headers and trailers into one lookup, since a
// flow asking for `header.x` does not care which half of the response carried
// it — and a trailers-only response (any error, most of the time) puts
// everything in the second half.
//
// Binary metadata is skipped: a `-bin` value is raw bytes, not text, and
// storing it in a captured artifact would put invalid UTF-8 in a JSON file.
func grpcHeaders(header, trailer metadata.MD) http.Header {
	out := http.Header{}
	for _, md := range []metadata.MD{header, trailer} {
		for k, values := range md {
			if strings.HasSuffix(k, "-bin") {
				continue
			}
			for _, v := range values {
				out.Add(k, v)
			}
		}
	}
	return out
}

// grpcRetryAfter is the server saying how long it wanted to be left alone.
// gRPC has no standard for it, so both spellings in the wild are read: the
// HTTP header name servers reuse, and the pushback header gRPC's own retry
// policy defines (in milliseconds).
func grpcRetryAfter(h http.Header) string {
	if v := h.Get("retry-after"); v != "" {
		return v
	}
	if v := h.Get("grpc-retry-pushback-ms"); v != "" {
		return v + "ms"
	}
	return ""
}

// GRPCCodeName renders a status in gRPC's canonical vocabulary —
// RESOURCE_EXHAUSTED rather than grpc-go's CamelCase — because that is the
// spelling in the spec, in grpcurl, and in the service's own .proto comments.
func GRPCCodeName(c codes.Code) string {
	names := [...]string{
		"OK", "CANCELLED", "UNKNOWN", "INVALID_ARGUMENT", "DEADLINE_EXCEEDED",
		"NOT_FOUND", "ALREADY_EXISTS", "PERMISSION_DENIED", "RESOURCE_EXHAUSTED",
		"FAILED_PRECONDITION", "ABORTED", "OUT_OF_RANGE", "UNIMPLEMENTED",
		"INTERNAL", "UNAVAILABLE", "DATA_LOSS", "UNAUTHENTICATED",
	}
	if int(c) < len(names) {
		return names[c]
	}
	return fmt.Sprintf("CODE_%d", int(c))
}

// grpcPhaseKey carries the per-call phase recorder to the stats handler, which
// is installed on the channel and therefore shared by every call on it.
type grpcPhaseKey struct{}

type grpcStatsHandler struct{}

func (grpcStatsHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (grpcStatsHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (grpcStatsHandler) HandleConn(context.Context, stats.ConnStats) {}

func (grpcStatsHandler) HandleRPC(ctx context.Context, s stats.RPCStats) {
	if p, ok := ctx.Value(grpcPhaseKey{}).(*grpcPhases); ok {
		p.handle(s)
	}
}

// grpcPhases turns gRPC's stats events into the same phase spans httptrace
// produces for a call step, so a reader comparing the two sees one breakdown
// rather than two.
//
// Events arrive on the transport's goroutines, so every mutation holds the
// mutex, and finish freezes the tree as Invoke returns — the pool folds,
// captures and stores it concurrently, and a late callback must not be able to
// touch it. That is the same discipline legRecorder uses, for the same reason.
type grpcPhases struct {
	mu     sync.Mutex
	anchor time.Time
	leg    *span.Span

	sent       time.Duration
	hasSent    bool
	headers    time.Duration
	hasHeaders bool
	done       bool
}

func (p *grpcPhases) finish() {
	p.mu.Lock()
	p.done = true
	p.mu.Unlock()
}

func (p *grpcPhases) handle(s stats.RPCStats) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	now := time.Since(p.anchor)

	switch s.(type) {
	case *stats.OutPayload:
		p.sent, p.hasSent = now, true

	case *stats.InHeader:
		// Time to first response byte: the request is on the wire and the
		// target has started answering.
		if p.hasSent && !p.hasHeaders {
			p.leg.Child("ttfb", p.sent).Duration = now - p.sent
			p.headers, p.hasHeaders = now, true
		}

	case *stats.InTrailer:
		// A trailers-only response — the shape of most errors — never sends
		// headers, so this is where its first byte arrives.
		if p.hasSent && !p.hasHeaders {
			p.leg.Child("ttfb", p.sent).Duration = now - p.sent
			p.headers, p.hasHeaders = now, true
		}

	case *stats.InPayload:
		if p.hasHeaders {
			p.leg.Child("transfer", p.headers).Duration = now - p.headers
			p.hasHeaders = false
		}
	}
}
