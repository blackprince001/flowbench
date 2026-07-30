# Changelog

Human-curated, newest first. Versions are tags; the tag is the source of truth. Until v1, minor versions may change surfaces without a deprecation cycle — the run-store format written by the two producers (Go engine, Python SDK) is the compatibility contract to watch.

## v0.1.0 — 2026-07-30

The first cut of the toolkit: everything from milestones M1–M4, dogfood-ready per the [rollout checklist](docs/project/rollout-checklist.md).

### Engine + CLI (`flowbench`)

- One canonical flow IR with two authoring surfaces — YAML DSL and Python — held byte-identical by a conformance suite; the engine executes only IR.
- Five execution profiles over the same flow: integration, system, load, stress, soak. Goroutine-per-VU executor benchmarked at 10k VUs on one node with 70% generator headroom.
- Protocols: HTTP, GraphQL, WebSocket sessions, gRPC unary (dynamic from `.proto`, no codegen). Auth: bearer, basic, api_key, cookie, OAuth2 client credentials, HMAC. Data pools, retry/backoff with per-attempt spans, read-only Postgres `verify` steps.
- Rate limiting as a first-class signal: `throttled` is its own outcome class (HTTP 429, WS 1013, gRPC RESOURCE_EXHAUSTED), with mode-aware semantics and its own `throttle_rate` thresholds; the arrival cap is a hard open-loop scheduling constraint and latency is coordinated-omission-aware.
- Thresholds as the run's persisted verdict (exit codes 0/1/2), soak trend evaluation, and stress knee classification: a breaching stress run is classified `degraded` vs `throttled` (vs `inconclusive`) by correlating quality-rate windows against the agent's resource series.
- Safety rails: host allow-list, VU/RPS ceilings, disallowed modes, request timeouts, clean abort.
- The run store: plain directories with attribution (who/what/target/commit), folded + raw span tiers, bucketed series, self-metrics, agent series.
- The embedded results server: run overview with gates/trend/knee cards, time-series tab, flame graph, waterfall, failures drill-down, outcomes grid, run-vs-baseline compare, prompt diff, live view with server-side abort (`run --watch`).

### Agent (`flowbench-agent`)

- One-endpoint scrape target (`GET /metrics`) streaming host CPU/memory/net/fds into runs; polled fail-open by the engine. Host sampling is Linux-only in v1.

### Python SDK (`flowbench`, 0.1.0)

- The authoring surface as an importable package: `Flow`, `@flow.step`, `expect`, profiles, auth, retries; compiles to the IR for engine execution, or runs integration/system flows directly (`python flow.py`) into the same run store.
- httpx auto-instrumentation emitting the engine's span shape; prompt observation with variants, pace guards, usage normalization, and hashing for the diff-first prompt-testing workflow.

### Docs

- A GitBook-layout book under `docs/`: quickstart, guide, CLI/YAML/Python references, cookbook, architecture, and the dogfood rollout checklist.
