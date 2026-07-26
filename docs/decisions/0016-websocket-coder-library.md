---
type: Decision
title: "ADR 0016: coder/websocket for the WebSocket adapter"
description: The ws adapter is built on github.com/coder/websocket rather than a hand-rolled RFC 6455 client, chosen because its handshake is an ordinary http.Client request — so the session, cookie jar, auth, host allow-list, and per-phase spans carry over unchanged.
status: Accepted
timestamp: 2026-07-26
---
# ADR 0016: coder/websocket for the WebSocket adapter

Status: Accepted

## Context

The `ws` step type (issue #27) opens a session, exchanges matched messages
across steps, and asserts on received frames. Go's standard library has no
WebSocket client, so the adapter either imports one or implements RFC 6455.

The project has hand-rolled before — the JSONPath subset (ADR 0011) and both
report renderers (ADR 0014) — so importing is not automatic. But those two
were bounded expression/layout problems with an obvious seam. RFC 6455 is a
wire protocol: masking, fragmentation, control frames interleaved inside
fragmented messages, the close handshake, UTF-8 validation of text payloads.
Getting it subtly wrong produces a test tool that lies about the target.

The deciding constraint is not the framing, though — it is the handshake. A
WebSocket connection opens with an HTTP `GET` carrying `Upgrade: websocket`,
and everything FlowBench already does to an HTTP request should apply to it:
the target's host allow-list, the auth schemes from #30, the cookie jar, the
per-VU session and its transport, and the `httptrace` hooks that produce the
`dns`/`connect`/`tls` phase spans. An adapter that dials its own TCP
connection re-implements all of that or silently loses it.

## Decision

Use `github.com/coder/websocket` (MIT, the maintained successor to
`nhooyr.io/websocket`), confined to `internal/adapters` the way ADR 0010
confines goccy to `internal/parser`.

It is the only mainstream Go client whose `Dial` takes an `*http.Client` and
performs the handshake through it, using the `net/http` protocol-switch
support added in Go 1.12. The adapter therefore hands it the same
`adapters.Session` client every call step uses, and the handshake is a real
request: `auth.Provider.Apply` decorates it, the jar sends and stores cookies,
`httptrace` records the phases, and a `429` before the upgrade classifies as
`throttled` exactly like any other `429`. It also has no dependencies of its
own, so `go.sum` gains one line.

## Consequences

- A `ws` step inherits the HTTP machinery rather than duplicating it, which is
  the same relationship `graphql` has to `call` (ADR 0002's one-IR principle
  showing up at the adapter layer).
- Second third-party dependency in the engine, and the first outside the
  parser. Confined to `internal/adapters`; the IR, executor, and evaluator
  stay dependency-free, so the library is swappable behind `WSSession`.
- Rejected: **hand-rolling RFC 6455.** Correct framing is ~400 lines before
  the handshake, and the handshake is the part that had to integrate with
  `http.Client` anyway — so the hand-rolled version's cost lands entirely on
  the protocol details that are not this project's contribution.
- Rejected: **gorilla/websocket.** Widely used and well tested, but it dials
  through its own `Dialer` with its own TLS config, proxy handling, and cookie
  jar. Every integration above would have to be re-plumbed into a second
  transport, and the phase spans would have to be re-derived from its dial
  hooks rather than shared with the HTTP adapter.
- The library's default 32 KiB read limit and disabled compression are kept:
  a frame larger than that is a payload a flow should not be matching on, and
  `permessage-deflate` would make captured frame sizes lie about the wire.
