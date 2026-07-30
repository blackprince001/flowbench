# The target-metrics agent

Without the target's side of the story, a load test can only see its own: latency rose, errors rose — but was that the target saturating, the generator saturating, or a rate limiter doing its job? The agent is a single small binary on the host under test that answers the question, streaming that host's resource use into the run so the three are never confused.

## Running it

Copy `flowbench-agent` onto the target host and start it:

```bash
flowbench-agent --addr :9090
```

It binds a non-loopback address by design — it must be reachable from wherever `flowbench run` executes. It serves exactly one endpoint, `GET /metrics`, returning the current reading as JSON; everything else is a 404. There is no push, no buffering, no state on the agent side: the engine scrapes it (ADR 0017), so a run that never happens costs the target host nothing.

Host sampling reads `/proc` and is **Linux-only in v1**. The binary compiles on other platforms but serves an error there, and a run attached to it proceeds with no overlay rather than a broken one.

## Attaching it to runs

Point the target config at it:

```yaml
# targets/staging.yaml
base_urls:
  - https://staging.shop.internal
agent_addr: staging.shop.internal:9090
```

Every load-family run against that target polls the agent once per second — the same cadence as the generator's own self-metrics, so the two series line up — and saves the result as `agent.json` in the run directory. The run's meta records `agent_attached`, derived from the saved series itself, never from a flag.

Polling is **fail-open** on its own context: an agent that is down, stalls, or dies mid-run can never block or fail flow execution. Failed scrapes are dropped; the series just has gaps.

## What it measures

Each sample carries cumulative CPU seconds (with the core count, so a consumer diffs adjacent samples for utilization), memory used/total, cumulative network rx/tx bytes, open file descriptors, and 1-minute load average. Cumulative counters follow the same diff-two-samples convention as the generator's own metrics.

## What it unlocks

- **The Target resources overlay** on the run page: target CPU against generator CPU (as cores burned), target RSS against generator heap — on the run's own time axis. A saturated generator is visible as itself, not misread as a slow target.
- **The stress knee classification**: with an agent attached, a breaching stress run is classified `degraded` (resources climbed with the break — genuine saturation) or `throttled` (throttle rose over flat resources — an enforced limit). Without one, the finding is honestly `inconclusive`. See [profiles and thresholds](profiles-and-thresholds.md) and the [capacity recipe](../cookbook/rate-limits-and-capacity.md).

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--addr` | `:9090` | listen address for `GET /metrics` |
| `--version` | — | print the build identity and exit |
