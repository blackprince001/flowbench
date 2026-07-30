# Rate limits and capacity

A stress run that ends in failures has told you almost nothing until you know *which kind*: did the target degrade, or did its rate limiter engage? FlowBench keeps the two apart at every layer — `throttled` is its own outcome class with its own `throttle_rate`, separate from `error_rate` — so pushing past a limit produces a reading, not a wall of red. This recipe shows the open-loop pattern for doing that on purpose, the same signal across three protocols, and the knee classification that names what broke.

## Hold a known rate above the limit

[`examples/load-local/stress.flow.yaml`](../../examples/load-local/stress.flow.yaml) holds a steady 400 req/s (`arrival_cap`) against a local target that admits ~200/s:

```yaml
flow: checkout_pressure
steps:
  - id: checkout
    call: POST /checkout
    assert:
      # In stress mode a 429 is classified `throttled` and never reaches this
      # assertion; only a real non-200 (the ~0.5% of 500s) fails it and counts
      # against error_rate.
      - status == 200
profile:
  mode: stress
  vus: 40
  arrival_cap: 400/s # hold a known rate above the 200/s limit, rather than flooding
  hold: 5s
  thresholds:
    - p95(latency) < 300ms
    - error_rate < 2% # throttled responses do NOT count against this
```

Start the stub (`go run ./examples/load-local/stub`), run it, and read the summary:

```text
running "checkout_pressure" against local-stub (http://localhost:8080) [stress, 40 VUs]
  2000 iteration(s), 2000 flow-run(s) in 5.011s
  error_rate=0.10%  throttle_rate=40.10%  p50=15.462ms p95=15.676ms p99=16.496ms
  p95(latency) < 300ms: ok  (p95(latency) = 15.676ms, want < 300ms)
  error_rate < 2%: ok  (error_rate = 0.10%, want < 2.00%)
```

`throttle_rate` is 40%, `error_rate` is 0.1%, and the run exits `0`. That distinction is the whole point: the limiter turning away half your traffic is the limiter *working*, and if `429`s counted as errors, every threshold on a rate-limited target would be meaningless — you could never stress it without failing, and a real 0.5% fault rate would drown in throttle noise. The `arrival_cap` is what makes the run open-loop: the planner holds the offered rate at 400/s regardless of how the target responds, so you are measuring behavior *at a known rate* rather than discovering an unknown one by flooding ([ADR 0013](../decisions/0013-arrival-cap-hard-scheduling-constraint.md)).

## The same signal in three vocabularies

Load-shedding looks different on the wire per protocol; it classifies the same:

| Protocol | The throttle | Example |
|---|---|---|
| HTTP | `429` (plus `Retry-After`) | [`load-local/stress.flow.yaml`](../../examples/load-local/stress.flow.yaml) |
| WebSocket | close code `1013` "try again later" | [`ws-local/capacity.flow.yaml`](../../examples/ws-local/capacity.flow.yaml) |
| gRPC | `RESOURCE_EXHAUSTED` (code 8) | [`grpc-local/capacity.flow.yaml`](../../examples/grpc-local/capacity.flow.yaml) |

The WebSocket version asks for 120 concurrent sessions from a feed that carries 40; the surplus is accepted, then closed with `1013`, and the run reads `throttle_rate=45.66%` with `error_rate=0.00%`. The gRPC version puts more charges in flight than the service processes at once and the shed calls arrive as `RESOURCE_EXHAUSTED` — nothing in gRPC's numbering is `429`, which is why the classification lives on the outcome, not the status code. Every other close code or status fails and names itself; only the target's own "you are going too fast" becomes `throttled`.

## The knee, classified

During a stress ramp, the **knee point** is the concurrency level at which thresholds begin to fail. Knowing *where* it is answers half the question; the other half is *why*, and that needs the [target-metrics agent](../guide/agent.md) attached. With one streaming CPU/memory/network series into the run, a breaching stress run is classified (issue #39):

- **degraded** — thresholds broke as the target's resources climbed: genuine saturation, a real capacity limit.
- **throttled** — throttle rate rose over flat resources: an enforced limit engaging, not the target struggling.
- **inconclusive** — no agent attached, or mixed signals.

The finding appears in three places that never disagree, because they are computed by the same collector routine: the CLI prints a `knee_point_found:` line with the class and detail, the run page shows it as the **Knee point** card, and the run's `meta.json` persists it under `knee` for anything scripting over the run store.

## Why it works

Classification happens at the point of assertion, per response, so the separation survives aggregation: `throttle_rate` and `error_rate` are independent metrics, thresholds bind to whichever you mean, and the [profile](../guide/profiles-and-thresholds.md) decides the defaults per mode. Without that, "find the capacity" and "confirm the rate limit" would be the same experiment with indistinguishable results; with it, the stress run above reads unambiguously — near-zero errors, heavy throttling, no resource climb — as *the limiter is doing its job at 200/s*, which is a fact about the target you can write down.
