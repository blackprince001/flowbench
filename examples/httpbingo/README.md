# httpbingo.org — every way a step can fail

[httpbingo.org](https://httpbingo.org) is an HTTP request-and-response service
that exists to be tested against: it returns any status you ask for, waits as
long as you ask, and echoes back whatever you send. That makes it the one public
API it is reasonable to point failure scenarios at — every failure here is one
you asked for, and nobody's production service absorbs it.

Nothing needs installing. All three scenarios run against the live service.

```sh
flowbench run examples/httpbingo/echo.flow.yaml       --target examples/httpbingo/target.yaml
flowbench run examples/httpbingo/faults.flow.yaml     --target examples/httpbingo/target.yaml
flowbench run examples/httpbingo/resilience.flow.yaml --target examples/httpbingo/target.yaml
flowbench serve --store runs
```

## `echo.flow.yaml` — chaining and secrets

Take a value the service generated, send it back, prove it round-tripped. It
mints a uuid, injects it into both a header and a JSON body of the next call,
and asserts on the echo — plus a `wait` step and assertions across all five
sources (`status`, `latency`, `header.*`, `$.json.path`, and a bare variable).

The `Authorization` header is sourced from the environment, never the file
(ADR 0005). Any value resolved through `{{ env.* }}` is registered as a secret
and scrubbed before anything is stored — so the flow deliberately sends a real
credential to an endpoint that hands it straight back, and it still reaches
nothing on disk:

```sh
DEMO_TOKEN=hunter2-do-not-leak flowbench run examples/httpbingo/echo.flow.yaml \
  --target examples/httpbingo/target.yaml
grep -rc hunter2-do-not-leak runs/   # 0 — nowhere in any artifact
grep -rl '\[redacted\]' runs/        # the captured echo, with the value scrubbed out
```

That is why this one runs a small load profile instead of a single iteration:
there have to be stored traces to grep. Integration mode prints and exits
without writing a run at all — see `deck-of-cards` for the exit-code contract.

## `faults.flow.yaml` — the failure taxonomy

One run in which every step fails a different way, which is what the **Failures**
tab exists to separate:

| step              | cause        | because                                          |
| ----------------- | ------------ | ------------------------------------------------ |
| `gateway`         | `status`     | the target answered 500                          |
| `declined`        | `status`     | the target answered 402 — a different group      |
| `rate_limited`    | `throttled`  | 429, never counted with the errors (ADR 0006)    |
| `shedding`        | `throttled`  | 503, mapped to a throttle by this step           |
| `wrong_shape`     | `assertion`  | it answered fine, the answer was wrong           |
| `missing_receipt` | `extraction` | it answered fine, the value was not there        |
| `never_answers`   | `timeout`    | it accepted the call and then said nothing       |
| `unreachable`     | `connection` | there is no host to accept it at all             |

Open `Failures` and the eight groups are there with a count each, expandable to
any single iteration's waterfall. The two throttle groups sit apart from the six
error groups no matter how the run is read, which is the rule the whole view is
built on.

Two details worth knowing:

- **`never_answers` needs the target's `request_timeout`.** `/delay/5` answers
  after five seconds; the target grants two, so the engine records a timeout
  rather than a slow success. Without a bound, a hung call holds its VU for the
  adapter's 30s default.
- **`unreachable` calls `https://nothing.invalid`,** which is on the target's
  allow-list on purpose. `.invalid` is reserved by RFC 2606 and never resolves,
  so the call fails at DNS with no server anywhere to bother.

## `resilience.flow.yaml` — retries, and the case for them

`/status/200:3,503:1` is a weighted roll: three 200s to every 503. The scenario
calls it twice per iteration — once with a retry policy, once without — so the
run is its own control.

The result is the argument. The retried step recovers every time and contributes
**no failure group at all**; the unguarded one fails about a quarter of the time
and gets a `status · HTTP 503` group of its own. Open a kept trace in the
**Waterfall** and the retried step is not one bar but several — `attempt 1`,
`backoff`, `attempt 2` — nested under it, with the step's duration covering all
of them. Retries add to measured latency rather than hiding inside it: a call
that succeeds on its third try took as long as three tries.

Note the 503 here is *not* declared a throttle, unlike `faults.flow.yaml`. The
same status is load-shedding in one service and a plain fault in another, and
only the author knows which — so the engine never guesses.

## Scale

These profiles are small on purpose: a few VUs for a few seconds, a couple of
hundred requests in total. httpbingo is free and someone else pays for it.

Anything at real scale belongs against the local stub in
[`examples/load-local`](../load-local), which exists so that a public service
never has to absorb a stress run.
