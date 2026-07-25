# Deck of Cards API — chaining against a real, stateful service

[deckofcardsapi.com](https://deckofcardsapi.com) is free, needs no key, and is
genuinely **stateful**: a shuffled deck lives on the server under an id, and the
cards you draw are gone from it. That state is what makes it worth pointing a
chained flow at — a fixed JSON endpoint can demonstrate a request, but not a
flow whose every step depends on what the last one returned.

```sh
flowbench run examples/deck-of-cards/smoke.flow.yaml --target examples/deck-of-cards/target.yaml
flowbench run examples/deck-of-cards/deal.flow.yaml  --target examples/deck-of-cards/target.yaml
flowbench run examples/deck-of-cards/table.flow.yaml --target examples/deck-of-cards/target.yaml
flowbench serve --store runs
```

## `smoke.flow.yaml` — the chain

Shuffle a deck, keep its id, draw from *that* deck, put the drawn cards in a
pile, read the pile back. This is the login → token → act pattern without the
auth, and it exercises the whole authoring surface in one flow:

- **extraction** into flow variables (`$.deck_id`, `$.cards[0].code`)
- **injection** of those variables into a later path (`/api/deck/{{ deck_id }}/…`)
  and into a query value that combines two of them
- **assertions** across every source — `status`, `latency`, `header.Content-Type`,
  JSONPath into the body, and a bare variable name
- **operators** beyond equality: `matches` against a regex, `contains`, `!= null`
- a **`wait`** step, which appears in the trace as a step of its own

The proof that the state is real: after drawing two cards the deck asserts
`$.remaining == 50`, and the pile it builds is still there to be read back.

Integration mode is the exit-code contract: `0` clean, `1` a recorded failure
naming the step, `2` a pre-run error (bad arguments, a parse error, or the
safety gate). It prints and exits — only load, stress and soak write a run
artifact for the results server.

## `deal.flow.yaml` — one iteration per row

The same chain bound to `players.csv`, which decides how many cards each player
is dealt. The row arrives as `{{ user.* }}` and drives the request, so the
fixture changes what the flow does rather than just what it says.

Integration mode runs the flow once per row and stops, which is exactly what the
default unique-per-VU pool is for: every iteration gets a row of its own, and the
run ends when the rows do.

> A **load** profile bound to a pool is a different matter — it will exhaust a
> five-row fixture in five iterations and then fail every one after. Either size
> the fixture to the run, or leave the pool out, as `table.flow.yaml` does.

## `table.flow.yaml` — a run you can open

The chain again under a load profile, so there is something in the run store to
look at: the flame graph folds the three steps across every iteration, the
waterfall shows one iteration's spans in causal order with the captured
request and response on each call, and the charts plot throughput and latency
percentiles over the run.

It is deliberately tiny — 2 VUs at 2 flow-runs a second for eight seconds, about
eighty requests. This is a free hobby API; that is roughly a person clicking
around the site.

## Safety rails

`target.yaml` is the allow-list: these scenarios may reach `deckofcardsapi.com`
and nothing else, a call is given five seconds before it counts as failed, and
**stress and soak are refused outright**. Try it:

```sh
sed 's/mode: load/mode: stress/' examples/deck-of-cards/table.flow.yaml > /tmp/nope.flow.yaml
flowbench run /tmp/nope.flow.yaml --target examples/deck-of-cards/target.yaml ; echo "exit $?"
# flowbench: target "deck-of-cards" disallows "stress" mode      (exit 2)
```

Refused before a single request is sent. Load at real scale belongs against the
local stub in [`examples/load-local`](../load-local), and the failure taxonomy
against [`examples/httpbingo`](../httpbingo) — a service that exists to be
tested against.
