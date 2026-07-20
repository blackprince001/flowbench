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
