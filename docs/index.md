okf_version: "0.1"

# FlowBench documentation

Entry point for the FlowBench knowledge bundle. Read this index first; open documents as needed.

## Product

* [PRD](prd.md) - Full product requirements document (v0.6 DRAFT); the source of truth for v1 scope.
* [CONTEXT.md](CONTEXT.md) - Ubiquitous-language glossary: Endpoint, Flow, Step, Profile, Scenario, Span, Run, Agent, and friends.

## Decisions

* [decisions/](decisions/) - Architecture decision records, one per major decision already made in the PRD:
  * [0001 Go engine with goroutine-per-VU concurrency](decisions/0001-go-engine-goroutine-per-vu.md) - Why Go and goroutine-per-VU for the 10k-VU target.
  * [0002 One canonical flow IR, two authoring surfaces](decisions/0002-one-ir-two-authoring-surfaces.md) - YAML and Python both compile to one IR; the executor accepts only the IR.
  * [0003 Four test categories as execution profiles](decisions/0003-test-categories-as-execution-profiles.md) - Integration/system/load/soak are profiles over the same flow.
  * [0004 Tooling packages, not a platform](decisions/0004-packages-not-platform.md) - Local-first binaries and packages; no hosted anything in v1.
  * [0005 No bespoke secrets mechanism](decisions/0005-no-bespoke-secrets.md) - Env vars and existing vaults; the engine only owns redaction.
  * [0006 Rate limiting as a first-class signal](decisions/0006-rate-limiting-first-class-signal.md) - `throttled` is its own outcome class with mode-aware semantics.
  * [0007 Span model as single source of truth](decisions/0007-span-model-single-source-of-truth.md) - One span model feeds both flame graphs and the waterfall view; two storage tiers.
  * [0008 Python logic steps via bridged worker pool](decisions/0008-python-bridge-worker-pool.md) - Pure-declarative fast path vs bridged Python path, honestly reported.
  * [0009 Prompt testing by observation](decisions/0009-prompt-testing-diff-first.md) - The flow's own SDK code makes LLM calls; FlowBench captures, hashes, paces, and diffs — no provider adapters, no scoring.
  * [0010 goccy/go-yaml for the YAML surface](decisions/0010-yaml-library-goccy.md) - The YAML library backing the declarative authoring surface.
  * [0011 Hand-rolled JSONPath subset](decisions/0011-jsonpath-hand-rolled-subset.md) - A small, dependency-free JSONPath subset for extraction and body assertions.
  * [0012 No runtime Go↔Python bridge](decisions/0012-no-runtime-bridge-shared-run-store.md) - Two independent producers sharing one run-store contract; no runtime bridge.
  * [0013 Arrival cap is a hard scheduling constraint](decisions/0013-arrival-cap-hard-scheduling-constraint.md) - The open-loop generator enforces the cap at the rate; the soft self-paced model undershot by ~11%.
  * [0014 Build the renderers in-house](decisions/0014-build-renderers-in-house.md) - Flame graph and waterfall are server-side HTML sharing one vocabulary, not vendored speedscope.
  * [0015 Live view and abort in one process](decisions/0015-live-view-and-abort.md) - `flowbench run --watch` streams an in-progress run over SSE and owns the one write path.
  * [0016 coder/websocket for the ws adapter](decisions/0016-websocket-coder-library.md) - Its handshake is an `http.Client` request, so auth, the allow-list and the phase spans carry over unchanged.
  * [0017 Target-metrics agent is scraped over HTTP, Linux-first](decisions/0017-agent-scrape-transport.md) - `flowbench-agent` serves `GET /metrics`; the CLI polls it on a ticker into the run store's own `agent.json` tier.
  * [0018 gRPC calls are made dynamically from .proto](decisions/0018-grpc-dynamic-from-proto.md) - Schemas are compiled at run time and invoked through `dynamicpb`; no code generation, and the response arrives as JSON so the rest of the engine applies unchanged.

## Planning

* [planning/milestones.md](planning/milestones.md) - M1–M4 milestone plan, mirrored as GitHub milestones and issues.

## Benchmarks

* [benchmarks/10k-vu-footprint.md](benchmarks/10k-vu-footprint.md) - Sustained 10k-VU generator CPU headroom and per-VU memory, with a re-runnable harness.

## History

* [log.md](log.md) - Decision/work log, newest first.
