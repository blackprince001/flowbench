---
type: Decision
title: "ADR 0012: No runtime Go↔Python bridge; two producers, one run store"
description: The engine drops the Go→Python worker-pool bridge and the general logic step; declarative flows run on the Go engine and Python-driven flows write to the same run store, the two meeting only at the span model.
status: Accepted
timestamp: 2026-07-20
---
# ADR 0012: No runtime Go↔Python bridge; two producers, one run store

Status: Accepted — supersedes ADR 0008

## Context

ADR 0008 split execution into a pure-Go fast path and a bridged path, where
Python `logic` steps ran in a pool of worker processes the Go engine called
into at run time. That bridge — a bidirectional Go↔Python runtime coupling
with worker-pool sizing, backpressure, and cross-process pace-guard
coordination — was the project's single hardest engineering problem and its
largest delivery risk (PRD §17), and issue #10 was a spike to prove it before
the SDK surface froze.

Reviewing that risk against what the bridge actually buys: the capability it
unlocks is arbitrary Python running mid-flow. The declarative fast path
already covers integration, system, load, stress, and soak for HTTP and the
other protocol adapters without it. The one feature that genuinely needs the
team's Python at run time — prompt observation, which watches the team's own
LLM SDK call — is Python-surface-only and low-volume by nature (deliberately
paced, integration-style). Paying the bridge's cost and risk to serve that one
Python-only, low-concurrency case is the wrong trade.

## Decision

Drop the runtime bridge and the general `logic` step. Execution has two
independent producers that share one contract — the span model and the on-disk
run store:

- **Go engine** (`flowbench run`): YAML flows, and Python that only
  *constructs* the IR, execute entirely in Go at full VU scale. No Python runs
  at execution time.
- **Python SDK** (`python file.py`): a Python-driven flow makes its own
  protocol calls (SDK-side auto-instrumentation) and, where present, the team's
  LLM call wrapped for observation, emitting spans in the shared model and
  writing a run to the run store. It runs at Python concurrency, not the Go
  engine's ceiling.

The two paths never call into each other. The results server reads the run
store uniformly, so flame graphs, the waterfall view, and prompt diffs work
across both producers.

## Consequences

- The hardest, riskiest subsystem is removed: no bidirectional bridge, no
  worker-pool sizing, no backpressure protocol, and no cross-process pace-guard
  coordination. Pace guards now coordinate in-process within the Python driver.
- Prompt observation survives, re-homed to the Python-driven path (ADR 0009 is
  rewritten). It carries an honest ceiling — prompt-observing runs are
  integration/diff-scale, not 10k-VU — consistent with its Python-surface-only,
  deliberately-paced design.
- Load and stress stay bridge-free on the Go engine, exactly as before; nothing
  that runs there today regresses.
- Two HTTP instrumentation surfaces exist by design: the Go adapters for the
  engine path, and SDK-side instrumentation (httpx/requests) for the Python
  path. Both emit the same span shape, so the run store and results server do
  not care which produced a run.
- The run store's format becomes a load-bearing public contract between two
  producers, not just an engine output. It must be versioned and validated on
  read.
- `logic` leaves the IR (`StepLogic`/`LogicSpec` removed). The open string
  enums keep this additive to reintroduce if a future version decides runtime
  Python earns the bridge after all.
- Rejected: keeping a narrow Python↔Go call bridge only for observation — it
  still couples the two runtimes and re-introduces the coordination problem for
  a fraction of the benefit; the shared-run-store contract is simpler and is
  already the single source of truth.
- Closed by this decision: #10 (bridge spike), #23 (logic-step bridge), #24
  (auto-instrumentation inside logic steps). PRD §17's Go↔Python bridge open
  question and its risk row are resolved by removal.
