---
type: Decision
title: "ADR 0015: The live view and server-side abort run in one process, over SSE"
description: The live view of an in-progress run and its browser-triggered abort are served by the run command itself (flowbench run --watch), not by serve; updates stream over server-sent events and abort cancels the run's context — the results server's one write path.
status: Accepted
timestamp: 2026-07-24
---
# ADR 0015: The live view and server-side abort run in one process, over SSE

Status: Accepted (issue #41; PRD sections 10.7 lines 289/307/315/601)

## Context

Two of the results server's abilities need something the rest does not: watching a run *while it happens*, and stopping it from the browser. Every other view reads a finished run off the store (ADR 0004, ADR 0007), and `serve` is deliberately read-only — "no write path beyond triggering abort" (PRD 10.7). But `run` and `serve` are separate processes that share only the on-disk store; a `serve` process holds no reference to a running executor, and the executor exposes no live state beyond an active-VU counter.

So a live view and an abort cannot be bolted onto the read-only `serve`. They need a process that both executes the run and serves it, and a channel from an HTTP handler back into the running executor.

## Decision

The live view is a mode of the run command: **`flowbench run <scenario> --watch [--addr]`** runs the scenario and, in the same process, serves a small live page bound to loopback. It is not part of `serve`; the `internal/server` package stays read-only, and the live server is its own small handler in `cmd/flowbench`.

- **Abort is the one write path.** The run is driven by a cancellable context (as Ctrl-C already was); the live server holds that `cancel` and a `POST /abort` calls it. The executor's existing cancellation unwinds every VU and returns a partial `*Result`, which is saved exactly as an interrupted run is. Ctrl-C and a browser abort are the same mechanism.
- **Live metrics come from new hot-path atomics.** The executor gained `completed`/`failed`/`throttled` counters and an `Options.Progress` callback, invoked on the self-metrics sampler's existing cadence with a thread-safe snapshot. Percentiles are not computed live — they need a concurrent histogram — so the live view shows VUs, throughput and the outcome rates, and the full percentiles appear on the completed run's dashboard.
- **Updates stream over server-sent events.** `GET /live/stream` is a `text/event-stream` that pushes one snapshot per tick and ends with a `done` event carrying the stored run's URL. A `GET` stream keeps the read-only-except-abort invariant. The page is server-rendered first (so it reads before any script), a dependency-free `EventSource` client updates the figures in place (ADR 0014 — one file, no build), and with JavaScript off a `<noscript>` meta-refresh keeps the snapshot current and the abort button falls back to a plain form POST.
- **Watching flows into inspecting.** When the run ends, its artifacts are flushed and the same process mounts the read-only `server.Server` over the store, so the live page redirects into the completed run's dashboard, flame graph and traces. The command keeps serving until a second Ctrl-C.

SSE was chosen over long-polling or a websocket: the data is one-directional server→client, the volume is one small frame per second, and an `EventSource` reconnects on its own with no client code — websockets would add a bidirectional channel the feature does not use, and polling would reload rather than stream. (This mirrors #32's own decision to record its transport choice as an ADR.)

## Consequences

- **The live view is a `run` flag, not a `serve` feature.** You watch the run that this process is executing; `serve` never gains a live mode or a write path. A future "attach to a run started elsewhere" would need real IPC and is out of scope.
- **Live abort saves an interrupted, partial run** — the same shape Ctrl-C produces, not the kill-switch `Aborted` flag (which stays reserved for an `abort_run` step). The partial artifacts are flushed and browsable.
- **Live percentiles are deferred.** Adding a concurrent latency histogram to the hot path would let the live view show p50/p95/p99; until then they are a dashboard-only figure, stated as such in the UI.
- **The target-resource overlay is still absent.** The live view lists an agent series in the PRD, but that waits on #32; the live figures are generator-side only for now.
