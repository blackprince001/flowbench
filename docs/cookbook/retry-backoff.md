# Retry and backoff

Two different problems get solved by the same `retry:` block: a rate limiter turning you away (retry after the window refills, ideally when the server says to) and a flaky upstream throwing intermittent 5xx (retry with growing gaps and hope the fault is transient). This recipe shows both, and how to read the result — every attempt and every backoff wait is its own span, so retries are visible in the trace rather than smoothed into a latency number.

## Recovering throttles

When the server sends `Retry-After`, honor it — from [`tests/flows/authenticated_checkout.flow.yaml`](../../tests/flows/authenticated_checkout.flow.yaml):

```yaml
    retry:
      on_status: [429, 503]
      backoff: honor_retry_after
      max_attempts: 5
```

Against a known limiter with a short refill window, a fixed delay works too. [`examples/load-local/retry.flow.yaml`](../../examples/load-local/retry.flow.yaml) pushes 250 req/s past the local stub's 200/s limit and retries the `429`s:

```yaml
flow: checkout_with_retry
steps:
  - id: checkout
    call: POST /checkout
    retry:
      on_status: [429]
      backoff: fixed
      base_delay: 50ms
      max_attempts: 4
    assert:
      - status == 200
profile:
  mode: load
  vus: 20
  arrival_cap: 250/s # above the 200/s limit, so some calls are throttled first
  hold: 5s
```

Run it (start the stub with `go run ./examples/load-local/stub` first) and compare against the unretried stress run in the same folder: `throttle_rate` drops from ~40% to ~3.28%, because most throttled calls recover on a later attempt once the limiter refills — and p95 rises, because the backoff waits are honestly counted inside the step's duration.

## Flaky 5xx, with a built-in control

[`examples/httpbingo/resilience.flow.yaml`](../../examples/httpbingo/resilience.flow.yaml) runs against `/status/200:3,503:1` — a weighted roll, three 200s to every 503 — and calls it twice per iteration, once guarded and once not, so the run is its own control:

```yaml
flow: flaky_upstream
steps:
  - id: flaky_read
    call: GET /status/200:3,503:1
    retry:
      on_status: [503]
      backoff: exponential
      base_delay: 100ms
      max_attempts: 4
    assert:
      - status == 200

  # A second call with no retry policy at all, as the control: same odds, but
  # every 503 it sees is a recorded failure.
  - id: unguarded_read
    call: GET /status/200:3,503:1
    assert:
      - status == 200
```

The result is the argument. The retried step recovers every time and contributes no failure group at all; the unguarded one fails about a quarter of the time and gets a `status · HTTP 503` group of its own in the **Failures** tab. Note the 503 here is *not* declared a throttle, unlike `faults.flow.yaml` in the same folder: the same status is load-shedding in one service and a plain fault in another, and only the author knows which, so the engine never guesses ([ADR 0006](../decisions/0006-rate-limiting-first-class-signal.md)).

## Bounding attempts

`max_attempts` is the ceiling, and hitting it is not hidden. A call that stayed throttled through every attempt classifies as `throttled` at the end (or `failed`, for a plain error status) — the retries bought nothing, and the outcome says so. From the run's own terminal walkthrough in [`examples/README.md`](../../examples/README.md), a call that stopped at `max_attempts: 4`:

```text
step 'checkout'  (151ms total, incl. backoff)
   attempt 1      0.1ms      # 429
   backoff       50.3ms      # fixed 50ms wait
   attempt 2      0.2ms      # 429
   backoff       50.1ms
   attempt 3      0.1ms
   backoff       50.4ms
   attempt 4      0.2ms      # still 429 → classified throttled
```

## Reading it in the waterfall

Open the run (`flowbench serve --store runs`) and pick a kept trace in the **Waterfall**. A retried step is not one bar but several — `attempt 1`, `backoff`, `attempt 2` — nested under it, with the step's own duration covering all of them. Retries add to measured latency rather than hiding inside it, which is the point: a call that succeeds on its third try took as long as three tries. To see the spread across a whole run from the shell:

```bash
grep -o '"attempt [0-9]*"' runs/*/traces.json | sort | uniq -c
```

## Why it works

Attempts and backoff waits are spans like everything else ([Spans and outcomes](../reference/spans-and-outcomes.md)), so the flame graph folds them across iterations — a step whose time is mostly backoff is visibly mostly backoff — and the waterfall shows any single iteration's exact sequence. That is what keeps retry policies honest: you can see what each one cost, not only what it saved. For the full retry surface, see [Retries and throttling](../guide/retries-and-throttling.md).
