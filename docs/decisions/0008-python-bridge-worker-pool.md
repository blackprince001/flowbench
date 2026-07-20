---
type: Decision
title: "ADR 0008: Python logic steps run in a bridged worker pool"
description: Declarative flows execute on the pure-Go fast path; flows with Python logic steps route through a worker-pool bridge with an honestly lower VU ceiling.
status: Superseded
timestamp: 2026-07-10
---
# ADR 0008: Python logic steps run in a bridged worker pool

Status: Superseded by [ADR 0012](0012-no-runtime-bridge-shared-run-store.md)
(2026-07-20) — the runtime Go↔Python bridge and the general `logic` step are
dropped; declarative flows run on the Go engine and Python-driven flows write
to the same run store. The rest of this record is retained for history.

## Context

The executor is Go (ADR 0001) but Python is a primary authoring surface, including arbitrary `logic` steps. Embedding an interpreter versus bridging processes is the one hard engineering problem, and the SDK's ergonomics depend on it.

## Decision

Purely declarative flows — whether authored in YAML or in Python code that only constructs steps — execute entirely on the Go fast path at full VU scale. Flows containing `logic` steps execute those steps in a pool of Python worker processes bridged to the Go engine. The engine reports which path a scenario is on. The SDK's HTTP client auto-instruments so nested calls inside logic steps emit child spans — Python is never opaque in the graph.

## Consequences

- The bridged path caps practical VUs below the pure-Go path; this is documented, not hidden.
- The bridge mechanism (subprocess pool over gRPC/stdin vs embedded interpreter) is an open question; a prototype in M1 must land before the SDK surface freezes.
- Most stress cases are covered bridge-free, de-risking bridge underdelivery.
