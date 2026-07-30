# Authoring flows

A flow file is exactly one YAML document with at most five top-level keys: `flow` (the name, required), `steps`, `profile`, `auth`, and `data`. Anything else is a pre-run error naming the file, line, and column — the parser keeps position data precisely so mistakes are caught before any load is generated.

```yaml
flow: order_lookup
steps:
  - id: login
    call: POST /login
    body: { username: "{{ env.SHOP_USER }}", password: "{{ env.SHOP_PASS }}" }
    extract:
      token: $.token
    assert:
      - status == 200
      - token != null
  - id: fetch_order
    call: GET /orders/latest
    headers:
      Authorization: "Bearer {{ token }}"
    assert:
      - status == 200
      - $.order.state == "paid"
profile:
  mode: integration
```

## Steps

Each step needs an `id` — a name matching `[A-Za-z_][A-Za-z0-9_-]*`, unique within the flow (dots and `@` are reserved for span names) — and exactly one action: `call`, `graphql`, `ws`, `grpc`, `wait`, or `poll`. The `call` shorthand is one string, `METHOD /path`; the [protocols page](protocols.md) covers the other four surfaces.

`headers`, `query`, and `body` ride on request-shaped steps. `wait` takes a duration (`wait: 500ms`). `poll` repeats a call until its `until` assertions hold, bounded by `timeout` and/or `max_attempts` — at least one bound is required, so a flow can never spin forever.

## Chaining: extract, then template

`extract` pulls values from a step's response into named variables:

```yaml
extract:
  token: $.auth.token      # JSONPath into the body — paths always start with $
```

Later steps inject them with `{{ token }}` templates. Three roots are available: variables extracted by **earlier** steps, `{{ user.* }}` for the flow's [data pool](data-pools.md) row, and `{{ env.* }}` for process environment. Templating reaches URLs, headers, query, bodies, GraphQL variables (never the query document), WS frames, gRPC messages, and auth fields. A template with no upstream source — a typo, a step ordered wrong — is a pre-run error quoting it, not a runtime surprise at VU 4,000.

Every `{{ env.* }}` value is registered for redaction: it appears as `[redacted]` in every captured payload and every span the run stores. There is no bespoke secrets mechanism (ADR 0005) — your environment supplies credentials, the engine makes sure they never land on disk.

## Assertions

Assertions are `subject op value` expressions:

```yaml
assert:
  - status == 200
  - latency < 800ms
  - header.Content-Type contains "application/json"
  - $.items[0].sku == "A-100"
  - token != null
```

Subjects: `status`, `latency`, `header.<Name>`, a `$.` JSONPath into the body, or a variable an earlier step extracted. Operators: `==` `!=` `<` `<=` `>` `>=` `contains` `matches` (regular expression). `!= null` asserts existence; `== null` asserts absence. Asserting on a variable nothing extracts is a pre-run error. The full grammar is in the [YAML reference](../reference/yaml-dsl.md).

## When a step fails

A failed assertion (or a failed exchange) marks the flow-run `failed`, and remaining steps are `skipped` — chained steps depend on their predecessors, so running them would only manufacture noise. `on_failure` overrides that per step:

```yaml
on_failure: record      # keep going; this step's failure is data
```

`record` continues the flow, `abort_flow` is the default skip-the-rest, `abort_run` stops the whole run — for the step whose failure means nothing after it can be trusted.

In integration/system mode failures are loud and per-iteration; in load/stress/soak they are counters and samples — data, not test failures — judged in aggregate by the profile's [thresholds](profiles-and-thresholds.md).

## Layout convention

Flows live where the code they test lives. The conventional layout, which the examples and the [quickstart](../getting-started/quickstart.md) follow:

```text
tests/
  flows/          # *.flow.yaml and *.py — one flow per file
  scenarios/      # profiles composed over flows, if kept separate
  fixtures/       # CSV/JSON data pools
  targets/        # target configs; flowbench run --targets-dir points here
runs/             # the run store (git-ignore it)
```
