---
type: Decision
title: "ADR 0001: Go engine with goroutine-per-VU concurrency"
description: The execution engine is written in Go, one goroutine per virtual user, sized for 10k concurrent VUs on a single node.
status: Accepted
timestamp: 2026-07-10
---
# ADR 0001: Go engine with goroutine-per-VU concurrency

Status: Accepted (per PRD v0.4, sections 10.5, 13, 14)

## Context

The toolkit must sustain 10,000 concurrent virtual users on a single reference node with honest measurement (coordinated-omission awareness, generator self-metrics proving headroom). Python-bound generators (Locust) hit a VU ceiling; JS engines (k6) preclude the desired Python authoring surface.

## Decision

The executor, scheduler, protocol adapters, and collector are written in Go. Each VU is a goroutine with its own cookie jar, data row, and variable scope. The engine records its own resource series so generator saturation is always distinguishable from target saturation.

## Consequences

- 10k VUs is feasible on one node for pure-declarative flows; distribution is deferred to v2.
- Python enters only via the authoring surface and a bridged worker pool for `logic` steps (see ADR 0008), which has a lower, honestly-reported VU ceiling.
- Per-VU memory overhead must stay low enough for hundreds of VUs on a laptop; this is a guardrail metric.
