# Decision records

Every choice that shaped the toolkit, with its context and consequences. One file per decision; superseded records stay, marked, because the reasoning that was replaced is part of the record.

| # | Decision | Status |
| --- | --- | --- |
| [0001](0001-go-engine-goroutine-per-vu.md) | Go engine with goroutine-per-VU concurrency | Accepted |
| [0002](0002-one-ir-two-authoring-surfaces.md) | One canonical flow IR, two authoring surfaces | Accepted |
| [0003](0003-test-categories-as-execution-profiles.md) | Four test categories as execution profiles | Accepted |
| [0004](0004-packages-not-platform.md) | Tooling packages, not a platform | Accepted |
| [0005](0005-no-bespoke-secrets.md) | No bespoke secrets mechanism | Accepted |
| [0006](0006-rate-limiting-first-class-signal.md) | Rate limiting as a first-class signal | Accepted |
| [0007](0007-span-model-single-source-of-truth.md) | Span model as single source of truth, two storage tiers | Accepted |
| [0008](0008-python-bridge-worker-pool.md) | Python logic steps run in a bridged worker pool | Superseded by 0012 |
| [0009](0009-prompt-testing-diff-first.md) | Prompt testing by observation — diff-first, never owning the LLM call | Accepted |
| [0010](0010-yaml-library-goccy.md) | goccy/go-yaml for the YAML authoring surface | Accepted |
| [0011](0011-jsonpath-hand-rolled-subset.md) | Hand-rolled JSONPath subset | Accepted |
| [0012](0012-no-runtime-bridge-shared-run-store.md) | No runtime Go↔Python bridge; two producers, one run store | Accepted |
| [0013](0013-arrival-cap-hard-scheduling-constraint.md) | Arrival cap is a hard scheduling constraint | Accepted |
| [0014](0014-build-renderers-in-house.md) | Build the flame-graph and waterfall renderers in-house | Accepted |
| [0015](0015-live-view-and-abort.md) | Live view and server-side abort in one process, over SSE | Accepted |
| [0016](0016-websocket-coder-library.md) | coder/websocket for the WebSocket adapter | Accepted |
| [0017](0017-agent-scrape-transport.md) | Target-metrics agent is scraped over HTTP, Linux-first | Accepted |
| [0018](0018-grpc-dynamic-from-proto.md) | gRPC calls made dynamically from .proto, no codegen | Accepted |
| [0019](0019-grpc-streaming-out-of-v1.md) | gRPC streaming is out of v1 | Accepted |
