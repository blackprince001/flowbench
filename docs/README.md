# FlowBench

FlowBench is a scripting-first API and flow testing toolkit: one canonical flow, authored in YAML or Python, executed by one Go engine under four execution profiles — integration, system, load/stress, and soak. It is a set of local-first tooling packages, not a hosted platform.

A **flow** chains steps by extracting values from one response and injecting them into later requests: login, take the token, act, assert. Every step, protocol phase, extraction, assertion, and retry attempt emits a span, and one span model powers both the flame graph (where aggregate time goes) and the waterfall (what exactly happened in one iteration). Rate limiting is a first-class signal — `throttled` is its own outcome class, never folded into `failed` — and a lightweight agent on the target host streams CPU and memory into the run so target saturation, generator saturation, and knee points are never confused.

Flows can also observe prompt calls: your code calls whatever LLM SDK it already uses inside a scripted step, and FlowBench wraps the call — capturing every prompt and completion, keeping prompt variants separately comparable, and diffing completions across variants and against baseline runs. Prompt changes get reviewed like code changes, not eyeballed in a playground.

## The deliverables

| Artifact | Language | What it is |
| --- | --- | --- |
| `flowbench` (engine + CLI) | Go | Parser, planner, goroutine-per-VU executor, collector, run store; results server embedded |
| `flowbench-agent` | Go | Streams target-host resource metrics into runs (Linux) |
| `flowbench` Python SDK | Python | The authoring surface as an importable package; flows run like normal test files |
| YAML DSL | — | Declarative authoring surface compiling to the same canonical IR as Python |

## Where to start

- New here: [Installation](getting-started/installation.md), then the [Quickstart](getting-started/quickstart.md) — one YAML flow to a first stress run and flame graph in under ten minutes.
- Writing flows: the [Guide](guide/flows.md) walks the authoring surface end to end; the [Cookbook](cookbook/README.md) holds worked patterns, each runnable from `examples/`.
- Looking something up: the [CLI](reference/cli.md), [YAML DSL](reference/yaml-dsl.md), and [Python API](reference/python-api.md) references are complete.
- Why is it built this way: [Architecture](project/architecture.md), the [PRD](prd.md), and the nineteen [decision records](decisions/README.md).

One vocabulary runs through everything — a flow is never a "test case", a throttle is never an "error". The [glossary](CONTEXT.md) is the source of truth for the words.
