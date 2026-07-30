---
type: Decision
title: "ADR 0019: gRPC streaming is out of v1"
description: Unary only. The step/span model could carry streaming — the WebSocket slice already built the shape — but the profile model cannot say what a long-lived stream measures, and no one has asked for it.
status: Accepted
timestamp: 2026-07-30
---
# ADR 0019: gRPC streaming is out of v1

Status: Accepted (resolves the PRD §17 open question; closes spike #29)

## Context

The gRPC adapter (#28, ADR 0018) executes unary calls and refuses streaming methods before the run starts, listing the unary alternatives. The PRD scoped streaming as "later" with an open question against it, on the stated grounds that **the step/span model is call-shaped**: a step builds a request, gets a response, extracts, asserts, and returns.

That objection has since expired. The WebSocket slice (#27) already grew the two structural pieces streaming needs:

- **A session outliving the step that opened it.** Sessions live on the `Iteration`, `RunFlow` closes them in a `defer`, and the validator resolves the session graph before the run — a step working on a session no earlier step opened is a pre-run error with file:line.
- **An arriving message is not a response.** `match` selects the message the step is about, `assert` judges the one match selected, and everything else is `skipped` rather than failed on.

So the honest finding of this spike is that **the model is no longer the blocker**. A server-streaming call maps onto `ws_open` + matched receives almost exactly; the response is already JSON (ADR 0018), so extraction, assertions, capture, redaction and folding apply unchanged, as they did for unary. If the question were only "does it fit", the answer would be yes.

Three things make it a no anyway.

**1. The profile model cannot say what a long-lived stream measures.** A run reports iterations/sec, per-flow-run latency percentiles, and an optional arrival cap. Those describe a workload of discrete flow-runs. A bidi stream held open for minutes turns 500 VUs into 500 concurrent streams, and every number the tool reports about that run stops describing anything: throughput counts stream *opens*, p95 measures the flow-run that contained a stream rather than the stream, and the arrival cap governs openings rather than traffic. WebSocket sessions do not have this problem in practice because they are iteration-scoped by construction — an exchange, not a subscription. Streaming's characteristic use is the subscription. Answering this properly means a second vocabulary for "what a run measured", which is a far larger change than an adapter.

**2. Three kinds, not one.** Server streaming (open, then receive), client streaming (open, send repeatedly, close and read the single result), and bidi need distinct step semantics, in both authoring surfaces, with a conformance fixture apiece and a dynamic streaming stub (`internal/grpcstub` has no code generation to lean on — ADR 0018). Two further items are not free: a stream's **status arrives in the trailers**, so a `RESOURCE_EXHAUSTED` shed mid-flight is only discovered at close and would be attributed to whichever step drains the stream rather than the one that caused it; and **client streaming blocks on HTTP/2 flow control**, so a VU stalled in `SendMsg` is indistinguishable from a slow call unless blocked-on-flow-control becomes a span of its own.

**3. Nobody has asked.** This spike exists because demand was unknown. Assessed rather than surveyed: streaming RPCs in an internal estate cluster around telemetry pipelines, log tailing, pub/sub bridges and model inference — infrastructure that is usually covered by its own integration suites rather than by an API-flow test. The streaming case a team is most likely to bring to FlowBench today is token-by-token model output, and that is overwhelmingly HTTP SSE rather than gRPC, so it lands on the Python-driven path where the flow's own client is auto-instrumented (#24, #43).

## Decision

**gRPC support in v1 is unary only.** A streaming method stays a pre-run error naming the unary alternatives, which is where a flow author meets this decision.

The mapping is recorded here so that adding it later is additive rather than a redesign:

| Kind | Steps | Spans |
| --- | --- | --- |
| Server streaming | one step opens and names a stream; later steps receive with `match`, as `ws` does | `grpc_stream` open span; a `recv` span per matched message, `skipped` for the passed-over ones |
| Client streaming | one step opens; later steps send; a terminal step closes and reads the single response | `grpc_stream`; a `send` span each; `recv` on the close step, which carries the status |
| Bidi | the union, with send and receive independent | as above, interleaved |

In every kind the stream is iteration-scoped and closed in the same `defer` that closes WebSocket sessions, the status is read at close, and `RESOURCE_EXHAUSTED` classifies as `throttled` (ADR 0006) wherever it lands.

**What would reopen this:** a team with a streaming RPC inside a flow they actually want to test. That is a concrete trigger rather than a date — and if it arrives, server streaming alone is the slice to build, since it is the common kind and the one the existing `match`/`skip` machinery already fits.

## Consequences

- The unary adapter's promise stays narrow and true; nothing in the engine has to pretend a stream is a call.
- The `match`/`skip` machinery stays inside the ws adapter. Generalizing it protocol-neutrally is real work with regression risk against a shipped feature, and it is not paid for by a feature nobody has asked for.
- **There is no escape hatch, and it should not be presented as one.** A Python-driven flow can call a streaming RPC through the team's own generated stubs, but `grpc-python` does not travel over httpx, so auto-instrumentation records nothing and the step earns no span — the "a step makes a request or records an observation" rule fails it. A flow that must exercise a streaming RPC in v1 does it outside FlowBench.
- The PRD's §17 open question closes. §10.2's gRPC bullet now states unary-only as a decision rather than as a deferral.
