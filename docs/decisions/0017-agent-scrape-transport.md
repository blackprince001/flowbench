---
type: Decision
title: "ADR 0017: The target-metrics agent is scraped over HTTP, Linux-first"
description: flowbench-agent serves its host resource sample over a plain GET /metrics; the CLI polls it on a ticker into the run store's own agent.json tier. Resolves PRD §17's open transport question. v1 ships Linux only; other OSes return an honest unsupported error.
status: Accepted
timestamp: 2026-07-27
---
# ADR 0017: The target-metrics agent is scraped over HTTP, Linux-first

Status: Accepted (issue #32; PRD §10.6, §17)

## Context

`internal/executor`'s self-metrics sampler already streams the *generator's*
own resource use into every run (`selfmetrics.go`), but a stress knee or
soak drift can't be attributed to the target versus the generator without
the target's own numbers too. Issue #32 adds that missing half: a
standalone agent binary on the target host, streaming host resource
metrics, correlated by run ID into the run store. The PRD explicitly leaves
the transport open — "push to collector over gRPC? scrape?" (§17) — and
names this issue as the place to settle it.

The repo has exactly one third-party dependency in the whole engine (a YAML
parser, ADR 0010, described there as "the engine's first"), zero
gRPC/protobuf tooling anywhere, and already solved the one closely
analogous problem — one-directional streaming for the live-run view — with
plain stdlib SSE rather than a persistent bidirectional protocol (ADR 0015,
which names #32's pending decision directly). That precedent, plus the
project's minimal-dependency ethos (ADR 0004, ADR 0014), narrowed the real
choice to a push model (the agent dials home, akin to a metrics exporter's
remote-write) or a scrape model (the collector dials the agent, akin to
Prometheus).

Host-level sampling itself is also OS-specific: CPU/mem/network/descriptors/
load-average are all readable from `/proc` on Linux with zero dependencies,
but macOS has no `/proc` and needs real `sysctl` syscalls that the existing
precedent (`internal/executor/cpu_unix.go`) doesn't reach for — it only
reads the *generator process's* own CPU via `syscall.Getrusage`, not a
whole host's.

## Decision

**Scrape, not push.** `flowbench-agent` is a standalone binary
(`cmd/flowbench-agent`) that binds an address and serves one route,
`GET /metrics`, returning the current host sample as JSON
(`internal/agent.Handler`). A target that names an `agent_addr` gets that
address polled by `internal/agent.Poll` on a ticker (default 1s, matching
the generator's own self-metrics cadence) from `cmd/flowbench`'s `run`/
`--watch` commands, structurally the same shape as `selfmetrics.go`'s
sampler with an HTTP GET in place of a local `runtime` read. Each poll runs
against its own short timeout and is silently skipped on failure — never
retried, never blocking, never erroring the run. The accumulated series is
written to the run store's `agent.json` tier alongside `metrics.json`, and
`Meta.AgentAttached` is derived from whether that series is non-empty,
never a caller-supplied flag, so the two can never disagree.

Scrape was chosen over push for the same reasons ADR 0015 chose SSE over a
websocket: the agent's data is one small frame the collector already knows
how often it wants, there's no reconnect/backpressure protocol to design,
and each poll is independent — a stalled or unreachable agent just means
that tick's sample is missing, not a broken connection to recover. It also
means the agent binary itself stays server-only and stateless: it has no
opinion about who's collecting or how often.

**Linux-first; other OSes stubbed.** `internal/agent`'s `Read()` is built
with Go build tags: `read_linux.go` parses `/proc/stat`, `/proc/meminfo`,
`/proc/net/dev`, `/proc/loadavg`, and `/proc/sys/fs/file-nr`, all pure
stdlib. `read_other.go` covers every non-Linux `GOOS` and returns a clear
"unsupported on this OS" error — the same shape `cpu_other.go` already uses
for the generator's own self-metrics on non-unix. A target attached to a
non-Linux agent runs with no target-resource series, not a broken run:
fail-open applies to OS support exactly as it applies to network failures.

## Consequences

- The agent is one more standalone binary alongside the CLI, matching the
  PRD's four-deliverable framing (engine+CLI, Python SDK, agent binary,
  results server) — no new dependency, no new go.mod.
- A dead or unreachable agent, or an agent on an unsupported OS, never fails
  or blocks the run it's attached to; `agent.json` and `meta.json`'s
  `agent_attached` simply reflect however many samples actually landed,
  down to zero.
- Rejected: gRPC/protobuf push — would be the codebase's first RPC
  dependency for a problem SSE-style scraping already solves more simply,
  and a push model needs the agent to know the collector's address rather
  than the reverse, inverting the target/attacker-surface story the
  allow-list (`internal/target`) already tells.
- macOS/Windows `sysctl`-based sampling is a real follow-up, not ruled out —
  `read_other.go`'s stub is the seam a future `read_darwin.go` fills in
  exactly the way `read_linux.go` does today.
- Dashboard rendering of the target series (an overlay chart, throttle-rate
  correlation) is explicitly out of scope here; #32 is the data layer only,
  and the existing placeholder in `internal/server` is left for whichever
  issue builds that chart.
