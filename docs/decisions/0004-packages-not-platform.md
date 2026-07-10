---
type: Decision
title: "ADR 0004: Tooling packages, not a platform"
description: v1 ships binaries and packages only — no hosted service, accounts, teams, CI integration, scheduling, or notifications.
status: Accepted
timestamp: 2026-07-10
---
# ADR 0004: Tooling packages, not a platform

Status: Accepted (per PRD v0.4, sections 3, 6, 17)

## Context

Platform ambitions (hosting, teams, CI gating, notifications) would dilute v1 and add deployment dependencies for an internal, local-first developer tool whose users are engineers with git.

## Decision

Deliverables are exactly four artifacts: engine+CLI binary (Go), Python SDK package, agent binary (Go), and a results server embedded in the main binary. Flows live in git; runs are on-demand; results live on disk; the results server binds to localhost. CI integration, scheduling, notifications, teams/permissions, retention policies, and all business questions are explicitly v2.

## Consequences

- Install, point at a target, run, inspect — nothing else to deploy.
- The CLI must not preclude future CI use: clean exit codes and machine-readable output are in scope even though CI recipes are not.
- Scope creep toward platform features is a named risk; non-goals are enforced at review.
