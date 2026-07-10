---
type: Decision
title: "ADR 0005: No bespoke secrets mechanism"
description: Credentials come from the process environment or existing vaults; the engine's only secrets responsibility is redaction.
status: Accepted
timestamp: 2026-07-10
---
# ADR 0005: No bespoke secrets mechanism

Status: Accepted (per PRD v0.4, sections 9, 10.3, 14)

## Context

On a scripting-first surface, secrets are just Python; inventing a parallel FlowBench secrets mechanism would duplicate what teams already use (env vars, vault CLIs) and create a new thing to leak.

## Decision

Secrets are not a FlowBench concept. YAML flows resolve `{{ env.VAR_NAME }}` against the process environment at run time; Python flows read credentials like any script. Target configs never carry credentials, so all flow/scenario/config files are safe to commit. The engine's actual responsibility is redaction: any value sourced from `{{ env.* }}` or flagged sensitive by the SDK is scrubbed from captured bodies, traces, logs, and served views before reaching the run store.

## Consequences

- No secrets file format, storage, or rotation to build or audit.
- Redaction is a hard P0 engine requirement and a non-functional guarantee, testable in isolation.
- DB verifier connections use the same `{{ env.* }}` mechanism and are read-only by default.
