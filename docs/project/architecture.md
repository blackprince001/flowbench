# Architecture

FlowBench is tooling packages, not a platform (ADR 0004): two binaries and a Python package, everything local-first, plain files where a service would put a database. This page is the system in one pass; the [decision records](../decisions/README.md) carry the full reasoning behind each choice.

## The pipeline

```text
YAML ──parse──┐
              ├──> canonical IR ──validate──> plan ──execute──> collect ──> run store ──> serve
Python ─compile┘
```

- **Parser** (`internal/parser`) — an AST walk over the YAML (goccy/go-yaml, chosen for position data — ADR 0010), so every authoring error carries file:line:col and is caught before load exists.
- **IR** (`internal/ir`) — the one canonical flow representation both surfaces compile to (ADR 0002). The executor accepts only IR; nothing is expressible in one surface but not the other, and a conformance suite holds the two to byte-identical output.
- **Planner** (`internal/planner`) — profile → VU schedule: ramp segments, hold, open/closed arrival, the arrival cap as a hard scheduling constraint (ADR 0013), and the safety ceilings check against the target config.
- **Executor** (`internal/executor`) — goroutine-per-VU (ADR 0001), sized for 10k VUs on one node with generator headroom proved by [the footprint benchmark](../benchmarks/10k-vu-footprint.md). Adapters (`internal/adapters`) emit per-phase spans for HTTP, GraphQL, WebSocket, and gRPC (dynamic from `.proto` at run time — ADR 0018). The executor also samples its own CPU/heap, so generator saturation is a measured thing, not a suspicion.
- **Collector** (`internal/collector`) — folds spans into the two storage tiers, applies redaction, evaluates thresholds, soak trends, and classifies the stress knee against the agent series.
- **Run store** (`internal/store`) — a directory per run, JSON tiers, an index; attribution (who, what, target, commit, dirty) on every run. No retention machinery (ADR 0007).
- **Results server** (`internal/server`, `internal/report`) — embedded in the CLI, server-rendered HTML with renderers built in-house (ADR 0014), read-only over the store except the live view's abort (ADR 0015).
- **Agent** (`internal/agent`, `cmd/flowbench-agent`) — scraped over HTTP (ADR 0017), Linux `/proc` sampling, polled fail-open into `agent.json`.

## The span model is the contract

Every step, phase, extraction, assertion, retry attempt, and prompt observation emits a span; one model feeds both the flame graph (folded tier) and the waterfall (raw tier) — ADR 0007. This is also the boundary between the two producers: there is **no runtime bridge between Go and Python** (ADR 0012, superseding 0008). Python-driven runs execute in-process and write the same span tiers to the same store; the store format — not an RPC surface — is what the two sides agree on.

## Rate limiting as a signal

`throttled` is its own outcome class end to end (ADR 0006): the adapters classify it (HTTP 429, WS 1013, gRPC RESOURCE_EXHAUSTED), the collector rates it separately, thresholds gate it separately, the report draws it in its own color, and the stress knee classifier uses its movement against the agent's resource series to tell an enforced limit from genuine saturation.

## Prompt observation is observation

FlowBench never makes an LLM call and never sets model behavior (ADR 0009). Your code calls its own SDK inside a Python step; the observation wraps it — capture, hash, pace, diff. No provider adapters, no scoring, no judge models in v1.

## What is deliberately not here

Teams, notifications, hosting, CI integration, scheduling — v2 concerns, out of the packages by design (ADR 0004). No bespoke secrets store (ADR 0005): the environment supplies credentials, the engine owns only redaction. gRPC streaming is out of v1 (ADR 0019).
