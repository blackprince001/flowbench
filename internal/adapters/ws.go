package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/coder/websocket"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

// WebSocket rides on the HTTP adapter for the one part of it that is HTTP: the
// handshake. Dialing goes through the same *http.Client every call step uses
// (ADR 0016), so the cookie jar, the transport, the per-VU connection reuse,
// the auth schemes, and the httptrace phase spans all apply to the request
// that opens a session — a `429` before the upgrade is the same throttle it
// would be on any other request.
//
// What is genuinely different starts after the upgrade. A session is not
// call-shaped: it outlives the step that opened it, frames arrive whether or
// not a step asked for one, and the peer can end the conversation on its own
// terms. That is what the rest of this file is about.

// DefaultReceiveTimeout bounds a receive whose step declares none. Without a
// bound a frame that never arrives holds the VU until the run is cancelled,
// which reads as a hang rather than as the failed assertion it is.
const DefaultReceiveTimeout = 5 * time.Second

// closeTimeout bounds the closing handshake at iteration end. Teardown is not
// the measurement, and a peer that will not answer a close must not be able to
// hold a VU hostage.
const closeTimeout = 2 * time.Second

// StatusTryAgainLater is RFC 6455's 1013: the peer is ending the connection
// because it is overloaded, and would like to be asked again later. It is the
// in-band `429` — the only close code that classifies as throttled rather than
// failed.
const StatusTryAgainLater = int(websocket.StatusTryAgainLater)

// WSSession is one open WebSocket connection, held for the life of an
// iteration. It is the seam the library sits behind.
type WSSession struct {
	conn *websocket.Conn
	url  string
}

// URL is the resolved address the session was opened against.
func (s *WSSession) URL() string { return s.url }

// Frame is one received message. Binary frames arrive with their payload
// intact and Binary set: matching and extraction read JSON, so a binary frame
// simply matches nothing, which is the honest outcome rather than an error the
// author cannot act on.
type Frame struct {
	Payload []byte
	Binary  bool
}

// BuildWSOpen turns an opening step into the handshake request. It is an
// ordinary GET, which is the point: everything that decorates a request —
// templating, auth, the allow-list — applies to it unchanged.
func BuildWSOpen(spec *ir.WSSpec, resolve Resolver) (*Request, error) {
	url, err := ir.ExpandTemplates(spec.URL, resolve)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	req := &Request{Method: http.MethodGet, URL: url}
	for k, v := range spec.Headers {
		value, err := ir.ExpandTemplates(v, resolve)
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", k, err)
		}
		req.SetHeader(k, value)
	}
	return req, nil
}

// BuildWSFrame templates the frame the step sends. Values are JSON-escaped by
// the same resolver call bodies use, so an extracted value carrying a quote
// cannot rewrite the message.
func BuildWSFrame(spec *ir.WSSpec, resolve Resolver) ([]byte, error) {
	frame, err := ir.ExpandTemplates(string(spec.Send), jsonEscaped(resolve))
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	if !json.Valid([]byte(frame)) {
		return nil, fmt.Errorf("send frame is not valid JSON after templating: %s", frame)
	}
	return []byte(frame), nil
}

// DialWS opens a session. The Request carries the URL and whatever headers the
// step and its auth put on the handshake, so it is built and decorated exactly
// like a call step's request; the *Response is the handshake response, present
// even when the upgrade was refused so the caller can classify the status.
//
// The returned span mirrors Session.Do's: an `http_call` child with the
// dns/connect/tls/ttfb phases under it, because the handshake really is one.
func (s *Session) DialWS(ctx context.Context, spanName string, req *Request, subprotocols []string, anchor time.Time) (*WSSession, *Response, *span.Span, error) {
	sp := span.New(spanName, time.Since(anchor))

	u, err := req.FinalURL()
	if err != nil {
		sp.Outcome = span.OutcomeFailed
		sp.Duration = time.Since(anchor) - sp.Start
		return nil, nil, sp, err
	}

	header := http.Header{}
	for k, v := range req.Headers {
		header.Set(k, v)
	}

	// The same recorder the HTTP adapter uses: the handshake's phases are the
	// phases of any request, and a reader comparing a ws step to a call step
	// should see the same breakdown under both.
	rec := &legRecorder{anchor: anchor, step: sp}
	defer rec.finish()
	dialCtx := httptrace.WithClientTrace(ctx, rec.trace())

	conn, httpResp, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{
		HTTPClient:   s.client,
		HTTPHeader:   header,
		Subprotocols: subprotocols,
	})
	resp := handshakeResponse(httpResp)
	if err != nil {
		rec.closeLeg(span.OutcomeFailed)
		sp.Outcome = span.OutcomeFailed
		sp.Duration = time.Since(anchor) - sp.Start
		return nil, resp, sp, fmt.Errorf("ws dial %s: %w", u.String(), err)
	}
	rec.closeLeg(span.OutcomeOK)
	sp.Duration = time.Since(anchor) - sp.Start

	return &WSSession{conn: conn, url: u.String()}, resp, sp, nil
}

// Send writes one text frame. v0 speaks JSON, so the payload is the frame.
func (s *WSSession) Send(ctx context.Context, payload []byte) error {
	if err := s.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("ws send: %w", err)
	}
	return nil
}

// Receive reads the next frame, waiting at most timeout. A zero timeout uses
// the default.
//
// The deadline is per receive rather than per frame on purpose: a step waiting
// for one frame among many should time out when it has waited long enough in
// total, not be kept alive indefinitely by a heartbeat it is skipping.
func (s *WSSession) Receive(ctx context.Context, timeout time.Duration) (Frame, error) {
	if timeout <= 0 {
		timeout = DefaultReceiveTimeout
	}
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	typ, payload, err := s.conn.Read(readCtx)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Payload: payload, Binary: typ == websocket.MessageBinary}, nil
}

// Close ends the session with a normal closure. Best effort: a peer that has
// already gone leaves nothing to say goodbye to, and CloseNow releases the
// connection regardless.
func (s *WSSession) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	if err := s.closeWithin(ctx); err != nil {
		s.conn.CloseNow()
	}
}

func (s *WSSession) closeWithin(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- s.conn.Close(websocket.StatusNormalClosure, "") }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WSCloseError is the peer ending the session, as opposed to the transport
// failing. Code is the RFC 6455 close code.
type WSCloseError struct {
	Code   int
	Reason string
}

func (e *WSCloseError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("peer closed the session (%d %s)", e.Code, closeCodeName(e.Code))
	}
	return fmt.Sprintf("peer closed the session (%d %s): %s", e.Code, closeCodeName(e.Code), e.Reason)
}

// AsWSCloseError reports whether err is the peer closing the session, and with
// which code. A close is not a transport failure — it is the far end's verdict
// on the conversation, and 1013 in particular is a throttle.
func AsWSCloseError(err error) (*WSCloseError, bool) {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return &WSCloseError{Code: int(ce.Code), Reason: ce.Reason}, true
	}
	if code := websocket.CloseStatus(err); code != -1 {
		return &WSCloseError{Code: int(code)}, true
	}
	return nil, false
}

// closeCodeName renders the RFC 6455 §7.4.1 registry entry for a code, so a
// failure says "1011 internal error" rather than a bare number.
func closeCodeName(code int) string {
	switch websocket.StatusCode(code) {
	case websocket.StatusNormalClosure:
		return "normal"
	case websocket.StatusGoingAway:
		return "going away"
	case websocket.StatusProtocolError:
		return "protocol error"
	case websocket.StatusUnsupportedData:
		return "unsupported data"
	case websocket.StatusNoStatusRcvd:
		return "no status"
	case websocket.StatusAbnormalClosure:
		return "abnormal"
	case websocket.StatusInvalidFramePayloadData:
		return "invalid payload"
	case websocket.StatusPolicyViolation:
		return "policy violation"
	case websocket.StatusMessageTooBig:
		return "message too big"
	case websocket.StatusMandatoryExtension:
		return "mandatory extension"
	case websocket.StatusInternalError:
		return "internal error"
	case websocket.StatusServiceRestart:
		return "service restart"
	case websocket.StatusTryAgainLater:
		return "try again later"
	case websocket.StatusBadGateway:
		return "bad gateway"
	case websocket.StatusTLSHandshake:
		return "tls handshake"
	default:
		return "unknown"
	}
}

// handshakeResponse converts the handshake's *http.Response into the adapter's
// own shape. The body is deliberately left out: on a successful upgrade it is
// the connection itself, and reading it would consume the session.
func handshakeResponse(resp *http.Response) *Response {
	if resp == nil {
		return nil
	}
	return &Response{Status: resp.StatusCode, Headers: resp.Header}
}
