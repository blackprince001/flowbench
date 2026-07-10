---
type: Decision
title: "ADR 0003: Four test categories as execution profiles"
description: Integration, system, load/stress, and soak are execution profiles applied to the same flow, not separate test kinds.
status: Accepted
timestamp: 2026-07-10
---
# ADR 0003: Four test categories as execution profiles

Status: Accepted (per PRD v0.4, section 12 — the load-bearing design decision)

## Context

Functional, integration, and load tests conventionally live in different tools with different syntaxes; swapping tools to repeat a flow under concurrency loses the flow definition and the forensics.

## Decision

A profile — mode (`integration | system | load | stress | soak`), VUs, ramp, duration, thresholds, optional arrival cap — is the execution contract attached to a scenario. The same flow becomes any of the four categories by swapping a profile block.

## Consequences

- Failure semantics differ by mode: assertion failures are loud in integration/system, data in load/stress. `on_failure` defaults per mode, overridable per step.
- `throttled` counts as a failure in integration/system, is excluded from `error_rate` in load/stress/soak (see ADR 0006).
- The collector changes posture by mode: point thresholds (load/stress), windowed trends (soak), binary assertions (integration/system), knee-point classification (stress).
