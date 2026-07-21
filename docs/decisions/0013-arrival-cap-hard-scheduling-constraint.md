---
type: Decision
title: "ADR 0013: Arrival cap is a hard scheduling constraint"
description: The profile-level arrival cap is enforced by an open-loop generator that dispatches at the rate, not approximated by per-VU self-pacing.
status: Accepted
timestamp: 2026-07-21
---
# ADR 0013: Arrival cap is a hard scheduling constraint

Status: Accepted (resolves the PRD §17 open question; unblocks #16)

## Context

A profile may declare an optional self-imposed arrival cap (`arrival_cap: 300/s`), independent of VU count, so a scenario can test behavior _at_ a known rate limit rather than only discovering one by flooding. The PRD left open whether the cap should be a **hard** constraint enforced at the scheduling layer, or a **soft** target the planner approximates under load.

Two enforcement models were prototyped against the executor and measured (`internal/executor/arrival_spike_test.go`, issue #15):

- **Hard — open-loop generator.** The scheduler owns arrival timing: requests launch on a fixed `1/N` schedule, decoupled from response time and VU count. This is the executor's existing open arrival model. Intended dispatch times are a pure function of the iteration index, so a backed-up target surfaces as latency rather than as omitted load (coordinated-omission correct).
- **Soft — self-paced closed loop.** VUs remain the controlled variable; each of `V` VUs paces itself to `N/V` req/s via a post-request sleep, the aggregate approximating `N/s` with no global coordination.

## Measurements

Target `1000 req/s` for 2s against a realistic stub (mostly 2 ms, every 8th response 100 ms), steady-state after a 400 ms warmup, 100 ms windows. Stable across repeated runs:

| Model | Mean rate | Peak window | CoV | Miss vs cap |
|---|---|---|---|---|
| Hard (open-loop) | 1000/s | 1000/s | ~0.00 | 0% |
| Soft (self-paced) | 889/s | 890/s | ~0.00 | **−11%** |

The soft model undershoots the cap by ~11% under the latency tail: a VU stuck in a slow response cannot hold its cadence, and with a fixed VU count nothing compensates, so the achieved rate is coupled to response time. Hitting the cap would require hand-tuning `V` to the target's latency (Little's law) — the opposite of "a known rate." The open-loop model holds the cap exactly because arrival timing is independent of how long any request takes.

## Decision

Enforce the arrival cap as a **hard scheduling constraint** via the open-loop generator. The rate is a first-class controlled variable dispatched on a fixed `1/N` schedule ahead of the target, decoupled from VU count and response time. Declaring `arrival_cap` switches the schedule to the open arrival model (already produced by the planner, ADR 0011-era work). The soft self-paced model is rejected.

## Consequences

- **#16** implements enforcement in the planner/executor: an `arrival_cap` selects the open model and the generator dispatches at `N/s` ahead of the target. Acceptance tolerance: steady-state observed rate within **±5%** of `N/s` given sufficient workers. The open-loop generator cannot exceed `N/s` by construction, so the cap is a true ceiling.
- **Sufficient workers required.** Sustaining `N/s` needs concurrency ≥ `N × latency` (Little's law). Under-provisioned, the generator falls behind and the rate honestly undershoots, surfaced as rising latency (CO-correct) rather than hidden. The planner should size/validate workers against the cap or report the shortfall.
- **Single-generator granularity.** At `1000/s` (1 ms spacing) accuracy is exact; very high rates (tens of thousands/s) may need batched or sharded generation. Revisit under the 10k-VU benchmark (#21).
- The measurement harness stays in the tree (skipped under `-short`) so the decision is reproducible.
