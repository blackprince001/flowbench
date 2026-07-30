# Retries and throttling

Both are about a target saying "not now": a retry policy recovers from it, and throttle classification reports it honestly. FlowBench treats a rate-limit response as its own outcome class — `throttled`, never folded into `failed` — because counting `429`s as generic errors makes a stress run against a rate-limited endpoint fail by design, and confuses a limiter doing its job with the target falling over (ADR 0006).

## Retry policy

A step's `retry:` block names the statuses worth retrying, how to wait between tries, and where to stop:

```yaml
- id: flaky_read
  call: GET /status/200:3,503:1
  retry:
    on_status: [503]
    backoff: exponential
    base_delay: 100ms
    max_attempts: 4
  assert:
    - status == 200
```

(from `examples/httpbingo/resilience.flow.yaml`)

`on_status` lists the statuses that trigger another try; anything else — including a try that produced no status at all, like a refused connection — stops the loop. `max_attempts` bounds it, and must be at least 1 so runs stay bounded. The three `backoff` strategies:

- `fixed` — wait `base_delay` between every attempt.
- `exponential` — double the wait each attempt, starting from `base_delay`.
- `honor_retry_after` — wait what the target's `Retry-After` header asks for (delta-seconds, an HTTP date, or the millisecond form gRPC's pushback header uses), falling back to `base_delay` when the response carries none.

`base_delay` is optional and defaults to 100ms, so a retry loop never becomes a hot loop hammering the target; a single computed wait is capped at two minutes, so an exponential policy with a large attempt count cannot wait absurdly long.

## Every attempt is a span

With a policy set, each try and each wait is its own child span under the step, and the step's duration is the time-to-success *including* backoff — so retries add to measured latency rather than hiding a capacity problem inside it. A call that succeeds on its third try took as long as three tries:

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

(a trace from `examples/load-local/retry.flow.yaml`)

Credentials are attached per attempt, not once per step, so a retried request carries a fresh HMAC signature and timestamp — and a token that refreshed mid-backoff — instead of replaying the first try's (see [authentication](auth.md)).

## Where retry applies

Retry policies apply to `call`, `graphql`, and `grpc` steps — the call-shaped ones, where the loop can re-send a request and read a status. A `ws` step is deliberately excluded: neither re-sending a request nor reading a status has a meaning on a session that is already open. `poll` needs no policy because it bounds itself with `interval`, `timeout`, and `max_attempts` of its own.

## Throttling is a first-class signal

`throttled` is its own outcome class with its own `throttle_rate` metric, reported alongside — never inside — `error_rate`. HTTP `429` always classifies as a throttle, in every mode. Any other status is ambiguous: a `503` is load-shedding in one service and a plain fault in another, and only the author knows which, so the engine never guesses. The `throttle:` block is where the author says so:

```yaml
- id: shedding
  call: GET /status/503
  throttle:
    statuses: [503]
```

(from `examples/httpbingo/faults.flow.yaml`)

What a throttle *means* for the run is mode-aware (see [profiles](profiles-and-thresholds.md)). In `integration` and `system` modes a throttle is a failure by default — a functional test that got rate-limited did not pass. In `load`, `stress`, and `soak` it is data: excluded from `error_rate`, reported as `throttle_rate`, and the run exits clean unless a threshold says otherwise. Either default is overridden per step with `as_error: true` or `as_error: false`, and `throttle_rate` is tracked in every mode regardless — a throttled iteration that also fails still counts toward both.

That separation is what keeps thresholds meaningful against rate-limited targets: a stress run can hold `throttle_rate` at 40% while `error_rate` stays at 0.1% and the `error_rate < 2%` gate passes, because the limiter doing its job is the finding, not a failure.

## One signal, three wires

The same capacity signal arrives differently per protocol, and classifies the same everywhere:

| Wire | Signal |
| --- | --- |
| HTTP (`call`, `graphql`, a `ws` handshake) | status `429`, `Retry-After` and all |
| WebSocket, once the socket is up | close code `1013` — "try again later", RFC 6455's own `429` |
| gRPC | status `RESOURCE_EXHAUSTED` |

A server shedding load reads the same whether it refused the request, closed the session, or shed the call — which is what makes `throttle_rate` comparable across a flow that mixes [protocols](protocols.md).

The two mechanisms compose: retrying the throttled statuses recovers most of them once the limiter refills — `examples/load-local/retry.flow.yaml` pushes 250 req/s past a 200/s limit and drops `throttle_rate` from 40% to about 3% — and a call that is still throttled when `max_attempts` runs out classifies as `throttled`, visible in the trace as the attempt spans above.
