<img src="docs/assets/logo.svg" width="52" alt="">

# FlowBench

**Scripting-first API and flow testing toolkit. One flow, four execution profiles: integration, system, load/stress, soak.**

## TL;DR

FlowBench is an internal, local-first testing toolkit for API endpoints and multi-step flows. One canonical flow representation is authored either in a declarative YAML DSL or in Python (the engine shipped as an importable package), and executed by one Go engine built on goroutine-per-VU concurrency, targeting 10k concurrent virtual users on a single node. The four test categories — integration, system, load/stress, and soak — are four **execution profiles** applied to the same flow.

Flows chain steps by extracting values from one response and injecting them into later requests (login → take token → act → assert). Every step, protocol phase, extraction, assertion, and retry attempt emits a span; one span model powers both **flame graphs** (where does aggregate time go) and a **waterfall/trace view** (what exactly happened in one iteration). Rate limiting is a first-class signal (`throttled` is its own outcome class, never folded into `failed`), and a lightweight **agent** on the system under test streams CPU/memory into the run so target saturation, generator saturation, and knee points are never confused.

Flows can also **observe prompt calls**: your own code calls whatever LLM SDK or framework it already uses inside a scripted step, and FlowBench wraps the call — never making it or setting model behavior. Every prompt and completion is captured, variant labels keep prompt versions separately comparable, pace/timeout guards keep repeated calls under provider rate limits, and the results server **diffs completions** across variants and against baseline runs — so prompt changes are reviewed like code changes, not eyeballed in a playground (diff-and-assert; no LLM-as-judge scoring in v1).

This is a set of tooling packages, not a hosted platform: Go engine + CLI, Python SDK, YAML DSL, embedded results server, target-metrics agent. Teams, notifications, CI gating, and hosting are explicitly v2.

## Deliverables

| Artifact | Language | What it is |
| --- | --- | --- |
| Engine + CLI binary | Go | Parser, planner, goroutine-per-VU executor, collector, run store; results server embedded |
| Python SDK | Python | Engine as an importable package; granular hooks; flows run like normal test files |
| Agent binary | Go | Streams target-host resource metrics into runs |
| YAML DSL | — | Declarative authoring surface compiling to the same canonical IR as Python |

## Documentation

- [docs/index.md](docs/index.md) — documentation map (start here)
- [docs/prd.md](docs/prd.md) — the full PRD (source of truth for scope)
- [docs/CONTEXT.md](docs/CONTEXT.md) — ubiquitous-language glossary
- [docs/decisions/](docs/decisions/) — architecture decision records
- [docs/planning/milestones.md](docs/planning/milestones.md) — milestone plan (mirrored on GitHub)
- [docs/log.md](docs/log.md) — decision/work log
