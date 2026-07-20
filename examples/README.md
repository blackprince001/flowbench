# Examples

Worked examples for `flowbench run`. A run is always a **flow** (what to test)
paired with a **target** (where to run it, and the safety limits):

```
flowbench run <flow.yaml> --target <target>
```

The smoke check in this folder hits example.com (IANA's designated example
host):

```
flowbench run examples/example_com.flow.yaml --target examples/example.target.yaml
```

```
running "example_smoke" against example (https://example.com)
  example_smoke [1/1]  ok
1 iteration(s): 1 passed, 0 failed  (79ms)
```

Not built yet? Use `go run ./cmd/flowbench run …` with the same arguments.

## Two files, two jobs

The flow says *what* to do; the target says *where* to do it. Notice the flow
calls `GET /` — a relative path, with no host. At run time the target's base
URL fills that in: `/` + `https://example.com` → `GET https://example.com/`.

| | Flow (`*.flow.yaml`) | Target (`--target`) |
|---|---|---|
| Answers | *What* to test — steps, chaining, extractions, assertions | *Where* to run it, and the limits |
| Holds | relative URLs (`/orders`), `{{ variables }}` | base URLs, VU/RPS ceilings, disallowed modes |
| Credentials | none — read from `{{ env.* }}` at run time | **never** — safe to commit |
| Changes when | the test logic changes | you switch environment |

Splitting them means **one flow runs against many environments** without edits —
you just change `--target`:

```
flowbench run checkout.flow.yaml --target local      # localhost dev loop
flowbench run checkout.flow.yaml --target staging    # staging host, VUs capped
flowbench run checkout.flow.yaml --target prod        # prod host, stress disallowed
```

The target's `base_urls` double as a **host allow-list**: a call to any host
not listed is refused before a single request is sent. That is why the third
demo below exits non-zero without touching the network.

## Files here

- [`example_com.flow.yaml`](example_com.flow.yaml) — one `GET /` asserting the
  status, latency, and content type.
- [`example.target.yaml`](example.target.yaml) — points at `https://example.com`
  and allows only that host.

For a multi-step flow — login, extract a token, carry it forward, assert —
see [`tests/flows/authenticated_checkout.flow.yaml`](../tests/flows/authenticated_checkout.flow.yaml).

## Exit codes

`flowbench run` returns a code you can gate on:

| Code | Meaning |
|---|---|
| `0` | every iteration passed |
| `1` | ran, but assertions failed |
| `2` | pre-run error — bad arguments, a parse/validation failure, or the host allow-list gate |

Try it: change an assertion to `status == 301` and the run exits `1`; point a
step at another host and it exits `2`.

## Good to know

- **`--target local` needs no file.** It defaults to `http://localhost:8080`,
  so `flowbench run flow.yaml` just works against a local dev server.
- **Secrets stay out of these files.** Anything sensitive comes from the
  environment as `{{ env.API_TOKEN }}` and is scrubbed from recorded output
  ([ADR 0005](../docs/decisions/0005-no-bespoke-secrets.md)).
