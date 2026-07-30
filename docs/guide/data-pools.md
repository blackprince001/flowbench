# Data pools

A data pool is a fixture source — a CSV or JSON file — whose rows feed a flow's iterations, so concurrent VUs draw distinct data instead of hammering one hard-coded value. It is also the sanctioned seeding mechanism: the same rows that drive a sweep are the rows the test data came from.

## The `data:` shorthand

One flow-level key binds a fixture file, and its rows become `{{ user.* }}`:

```yaml
flow: bored_filter_sweep
data: filters.csv
steps:
  - id: filter
    call: GET /filter
    query:
      type: "{{ user.type }}"
      participants: "{{ user.participants }}"
    assert:
      - status == 200
```

(from `examples/bored-api/sweep.flow.yaml`)

The path resolves relative to the flow file. Each iteration gets one row, and the row's fields are addressed as `{{ user.<column> }}` — the validator checks the references the way it checks any template, so a `{{ user.* }}` in a flow that binds no `data:` is a pre-run error.

## Fixture formats

The format is inferred from the extension. A `.csv` file's first row is the header, and each following row is one record:

```csv
type,participants
education,1
recreational,1
```

A `.json` file is an array of objects:

```json
[
  { "type": "education", "participants": 1 },
  { "type": "recreational", "participants": 1 }
]
```

Scalar values become strings; a non-scalar value (a nested object or list) is carried as its JSON text, so a template injects it verbatim — which is how a fixture row can supply a whole request body fragment, not only a field.

A row's values go wherever a template goes: a query parameter as above, a header, a `body` field, a GraphQL variable, a gRPC message field (see [protocols](protocols.md)). An empty file is a pre-run error — a pool with no rows has nothing to run the flow over.

## One row, one iteration

What a bound pool means depends on the [profile](profiles-and-thresholds.md)'s mode. In `integration` and `system` modes the flow runs once per fixture row — a 1000-row file is 1000 iterations, each reported individually. In `load`, `stress`, and `soak` modes the VU pool drives the iteration count, and each iteration draws a row from the pool on its way in.

## Distribution, exhaustion, and `--seed`

Rows are dispensed `unique-per-vu` by default: every draw hands out a distinct row, and no two VUs ever share one. That guarantee has a consequence under load — the pool can run out. When a unique-per-vu pool is exhausted mid-run, the remaining iterations have no row to draw, and any step that references `{{ user.* }}` fails with a resolution error. Size the fixture for the iterations the profile will produce, or accept that the tail of the run fails.

The `random` distribution draws from a seeded RNG, and `--seed` makes those draws reproducible:

```bash
flowbench run sweep.flow.yaml --target staging --seed 7
```

The default seed is `1`, so two runs with no flag draw identically.

## What the IR carries that YAML v0 does not

The [IR](../reference/yaml-dsl.md) underneath is richer than the shorthand: a scenario can declare multiple *named* pools, each with an explicit `format`, a `distribution` (`unique-per-vu`, `round-robin`, `random`), and an `on_exhausted` policy (`fail`, `cycle`, `stop`), and the executor honors all of them — a pool declared with `cycle` wraps around instead of running dry. YAML v0 deliberately exposes none of that: `data: <path>` compiles to a single pool named `user` with the defaults (`unique-per-vu`, `fail`), because one flow with one fixture is the overwhelming case and the shorthand should not have to grow keys before someone needs them. When the richer surface is worth its keys, it will be an addition to the flow file, not a change to this one.
