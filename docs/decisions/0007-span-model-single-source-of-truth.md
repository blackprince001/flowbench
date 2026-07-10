---
type: Decision
title: "ADR 0007: Span model as single source of truth, two storage tiers"
description: One span model powers both flame graphs and the waterfall view; storage is folded aggregates plus policy-captured raw trace trees.
status: Accepted
timestamp: 2026-07-10
---
# ADR 0007: Span model as single source of truth, two storage tiers

Status: Accepted (per PRD v0.4, sections 9, 10.5, 10.7)

## Context

Two inspection experiences are needed — aggregate "where does time go" (flame graphs) and causal "what exactly happened" (waterfall) — and raw spans at 10k VUs would overwhelm storage if kept wholesale.

## Decision

Every step, protocol phase (dns/connect/tls/ttfb/transfer), extraction, assertion, and poll/retry attempt emits a span (name, parent, start offset, duration, self-time, outcome); one iteration's spans form a trace tree. Storage is two tiers: (1) folded aggregates per structural span-path, updated incrementally, unbounded-VU-safe, the sole input to flame graphs; (2) raw trace trees kept per capture policy — all failures plus a configurable sample of successes and throttled responses, bodies size-capped and redacted — the sole input to the waterfall view.

## Consequences

- Debugging and profiling never disagree, because they read the same spans.
- Span names carry structural identity; the parser warns on step renames that would silently break cross-run folding.
- Span storage encoding (OTel-compatible vs bespoke compact) remains an open engine-design question, deliberately deferred.
