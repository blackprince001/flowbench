# Concepts

FlowBench has one vocabulary, used the same way in the CLI, the report, the code, and these docs. This page is the working tour; the [glossary](../CONTEXT.md) is the authoritative list, including the words each term deliberately avoids.

## Flows, steps, and scenarios

A **flow** is an ordered chain of steps modelling one user-meaningful journey — login, add to cart, pay. A **step** is one action inside it: an HTTP `call`, a `graphql` operation, a `ws` send/receive, a `grpc` call, a `wait`, or a `poll`. Steps chain through **extraction** (pull a value out of a response by JSONPath, header, or status into a named variable) and **templating** (inject `{{ variables }}` into later requests). **Assertions** judge a response; a failed assertion marks the flow-run failed and, by default, skips the remaining steps.

A **scenario** is a flow plus its **profile** — the execution contract. The same flow runs under any profile; the four test categories are not four test suites but four profiles over one flow (ADR 0003):

| Mode | Question it answers |
| --- | --- |
| `integration` | does the chain work at all — one VU, once, loud failures |
| `system` | does it work at the author's declared concurrency, once |
| `load` | does it hold a known rate — ramp, hold, thresholds |
| `stress` | where does it break, and why — push past the ceiling |
| `soak` | does it degrade over hours — drift, creep, leaks |

## Execution

A **VU** (virtual user) is one concurrent executor of the flow — one goroutine in the engine. An **iteration** is one complete pass of a VU through the flow, also called a **flow-run**. A **run** is one execution of a scenario: the unit that is saved, listed, compared, and served.

Every flow-run lands in exactly one **outcome**: `ok`, `failed`, `skipped` — and `throttled` is tracked apart from all three, because rate limiting is a property of the exchange, not a verdict on your code (ADR 0006). In load/stress/soak a throttled flow-run's outcome *is* `throttled`; in integration/system it also counts as failed, since a smoke test that got throttled did not pass. HTTP `429`, WebSocket close `1013`, and gRPC `RESOURCE_EXHAUSTED` are all the same signal.

The **arrival cap** makes load open-loop: dispatch at the declared rate whether or not the target keeps up. That is what separates "the target slowed down" from "the generator slowed down with it" — and latency is measured coordinated-omission-aware, from intended dispatch, so a stalled target cannot hide its own queue.

The **knee point** is where a stress ramp breaks its thresholds — and with an [agent](../guide/agent.md) attached, the run says *why*: **degraded** (latency/errors broke while target resources climbed — genuine saturation) or **throttled** (throttle rate rose over flat resources — an enforced limit).

## Observation

Every step, protocol phase, extraction, assertion, and retry attempt emits a **span**. One span model is the single source of truth (ADR 0007), stored in two tiers: folded aggregates for the **flame graph** (where aggregate time goes) and sampled raw **traces** for the **waterfall** (what exactly happened in one iteration). The **collector** builds those tiers, applies redaction, evaluates thresholds and soak trends, and classifies the stress knee.

Runs are written to the **run store** — a plain directory (`runs/` by default) you own; there is no database and no retention machinery. The **results server** (`flowbench serve`) renders it read-only in a browser. The **agent** is a small binary on the target host streaming CPU/memory into the run, so target saturation and generator saturation are never confused.

## Prompt observation

A **prompt observation** wraps an LLM call *your* code makes inside a Python step — FlowBench never makes the call and never sets model behavior (ADR 0009). Each observation captures the prompt and **completion**, hashes the prompt's identity, and a **variant** label gives each prompt version its own span identity so versions are separately comparable. A **pace guard** keeps repeated calls under provider rate limits. The **prompt diff** view compares completions across variants and against baseline runs — diff-and-assert, no scoring.

## Two authoring surfaces, one IR

YAML and the Python SDK both compile to the same canonical **IR** (intermediate representation), and the engine executes only IR (ADR 0002). A conformance suite holds the two surfaces to byte-identical output, so nothing is expressible in one surface but not the other. Python-driven execution (running `python flow.py` directly) is the second producer into the same run store (ADR 0012), restricted to integration/system modes.

Next: [authoring flows](../guide/flows.md).
