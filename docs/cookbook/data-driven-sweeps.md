# Data-driven sweeps

The same flow, run once per row of a fixture: bind a data pool with `data:` and each iteration draws a row, exposed to templates as `{{ user.* }}`. A 1000-row file is 1000 iterations — the fixture drives what the flow does, not only what it says. Data pools are also the sanctioned mechanism for seeding state: rows designed after the request shapes of the system under test.

## One call per row

[`examples/bored-api/sweep.flow.yaml`](../../examples/bored-api/sweep.flow.yaml) runs one `/filter` call per row of [`filters.csv`](../../examples/bored-api/filters.csv):

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
      - header.Content-Type contains "application/json"
```

The CSV's header row names the variables — `type,participants` becomes `{{ user.type }}` and `{{ user.participants }}`. The public API behind this example is rate-limited to 100 requests / 15 min, so the full 1000-row sweep gets `429`-throttled partway; point it at a local or unrated target (`--target local`) for the whole run.

## The row drives what the flow does

[`examples/deck-of-cards/deal.flow.yaml`](../../examples/deck-of-cards/deal.flow.yaml) binds [`players.csv`](../../examples/deck-of-cards/players.csv) — five players, each dealt a different number of cards:

```yaml
flow: deal_a_hand
data: players.csv
steps:
  - id: shuffle_deck
    call: GET /api/deck/new/shuffle/
    extract:
      deck_id: $.deck_id

  # The fixture decides the hand size. `remaining` is asserted as a bound, not
  # an equality, because it differs per row.
  - id: deal
    call: GET /api/deck/{{ deck_id }}/draw/
    query:
      count: "{{ user.cards }}"
    assert:
      - status == 200
      - $.remaining < 52
profile:
  mode: integration
```

Two things worth copying. Integration mode runs the flow once per row and stops — the run ends when the rows do, which is exactly what a sweep wants. And when the fixture varies the request, the assertions have to survive every row: `$.remaining < 52` holds whether the row dealt one card or five, where `== 50` would only hold for one of them.

## Distribution: who gets which row

A data pool carries a distribution policy so concurrent VUs draw distinct rows: `unique-per-vu` (the default — every iteration gets a row of its own, uniqueness under concurrency is the engine's job), `round-robin`, and `random`. Under `unique-per-vu`, forty VUs sweeping a fixture never collide on a row, which matters as soon as rows are credentials or otherwise stateful — two VUs logging in as the same user is a different test than the one you wrote.

For `random` draws, `--seed` makes the sequence reproducible:

```bash
flowbench run sweep.flow.yaml --target staging --seed 42
```

Same seed, same draws — a failing iteration reproduces with the row that failed it, instead of a different sample each run.

## Pool exhaustion under load

A load profile bound to a pool will exhaust a five-row fixture in five iterations and then fail every iteration after — the default exhaustion policy is to fail, loudly, rather than silently recycle rows. That failure is information: it means your iteration count and your fixture size disagree. Either size the fixture to the run (the 1000-row `filters.csv` exists for exactly this), or leave the pool out of load scenarios entirely, as [`examples/deck-of-cards/table.flow.yaml`](../../examples/deck-of-cards/table.flow.yaml) does. Silent recycling would be worse: a "10,000-user" load test quietly replaying the same five users measures cache behavior, not capacity.

## Why it works

The pool is engine-owned state, not per-VU state: rows are handed out under the distribution policy across all VUs, and uniqueness under concurrency is the engine's responsibility, so it holds at any scale without the flow doing anything. The row lands in the same template namespace as everything else, which is why `{{ user.cards }}` composes with extraction (`{{ deck_id }}`) and environment secrets (`{{ env.* }}`) in one flow. See [Data pools](../guide/data-pools.md) for the full surface.
