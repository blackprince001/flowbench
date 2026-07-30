# Spans and outcomes

One span model is the single source of truth for everything the report draws (ADR 0007): the flame graph reads folded aggregates of spans, the waterfall reads sampled raw span trees, and both speak the same names. This page is the vocabulary you see in those views.

## Span names

| Span | Emitted by |
| --- | --- |
| `flow:<name>` | the root of every iteration |
| `<step-id>` | each step, named exactly as authored |
| `http_call`, `grpc_call`, `ws_open`, `ws_send`, `ws_receive` | the network leg of a step |
| `dns`, `connect`, `tls`, `ttfb`, `transfer` | phases inside an HTTP call |
| `attempt N`, `backoff` | retry policy execution — one span per attempt, waits visible |
| `graphql_errors` | inspection of a GraphQL response's errors array |
| `<variable>` | an extraction, named for the variable it binds |
| `assert_*` | assertions |
| `<name>` / `<name>@<variant>` | a prompt observation; `pace` appears beside it when a pace guard waited |

Folding is by name: the flame graph aggregates every span sharing a dot-path, which is why step ids are stable identifiers (renaming one splits its history) and why a prompt variant's label is part of its identity.

## Outcomes

Every flow-run and every span lands in exactly one outcome:

| Outcome | Meaning |
| --- | --- |
| `ok` | completed, assertions held |
| `failed` | an exchange failed or an assertion broke |
| `skipped` | not executed, because an earlier step failed |
| `throttled` | the target signalled rate limiting |

`throttled` is tracked apart from the other three (ADR 0006). In load/stress/soak a throttled flow-run's outcome *is* `throttled` and counts toward `throttle_rate`, not `error_rate`; in integration/system it also counts as failed — a smoke test that got throttled did not pass. Three protocol signals classify as throttled: HTTP `429` (plus any statuses the step's `throttle:` maps), WebSocket close code `1013`, and gRPC `RESOURCE_EXHAUSTED`.

## Latency

Latency is coordinated-omission-aware: measured from *intended* dispatch to completion, so time a flow-run spent waiting for a free slot counts against the target's percentiles rather than silently disappearing. The percentiles the CLI prints and the report plots are computed over that definition.

## Storage tiers

A run persists spans at two grains, plus derived series (see [the results server](../guide/results-server.md) for the on-disk layout):

- **folded** — span paths with aggregate counts and durations, bounded by path count; the flame-graph input.
- **traces** — complete raw span trees for a sample of iterations: every failure, plus sampled successes and throttles, with payloads captured under size caps and redaction.
- **series / samples** — bucketed outcomes-and-latencies over time, and a thinned per-flow-run tier, powering the charts, the outcome strip, and the knee's onset detection.

Capture policy in one line: all failures, sampled everything else, bodies capped, secrets redacted before anything touches disk.
