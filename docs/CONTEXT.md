---
type: Reference
title: CONTEXT.md — FlowBench ubiquitous language
description: Glossary of FlowBench's domain terms, extracted from the PRD's conceptual model.
tags:
  - glossary
  - domain-model
timestamp: 2026-07-10
---
# CONTEXT.md

FlowBench is an internal, local-first toolkit for testing API endpoints and multi-step flows: one canonical flow definition, executed under four profiles by one Go engine. This glossary is the project's ubiquitous language; use these terms exactly, in code, docs, and issues.

## Language

### Authoring

**Endpoint**:
A declared, reusable target — protocol, method/operation, URL or address template, default headers, auth requirement.
_Avoid_: route, API

**Flow**:
An ordered, optionally branching sequence of Steps; the unit of authorship. Written once in YAML or Python, tested four ways.
_Avoid_: test case, collection, journey

**Step**:
The atomic unit of a flow. Types: `call` (HTTP/GraphQL/gRPC), `ws`, `logic` (Python hook), `wait`/`poll-until`, `verify` (database check). Can extract values, assert conditions, and carry a retry/backoff policy.
_Avoid_: request, task

**Extraction**:
Capturing a value from a step's response (JSONPath; later XPath) into a flow variable for injection into later steps.

**Assertion**:
A per-step condition on status, latency, headers, or body; failure behavior is configurable (`abort_flow | abort_run | record`) with mode-aware defaults.

**IR (canonical flow representation)**:
The single structure both authoring surfaces compile to and the only thing the executor accepts.
_Avoid_: AST, intermediate format

### Configuration

**Profile**:
The execution contract that turns one flow into any of the four test categories — mode (`integration | system | load | stress | soak`), VUs, ramp shape, duration or iteration count, thresholds, optional arrival cap.
_Avoid_: config, test type

**Scenario**:
One or more flows bound to a profile, a target config, and data pools. The runnable unit.
_Avoid_: suite

**Target Config**:
A lightweight named file (local/dev/staging) carrying base URLs, VU/RPS ceilings, and optionally an agent address. Never carries credentials.
_Avoid_: environment

**Data Pool**:
A fixture source (CSV/JSON) with a distribution policy (`unique-per-vu`, `round-robin`, `random`) so concurrent VUs draw distinct rows; also the sanctioned seeding mechanism.
_Avoid_: dataset, seed file

**Arrival cap**:
An optional, self-imposed request-rate ceiling on a profile, enforced by the planner ahead of the target, for testing at a known rate limit rather than only discovering one by flooding.

### Execution

**VU (virtual user)**:
One concurrent executor of iterations — a goroutine — with isolated variables, cookie jar, and data rows.

**Run**:
One execution of a scenario, producing aggregate metrics, threshold evaluations, per-iteration traces, folded flame data, and (if an agent is attached) target resource series. Records initiator, target, and flow-file git commit.

**Iteration**:
One VU's single pass through a flow, recorded as one trace.

**Outcome**:
The classification of a response at the point of assertion: `ok | failed | throttled | skipped`.

**Throttled**:
The outcome class for rate-limit responses (HTTP `429`, gRPC `RESOURCE_EXHAUSTED`, or an author-mapped status), tracked separately from `failed` via its own `throttle_rate` metric.
_Avoid_: rate-limit error

**Knee point**:
The concurrency level at which thresholds begin to fail during a stress ramp, classified as **degraded** (real capacity limit) or **throttled** (rate limiter engaging).

**Coordinated omission**:
The measurement error where a slow target quietly suppresses request attempts, flattering latency stats; the engine must account for it.

### Observation

**Span**:
The atomic unit of tracing — a named, timed node (step, protocol phase, extraction, assertion, poll or retry attempt) with a parent, start offset, duration, self-time, and outcome. Span names carry structural identity so folding across iterations and runs works.

**Trace**:
One iteration's spans assembled into a tree in causal order; the input to the waterfall view.

**Flame graph (of a flow)**:
A fold of many traces — spans with the same structural name collapsed and summed — rendered as width-proportional time, per iteration, per run, or cumulatively across runs. Answers "where does aggregate time go."

**Waterfall / trace view**:
A causal, per-iteration rendering of one trace's spans in start-offset order, like a browser performance panel. Answers "what exactly happened, in order, in this one iteration."

**Run store**:
The on-disk directory of run artifacts (folded flame data, raw trace trees, agent series, aggregates) that the results server and CLI read. No retention machinery; the user owns it.

**Results server**:
The small embedded server (`serve`, in the spirit of `go tool pprof -http`) reading the run store locally. Not a web app: no accounts, no persistence of its own, no write path beyond abort.

**Agent**:
The small standalone binary on target hosts streaming resource metrics (CPU, memory, network, descriptors) into a run, keyed to the run ID. Fails open — a dead agent never affects the run, only the overlay.

**Collector**:
The engine component that streams spans into the two storage tiers, ingests agent and engine self-metrics series, applies redaction, evaluates thresholds/trends, and classifies knee points.
