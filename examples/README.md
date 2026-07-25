# Examples

Worked examples for `flowbench run`. Each folder is one self-contained example —
a **flow** (what to test) paired with a **target** (where to run it, and the
safety limits):

```
flowbench run <flow.yaml> --target <target>
```

Not built yet? Use `go run ./cmd/flowbench run …` with the same arguments.

## `example-com/` — the smallest thing that works

One `GET /` against example.com, asserting status, latency, and content type.

```
flowbench run examples/example-com/smoke.flow.yaml --target examples/example-com/target.yaml
```

```
running "example_smoke" against example (https://example.com)
  example_smoke [1/1]  ok
1 iteration(s): 1 passed, 0 failed  (79ms)
```

## `bored-api/` — chaining and a data-driven sweep

Against the [Bored API](https://bored-api.appbrewery.com):

- [`smoke.flow.yaml`](bored-api/smoke.flow.yaml) — three chained steps: grab a
  random activity, filter to an education activity, then extract that result's
  `key` and look it up directly (the login → take token → act pattern, minus
  the auth).

  ```
  flowbench run examples/bored-api/smoke.flow.yaml --target examples/bored-api/target.yaml
  ```

- [`sweep.flow.yaml`](bored-api/sweep.flow.yaml) — the same `/filter` call run
  once per row of [`filters.csv`](bored-api/filters.csv), each row injected as
  `{{ user.* }}`. This is the data-pool mechanic: a 1000-row file is 1000
  iterations.

  > The public API is rate-limited (100 requests / 15 min), so the full sweep
  > gets `429`-throttled partway. Point it at an unrated target for the whole run.

## `load-local/` — the load engine, end to end

The others run once at 1 VU. This folder exercises everything the load engine
does — the goroutine-per-VU pool, arrival caps, throttle-vs-error
classification, retry/backoff, threshold gating, soak trends, payload capture +
redaction, the run store, and safety rails — against one small **local** target,
so nothing real gets hammered.

Start the target in one terminal:

```
go run ./examples/load-local/stub
```

It has three endpoints: `/checkout` admits ~200 req/s and `429`s the rest (with
`Retry-After`, plus ~0.5% genuine `500`s), `/login` echoes its request body, and
`/slow` gets slower the longer it runs. Then run any flow below. Each drives the
VU pool, prints an aggregate summary, evaluates thresholds, and saves an
artifact to `runs/` (override with `--store`).

### stress — throttling is a signal, not a failure

[`stress.flow.yaml`](load-local/stress.flow.yaml) holds a steady 400 req/s
(`arrival_cap`) against the 200/s limit:

```
running "checkout_pressure" against local-stub (http://localhost:8080) [stress, 40 VUs]
  2000 iteration(s), 2000 flow-run(s) in 5.011s
  error_rate=0.10%  throttle_rate=40.10%  p50=15.462ms p95=15.676ms p99=16.496ms
  p95(latency) < 300ms: ok  (p95(latency) = 15.676ms, want < 300ms)
  error_rate < 2%: ok  (error_rate = 0.10%, want < 2.00%)
```

**`throttle_rate` (40%) is reported separately from `error_rate` (0.1%)** — the
milestone headline. A `429` classifies as `throttled`, not an error, so the rate
limiter doing its job doesn't fail the run. Exit `0`.

### load — capacity validation, with a ramp

[`load.flow.yaml`](load-local/load.flow.yaml) ramps `0 -> 30` VUs then holds, at
150 req/s (under the limit), to confirm the target holds up at expected load:

```
running "checkout_capacity" against local-stub (http://localhost:8080) [load, 30 VUs]
  1051 iteration(s), 1051 flow-run(s) in 7.017s
  error_rate=0.67%  throttle_rate=0.00%  p50=15.972ms p95=16.249ms p99=16.653ms
  p95(latency) < 100ms: ok   error_rate < 1%: ok
```

Under the limit → no throttling; the thresholds hold. Exit `0`.

### breach — thresholds gate the exit

[`breach.flow.yaml`](load-local/breach.flow.yaml) demands p95 under 5 ms from a
~15 ms target, so the CI gate fires:

```
  p95(latency) < 5ms: BREACH  (p95(latency) = 16.404ms, want < 5ms)
  error_rate < 1%: ok
```

Exit `1`, breach named. (The run is still saved.)

### retry — recover throttled calls, one span per attempt

[`retry.flow.yaml`](load-local/retry.flow.yaml) pushes 250 req/s past the limit
but retries `429`s with a short backoff, so most recover and `throttle_rate`
drops toward zero (`3.28%` here, vs `40%` unretried); p95 rises with the backoff
waits. Every attempt and wait is its own span in the trace — here a call that
kept getting throttled and stopped at `max_attempts: 4`:

```
step 'checkout'  (151ms total, incl. backoff)
   attempt 1      0.1ms      # 429
   backoff       50.3ms      # fixed 50ms wait
   attempt 2      0.2ms      # 429
   backoff       50.1ms
   attempt 3      0.1ms
   backoff       50.4ms
   attempt 4      0.2ms      # still 429 → classified throttled
```

`grep -o '"attempt [0-9]*"' runs/*/traces.json | sort | uniq -c` shows the spread.

### soak — trend detection, not point thresholds

[`soak.flow.yaml`](load-local/soak.flow.yaml) hits `/slow`, which degrades over
time. Soak posture splits the run at its midpoint and flags the creep (also
error-rate and throttle-rate drift) — a leak a point threshold would miss:

```
running "endurance" against local-stub (http://localhost:8080) [soak, 10 VUs]
  350 iteration(s), 350 flow-run(s) in 12.028s
  error_rate < 1%: ok
  p95(latency) trend: BREACH  (p95 latency crept 342.526ms → 392.277ms over the run (>10%))
```

Exit `1`. (A real soak runs for hours; this one is compressed to seconds.)

### login — secrets are captured, then scrubbed

[`login.flow.yaml`](load-local/login.flow.yaml) injects an env-sourced secret
into a request body; `/login` echoes it back, so it lands in the request *and*
the response. Captured traces keep those bodies for debugging — but the secret
is replaced with `[redacted]` before anything is stored:

```
DEMO_SECRET=hunter2-do-not-leak flowbench run examples/load-local/login.flow.yaml \
  --target examples/load-local/target.yaml

grep -rc 'hunter2-do-not-leak' runs/     # → 0, never stored
```

A captured payload in `traces.json`, request and echoed response both scrubbed:

```
"request":  {"password":"[redacted]","username":"ada"}
"response": {"password":"[redacted]","username":"ada"}
```

### safety rails — refuse dangerous runs before they start

[`strict.yaml`](load-local/strict.yaml) forbids high-load modes. The same stress
flow, pointed at it, never sends a request:

```
flowbench run examples/load-local/stress.flow.yaml --target examples/load-local/strict.yaml
flowbench: target "strict" disallows "stress" mode        # exit 2
```

The default [`target.yaml`](load-local/target.yaml) also sets `max_vus` / `max_rps`
ceilings; a profile that would peak higher is refused pre-run the same way.

### the run store

Every load/stress/soak run above wrote a directory under `runs/`:

```
cat runs/*/meta.json
```

```json
{ "scenario": "stress.flow.yaml", "mode": "stress", "initiator": "ada",
  "target": "local-stub", "commit": "56458d7…", "iterations": 2000,
  "error_rate": 0.001, "throttle_rate": 0.401, "p95": 15675959, … }
```

Alongside it: `folded.json` (the flame-graph tier — counts and duration sums per
span path), `traces.json` (sampled raw traces — all failures plus a sample of
successes, bodies redacted), and `metrics.json` (the generator's own
CPU/memory). It's a directory you own — no retention machinery.

## `auth-local/` — every auth scheme, against a service that checks

[`schemes.flow.yaml`](auth-local/schemes.flow.yaml) exercises all six schemes —
bearer, basic, API key (header and query), session cookie, OAuth2
client-credentials, HMAC signing — against a stub that `401`s anything it does
not recognise. That's the point: a scheme that quietly sends nothing **fails**
the run rather than passing it.

Start the stub in one terminal; it prints the credentials it expects:

```
go run ./examples/auth-local/stub
```

```
auth stub listening on :8090 — every endpoint demands a different scheme
export the credentials it expects:
  export DEMO_API_TOKEN=tok_demo_bearer_9f8e7d DEMO_USER=reports-service …
```

Paste that `export` block, then run:

```
flowbench run examples/auth-local/schemes.flow.yaml --target examples/auth-local/target.yaml
```

```
running "auth_schemes" against auth-stub (http://localhost:8090) [load, 5 VUs]
  20095 iteration(s), 20095 flow-run(s) in 3.001s
  error_rate=0.00%  throttle_rate=0.00%  p50=702µs p95=1.025ms p99=1.174ms
  error_rate < 1%: ok   p95(latency) < 100ms: ok
```

Nine steps × 20k iterations, every one authenticated. Three things that run
proves:

**One token, not twenty thousand.** The stub logs a line per client-credentials
grant. Across the whole run there is exactly one:

```
issued access token (grant 1, scope "payments:write")
```

The token endpoint is fetched once and the token shared by every VU, refreshed
30s before expiry. Without that, a 10k-VU run would open by rate-limiting
itself on its own auth server.

**Credentials declared once.** The `auth:` block at the top of the flow is the
flow-level default; every step inherits it, `reports` and the rest override it,
and `health` opts out with `auth: { scheme: none }`. The stub's `/health`
*refuses* a credential, so a default leaking onto an opted-out step fails the
run instead of passing unnoticed.

**Nothing reaches the run store.** `/whoami` echoes the credential into its own
response body, and captured payloads keep response bodies for debugging — so
there is something to scrub:

```
grep -rc 'tok_demo_bearer_9f8e7d\|s3cr3t-basic-pw\|at_demo_issued_by_the_stub' runs/
```

Zero, for every one of them — including the two the engine *derived* rather
than resolved: the base64 basic blob and the OAuth2 access token. The captured
body shows what happened instead:

```json
"response": "{\"seen\":\"Basic [redacted]\"}"
```

The HMAC signature deliberately isn't redacted: it is per-request and not
reversible to the secret, and registering one per request would grow the
redaction set without bound at 10k VUs.

Two more things the stub enforces, worth knowing because they shape the design:

- **The signature is stamped per attempt, not per step.** `/webhooks/replay`
  rejects a signature older than 30 seconds, which a request signed once and
  replayed through retry backoff would fall out of.
- **The OAuth2 token endpoint is inside the host allow-list.** It is a real
  outbound request carrying the client credentials, so it is gated like any
  call. Point `token_url` at a host missing from
  [`target.yaml`](auth-local/target.yaml) and the run refuses it — pre-run if
  the URL is a literal, at request time if it is templated.

## Two files, two jobs

The flow says *what* to do; the target says *where*. Notice the flows call
`GET /...` — relative paths, with no host. At run time the target's base URL
fills that in: `/random` + `https://bored-api.appbrewery.com` →
`GET https://bored-api.appbrewery.com/random`.

| | Flow (`*.flow.yaml`) | Target (`--target`) |
|---|---|---|
| Answers | *What* to test — steps, chaining, extractions, assertions | *Where* to run it, and the limits |
| Holds | relative URLs (`/filter`), `{{ variables }}` | base URLs, VU/RPS ceilings, disallowed modes |
| Credentials | none — read from `{{ env.* }}` at run time | **never** — safe to commit |
| Changes when | the test logic changes | you switch environment |

Splitting them means **one flow runs against many environments** without edits —
you just change `--target`. The target's `base_urls` double as a **host
allow-list**: a call to any host not listed is refused before a single request
is sent.

For a multi-step flow with auth — login, extract a token, carry it forward,
assert — see [`tests/flows/authenticated_checkout.flow.yaml`](../tests/flows/authenticated_checkout.flow.yaml).

## Exit codes

`flowbench run` returns a code you can gate on:

| Code | Meaning |
|---|---|
| `0` | every iteration passed |
| `1` | ran, but assertions failed |
| `2` | pre-run error — bad arguments, a parse/validation failure, or the host allow-list gate |

## Good to know

- **`--target local` needs no file.** It defaults to `http://localhost:8080`,
  so `flowbench run flow.yaml` just works against a local dev server.
- **Secrets stay out of these files.** Anything sensitive comes from the
  environment as `{{ env.API_TOKEN }}` and is scrubbed from recorded output
  ([ADR 0005](../docs/decisions/0005-no-bespoke-secrets.md)).
