---
type: Reference
title: Milestone plan
description: The M1–M4 milestone plan from the PRD, mirrored as GitHub milestones and tracer-bullet issues.
tags:
  - planning
timestamp: 2026-07-12
---
# Milestone plan

Relative sequencing per the PRD (section 19); no dates until team size lands. Each milestone is mirrored as a GitHub milestone with tracer-bullet issues — thin vertical slices, each demoable, with blocking edges noted in issue bodies. M1+M2 alone replace the pytest-plus-Locust glue for HTTP services; M3 and M4 are where differentiation compounds. Under scope pressure, SOAP, Lua, and the demo DB defer first; the IR, chaining, profile mechanics, span emission, outcome classification, and results server are the protected core.

## M1: Engine core

Canonical IR, YAML parser/validator with file/line errors, HTTP adapter emitting per-phase spans, extract/assert/template chaining, `{{ env.* }}` resolution with redaction, data pools, target configs with the host-allow-list safety gate, and the CLI with integration mode (the local dev loop).

Exit: a chained YAML flow (login → extract token → act → assert) runs in integration mode against a local server with a per-phase span tree recorded.

## M2: The four modes

Planner (profile → VU schedule, open/closed arrival, arrival cap), goroutine-per-VU executor toward the 10k benchmark, load/stress/soak profiles, retry/backoff execution with per-attempt spans, outcome classification (`ok/failed/throttled/skipped`) with mode-aware defaults, thresholds and soak trend evaluation, two-tier span storage (folded + raw trace trees), capture policy and redaction at scale, run store with attribution, safety rails and clean abort.

Exit: the same flow runs under all five modes; a stress run against a rate-limited stub reports `throttle_rate` separately from `error_rate`.

## M3: Python SDK + protocols + agent

Engine as importable Python package (declarative flows compiling to the IR, plus a Python-driven execution path that writes runs — ADR 0012), SDK-side HTTP auto-instrumentation, flows runnable via CLI and `python file.py`, GraphQL, WebSockets, gRPC unary (`RESOURCE_EXHAUSTED` → `throttled`), prompt-observation API (wrap the team's own LLM SDK calls in Python-driven flows: always-on prompt/completion capture, identity hashing, variant labels, in-process pace/timeout guards — ADR 0009), auth scheme coverage, agent v1 with collector time-alignment and engine self-metrics.

Exit: a Python-driven flow renders fully resolved spans in the run store; an agent-attached run overlays target CPU/memory; a flow wrapping its own SDK call records per-variant prompt/completion pairs, paced under a declared ceiling.

## M4: Results server

`serve` command over the run store, flame graphs (single + cumulative) and the waterfall/trace view over the same span data, dashboards with agent overlays and throttle-rate charting, failure drill-down grouped by step/cause with `throttled` as its own group, degraded-vs-throttled knee-point reporting, regression comparison, prompt diff view (variant vs variant within a run, run vs baseline; text and structural JSON diffs, prompt-hash change surfaced — ADR 0009), soak trend view, live view, hardening, quickstart docs, dogfood exit.

Exit: rollout entry criteria — conformance suite green across both surfaces, 10k-VU benchmark met, a stress finding reproduced with the flame graph pointing at the right step including one correctly-identified throttled knee.
