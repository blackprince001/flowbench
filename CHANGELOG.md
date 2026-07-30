# Changelog

Human-curated, newest first. Versions are tags; the tag is the source of truth. Until v1, minor versions may change surfaces without a deprecation cycle — the run-store format written by the two producers (Go engine, Python SDK) is the compatibility contract to watch.

## v0.1.1 — 2026-07-30

### Added

- **The Python SDK is on PyPI: `pip install flowbench`.** Installing it previously meant downloading a wheel off a GitHub release or pointing pip at a checkout. Publishing uses PyPI's Trusted Publishing, so there is no API token in the repository's secrets (ADR 0005's reasoning, applied to a new credential). The wheel and sdist stay attached to the release too, and the published bytes are the same ones the checksums cover.
- **The project is licensed Apache 2.0.** There was no `LICENSE` file at all; there is now one at the root and a copy inside the SDK package, so the license travels with the wheel.

### Fixed

- **A step with a `body` now says it is sending JSON.** The engine sent request bodies with no `Content-Type` at all, so a target that requires one answered `415`, and one that quietly ignores the body reported a confusing `400` — while the same flow run through `python flow.py` sent `application/json`, because httpx added it. The two surfaces now agree, and a conformance test reads the headers off the wire so they stay that way.

### Changed

- **flowbench identifies itself.** Requests carried Go's default `Go-http-client/1.1`; they now carry `flowbench/<version>` (`flowbench/<version> (python)` from the SDK's direct-execution path). If you have a WAF rule, an access-log filter, or an allow-list keyed on the old value, update it.
- Both headers are only added when the step does not declare them, so an explicit `headers:` entry still wins. Declaring one **empty** (`Content-Type: ""`) sends no such header at all — the way to test what a target does without one. See the [YAML reference](docs/reference/yaml-dsl.md#headers-the-engine-adds).

The run store is untouched: captures record request bodies and responses, never request headers, so runs written by v0.1.0 and v0.1.1 stay comparable.

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
