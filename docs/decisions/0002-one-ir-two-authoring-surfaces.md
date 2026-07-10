---
type: Decision
title: "ADR 0002: One canonical flow IR, two authoring surfaces"
description: YAML DSL and Python SDK both compile to a single canonical flow representation; the executor accepts only the IR.
status: Accepted
timestamp: 2026-07-10
---
# ADR 0002: One canonical flow IR, two authoring surfaces

Status: Accepted (per PRD v0.4, sections 3, 11, 17)

## Context

The core problem being solved is that the same flow gets written two or three times across tools and the versions drift. Users want both a declarative common path (YAML) and granular programmatic control (Python).

## Decision

Both surfaces produce one canonical flow representation (the IR); the engine only ever sees the canonical form. The YAML DSL covers the common path; Python can go beyond it (conditionals, loops, computed payloads, custom extraction) while remaining schedulable by the same executor.

## Consequences

- The flow that passed integration testing is byte-for-byte the flow being stress tested — no translation loss.
- Drift between the surfaces is the top risk; mitigation is a conformance suite that runs every DSL feature through both surfaces (a rollout entry criterion).
- The IR, not either surface, is the protected core under scope pressure.
