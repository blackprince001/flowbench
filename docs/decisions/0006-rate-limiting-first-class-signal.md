---
type: Decision
title: "ADR 0006: Rate limiting as a first-class signal"
description: Throttled responses form their own outcome class with mode-aware semantics, retry/backoff policies, and an optional arrival cap.
status: Accepted
timestamp: 2026-07-10
---
# ADR 0006: Rate limiting as a first-class signal

Status: Accepted (per PRD v0.4, sections 10.1, 10.3, 12)

## Context

Counting `429`s as generic errors makes a stress run against a rate-limited endpoint fail by design regardless of the target's actual capacity, and confuses a limiter doing its job with the target falling over.

## Decision

`throttled` (HTTP `429`, gRPC `RESOURCE_EXHAUSTED`, author-mapped statuses) is its own outcome class with its own `throttle_rate` metric. `call` steps may declare retry/backoff policies (`fixed`, `exponential`, `honor_retry_after`), each attempt emitting its own span. Profiles may declare a self-imposed arrival cap enforced by the planner. Stress knee points are classified **degraded** vs **throttled**, correlated against agent resource series.

## Consequences

- In integration/system modes a throttle is a failure by default; in load/stress/soak it is excluded from `error_rate` and reported separately.
- Retry loops are visible in the waterfall (one span per attempt) and time-to-success including backoff is reported, so retries cannot mask a capacity problem.
- Thresholds and knee findings stay meaningful against rate-limited targets.
