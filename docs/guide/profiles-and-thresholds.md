# Profiles and thresholds

The profile is the execution contract: the same flow, run five ways. Integration and system answer "does it work"; load, stress, and soak answer "does it hold" — and the split changes how failures are treated, how throttles are classified, and what the exit code means.

```yaml
profile:
  mode: load
  vus:
    ramp: 0 -> 200 over 2m
    hold: 10m
  arrival_cap: 300/s
  thresholds:
    - p95(latency) < 800ms
    - error_rate < 1%
    - throttle_rate < 5%
```

## The five modes

| Mode | VUs | Arrival | Stops when | Failures are |
| --- | --- | --- | --- | --- |
| `integration` | 1 (forced) | closed | one pass (or one per fixture row) | loud, per iteration |
| `system` | as declared | closed | one pass | loud, per iteration |
| `load` | ramp → hold | open under a cap | the schedule ends | data, judged by thresholds |
| `stress` | ramp → hold | open under a cap | the schedule ends | data, plus the knee classification |
| `soak` | ramp → hold | open under a cap | the schedule ends | data, plus trend evaluation |

`vus` is either an integer or a ramp: `ramp: "0 -> 500 over 5m"` with an optional `hold`. `iterations` bounds a closed-mode run by count instead. In integration mode, ramp, hold, and arrival cap are ignored — one VU, once, is the point.

## The arrival cap

`arrival_cap: 400/s` (also `/m`, `/h`, or `N/<duration>` like `50/2s`) is a **hard scheduling constraint**, not a soft target (ADR 0013): the generator dispatches at that rate whether or not the target keeps up. This is what makes a rate limit visible and what keeps latency honest — a closed loop slows down with a struggling target and quietly under-reports its problem. Latency is measured coordinated-omission-aware, from intended dispatch to completion, so queueing shows up in the percentiles instead of hiding between requests.

## Thresholds

Thresholds are the run's declared gates, evaluated over the whole run:

```text
p95(latency) < 800ms      # any pNN, or avg / max / min
error_rate < 1%           # rates take a percent or a 0..1 fraction
throttle_rate <= 5%
```

Operators are `<` `<=` `>` `>=`. Latency limits are durations; rate limits are percentages or fractions. A breached threshold prints `BREACH`, is persisted with the run, and exits the process nonzero — which is the entire [CI-gating story](../cookbook/ci-gating.md): the run itself carries its verdict, so a reader of the store never has to re-guess whether 0.2% errors under a 1% gate was a pass.

`throttle_rate` is its own metric because `throttled` is its own outcome class: a stress run at 40% throttle and 0.1% errors is a healthy target behind a working limiter, not a broken one.

## Soak: trend evaluation

Point-in-time gates miss the thing a soak exists to find — slow creep. Soak runs additionally split at their midpoint and compare halves: p95 latency growth beyond 10%, or error/throttle-rate drift beyond 5 points, is flagged as a trend finding (`p95(latency) trend`, …) and shown in the run page's **Soak trend** card. A clean soak is flat; a flat soak flags nothing.

## Stress: the knee, classified

A stress run exists to break, and a break has two very different causes. When thresholds breach, the collector locates the knee — the first point in the run's own time series where a breached gate first trips, tagged with the active VU count — and classifies it against the [agent's](agent.md) resource series:

- **degraded** — latency or errors broke their gates while the target's CPU or memory climbed across the knee. Genuine saturation: the next conversation is about capacity.
- **throttled** — the throttle rate rose while target resources stayed flat. An enforced limit: the next conversation is about quotas, backoff, or the arrival cap.
- **inconclusive** — no agent was attached, or the signals contradict (throttling *and* saturation at once; latency rising over a flat target, which points away from this host entirely). The finding says what is missing rather than guessing.

The finding is computed once, saved as `knee` in the run's meta, printed by the CLI as `knee_point_found: <class> — <detail>`, and rendered as the **Knee point** card on the run page. A stress run that held its gates says so on the card; it does not silently show nothing.

```text
  throttle_rate < 5%: BREACH  (throttle_rate = 41.20%, want < 5.00%)
  knee_point_found: throttled — throttle rate rose 0.3% → 41.2% across the knee
  while target CPU 40% → 42% stayed flat — the target enforced a rate limit
  rather than saturating
```

The worked version of this — rate-limited stub, saturating stub, and how to read each — is the [rate limits and capacity recipe](../cookbook/rate-limits-and-capacity.md).

## Exit codes

Every load-family run exits `0` when thresholds held, `1` on a breach, an abort, or an interrupt, and `2` on a pre-run error (parse failure, safety-gate refusal). Integration/system exit `1` on any recorded failure. The [CLI reference](../reference/cli.md) has the full contract.
