---
type: Decision
title: "ADR 0018: gRPC calls are made dynamically from .proto, with no code generation"
description: The gRPC adapter compiles .proto files to descriptors at run time with bufbuild/protocompile and invokes methods through dynamicpb, so a flow names a schema file rather than depending on generated Go stubs — and the response is converted to JSON so extraction, assertions and the span model apply unchanged.
status: Accepted
timestamp: 2026-07-27
---
# ADR 0018: gRPC calls are made dynamically from .proto, with no code generation

Status: Accepted

## Context

The `grpc` step type (issue #28) calls unary methods described by the team's
own `.proto` files. Go's usual answer is `protoc` plus `protoc-gen-go-grpc`:
the schema becomes generated Go, and a client calls a typed method on it.

That answer does not fit a testing tool. The schemas belong to the services
under test, not to FlowBench, and they change on those services' release
cycles. Generating from them would mean one of three things, all bad: a
`protoc` toolchain and a build step in every contributor's path and in CI; a
rebuild of the *engine* whenever a target adds a field; or a plugin boundary
where a user compiles their schema into something we load. A flow author
should be able to point at a `.proto` and run, the way they point at a URL.

The second constraint is the span and evaluation model. Everything the engine
does above the wire — `{{ }}` templating, JSONPath extraction, body
assertions, capture and redaction, the flame graph, the failure drill-down —
is written against JSON and a status. A protobuf-shaped step that needed its
own evaluator would fork all of it.

## Decision

Compile `.proto` files to descriptors at run time and invoke methods
dynamically:

- `github.com/bufbuild/protocompile` (Apache 2.0) parses and links the schema —
  a pure-Go `protoc`, no external binary.
- `google.golang.org/protobuf/types/dynamicpb` builds request and response
  messages from those descriptors; `google.golang.org/grpc`'s default codec
  marshals them, because a `*dynamicpb.Message` is an ordinary `proto.Message`.
- `protojson` converts in both directions, so a step's `message:` block is JSON
  going in and the response is JSON coming out.

Compilation happens **once per run, before the first request**. `ProtoRegistry.
Prepare` walks the scenario in `flowbench run`, next to the host allow-list and
the VU ceilings, so a missing file, an unknown method or a streaming method is
a pre-run error with the alternatives listed — not something 10k VUs each
discover separately.

## Consequences

- A flow names a `.proto` and a fully-qualified method. Nothing is generated,
  nothing is checked in, and a schema change is picked up on the next run.
- The response being JSON is what makes the rest of the engine apply unchanged:
  `$.chargeId` extraction, body assertions, capture, redaction and folding are
  the same code paths a `call` step uses. A field the schema does not declare
  is an error on the way in, rather than something the server quietly ignores.
- **The compiler's reach is a pinned dependency's property, not ours.**
  protocompile v0.14.1 accepts proto2, proto3 and edition 2023 — and only
  edition 2023 (`MinSupportedEdition == MaxSupportedEdition`). A schema written
  for a newer edition fails to compile, so the error says what this build
  accepts, and `TestEverySupportedProtoSyntaxCompiles` pins all three rather
  than leaving the claim in prose here. Well-known imports resolve from the
  author's import paths first and fall back to the copies embedded in the Go
  protobuf module, so a vendored `google/protobuf/*.proto` wins.
- Three modules enter the engine (`grpc`, `protobuf`, `protocompile`) where the
  previous two adapters cost one line of `go.sum` each. That is the real price,
  and it is paid because gRPC is a wire protocol with a schema language, a
  status model and an HTTP/2 client — none of which is this project's
  contribution. They are confined to `internal/adapters` and `internal/grpcstub`
  the way ADR 0010 confines goccy to the parser.
- gRPC brings its own client, so unlike `graphql` and `ws` (ADR 0016) this
  adapter does *not* ride on the HTTP session: no shared `http.Client`, no
  cookie jar, no `httptrace`. What is re-derived rather than lost is the phase
  breakdown — a `stats.Handler` produces `connect`/`ttfb`/`transfer` under a
  `grpc_call` leg, so a `grpc` step reads in the waterfall the way a `call`
  step does. A 120-VU run against the example stub folds to 120 `connect`
  spans, one per VU, and a `transfer` only on the calls that carried a message.
- One `*grpc.ClientConn` per VU, matching the per-VU HTTP transport: a channel
  multiplexes every call over one HTTP/2 connection, so sharing one across 10k
  VUs would measure that channel's stream limit rather than the target.
- The same descriptors serve as well as call, which is why `internal/grpcstub`
  exists: the test fixtures and `examples/grpc-local` are a schema plus a few
  JSON handlers, with no generated code on the server side either.
- Rejected: **requiring generated stubs.** It moves a toolchain into every
  contributor's path and couples the engine's build to the schemas of the
  services it tests.
- Rejected: **server reflection** (asking the target to describe itself).
  Attractive — no file to name — but it needs the target to have reflection
  enabled, which production services routinely do not, and it makes the schema
  a property of the environment rather than of the flow, so the same flow could
  mean different things against staging and prod. It is a reasonable *addition*
  later; it is not a replacement for naming a file.
- Rejected: **`jhump/protoreflect`.** It is the older, wider library and now
  wraps protocompile anyway; taking the layer we need directly keeps the
  dependency surface smaller.
- Streaming is out of scope: a stream is neither one step nor one span, and a
  streaming method is refused by name at pre-run. Whether v1 grows a shape for
  it is issue #29.
