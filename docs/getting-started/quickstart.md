# Quickstart

One YAML flow to a first stress run and flame graph, in under ten minutes. You need the `flowbench` binary ([installation](installation.md)) and a checkout of the repo for the bundled stub — or substitute any service of your own you are allowed to load.

## 1. Start a target worth stressing

The repo bundles a small stub that admits about 200 requests per second and answers `429 Too Many Requests` beyond that — a rate limiter, the thing stress runs exist to find. In one terminal:

```bash
go run ./examples/load-local/stub
```

## 2. Write the flow

A flow file is one YAML document: a name, ordered steps, and a profile. Save this as `stress.flow.yaml` (it is `examples/load-local/stress.flow.yaml`, trimmed):

```yaml
flow: checkout_pressure
steps:
  - id: checkout
    call: POST /checkout
    assert:
      - status == 200
profile:
  mode: stress
  vus: 40
  arrival_cap: 400/s   # hold a known rate above the 200/s limit
  hold: 5s
  thresholds:
    - p95(latency) < 300ms
    - error_rate < 2%  # throttled responses do NOT count against this
```

Two things to notice. The `arrival_cap` makes the load open-loop: FlowBench dispatches at 400 requests per second regardless of how fast the target answers, which is what makes a rate limit visible. And the `error_rate` threshold ignores throttles — in stress mode a `429` is classified `throttled`, its own outcome class, never an error.

## 3. Write the target config

Runs are gated by a target config: the host allow-list and the safety ceilings. Save as `target.yaml`:

```yaml
name: local-stub
base_urls:
  - http://localhost:8080
max_vus: 500
max_rps: 1000
```

A flow can only reach hosts under `base_urls`, and a run that would exceed the ceilings is refused before any load is generated.

## 4. Run it

```bash
flowbench run stress.flow.yaml --target target.yaml
```

You get a summary like:

```text
running "checkout_pressure" against local-stub (http://localhost:8080) [stress, 40 VUs]
  2000 iteration(s), 2000 flow-run(s) in 5.01s
  error_rate=0.10%  throttle_rate=40.55%  p50=2.1ms p95=4.8ms p99=11.2ms
  p95(latency) < 300ms: ok  (p95(latency) = 4.8ms, want < 300ms)
  error_rate < 2%: ok  (error_rate = 0.10%, want < 2.00%)
run saved to runs/20260730T...Z
```

The headline is the separation: `throttle_rate` around 40% (the limiter doing its job) against an `error_rate` near zero (the target itself is healthy). A tool that folded those together would report a 40% "failure rate" and send you hunting for a bug that does not exist. The exit code is `0` — the thresholds held; a breach exits `1`.

## 5. Open the flame graph

The run was saved to the `runs/` store. Serve it:

```bash
flowbench serve
```

Open `http://127.0.0.1:7580`, click the run, then the **Flame graph** tab. Depth 0 is the flow; above it the `checkout` step; above that the HTTP call and its phases — `connect`, `tls`, `ttfb`, `transfer`. Width is aggregate time: the widest frame is where the run's time actually went. The **Waterfall** tab shows the same spans for one iteration at a time; **Outcomes** draws every flow-run as a cell, throttles in their own color.

## 6. Where next

- Chain real steps — login, extract a token, act, assert: [authoring flows](../getting-started/concepts.md) and the [login-chained cookbook recipe](../cookbook/login-chained-flows.md).
- Attach the [target-metrics agent](../guide/agent.md) and a breaching stress run will classify its knee: `throttled` (an enforced limit) vs `degraded` (genuine saturation).
- Author the same flow in Python instead: the [Python SDK](../guide/python-sdk.md).
- Watch a run live and abort it from the browser: `flowbench run --watch`, in [the results server guide](../guide/results-server.md).
