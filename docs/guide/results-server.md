# The results server

Every run is saved to a plain directory you own — the run store — and `flowbench serve` renders it in a browser. The server is embedded in the CLI binary, entirely server-rendered HTML (ADR 0014), and read-only with one deliberate exception: aborting a live run.

```bash
flowbench serve                          # serve ./runs
flowbench serve --store perf/runs        # another store
flowbench serve --store api=runs --store checkout=../checkout/runs
```

Each `--store` becomes a **project** — a tab across the top of every page — so several services' runs read side by side. The default bind is `127.0.0.1:7580`; binding a non-loopback address warns, because runs carry captured request/response bodies.

## The run store on disk

A run is a directory named by a sortable UTC timestamp, under the store root:

```text
runs/
  index.json                     # newest-first run listing
  20260730T152144.803Z/
    meta.json                    # attribution, aggregates, thresholds, verdicts
    folded.json                  # tier 1: flame-graph aggregate
    traces.json                  # tier 2: sampled raw span trees
    series.json                  # tier 3: outcomes and latencies over time
    samples.json                 # tier 4: per flow-run, thinned by capture policy
    metrics.json                 # the generator's own resource series
    agent.json                   # the target's, when an agent was attached
```

`meta.json` answers who ran what, against which target, at which commit (and whether the flow file was dirty), plus the run's own verdict: evaluated thresholds, breach flag, and — for stress runs — the knee classification. Each view loads only the tier it needs. There is no retention machinery; deleting a run is deleting its directory.

## The run page

The run's front page is the summary and the shape at once: outcome tiles (flow-runs, duration, error rate, throttle rate, percentiles), the time-series small multiples, an outcome strip you can scrub bucket by bucket, funnels showing where flow-runs stopped, per-step time totals, the **Thresholds** table exactly as the CLI printed it, and the **Target resources** overlay (or its empty-state hint when no [agent](agent.md) was attached).

Two cards appear by mode. Soak runs get **Soak trend** — second half measured against first, with any persisted drift findings. Stress runs get **Knee point** — the `degraded` / `throttled` / `inconclusive` classification with its onset ("thresholds first broke 2.5s into the run, at ~340 VUs"), or a plain statement that the gates held.

## The tabs

| Tab | What it answers |
| --- | --- |
| **Over the run** | the time series full size — throughput, latency percentiles, error/throttle rates, VUs; `?chart=` opens one with per-series figures |
| **Flame graph** | where aggregate time went; frames are span paths, width is total time; zoom into any frame |
| **Waterfall** | one iteration, exactly as it happened — every span with its phases, payloads where captured |
| **Failures** | failures grouped by step and cause, each opening onto the iteration's waterfall |
| **Outcomes** | every flow-run as a cell, colored by outcome; throttles drawn as throttled even where the mode counts them as errors |
| **Compare** | this run against a baseline — the previous run of the same scenario by default, `?with=` for any other: metric deltas, threshold flips, the worst-regressed step, both flame graphs |
| **Prompts** | the prompt-diff view, present when the run recorded observations — see [prompt observation](prompt-observation.md) |

Every selection — a chart, a frame, a trace, a comparison — is URL state, so a finding worth discussing is a link, not directions to click.

## The prompt diff view

For runs with prompt observations, `/runs/{id}/prompts` compares completions: two variants within a run, or the same variant against an earlier run. The verdict leads — *prompt changed* (with the prompt's own diff shown above the output's) or *same prompt*, by hash — so a moved output is never misread as model drift when it was your edit. Diffs pair lines, then diff words within a line; JSON completions are re-indented both-or-neither and also structurally diffed, naming changed fields by JSONPath. Token usage shows as counts, not percentages.

## Watching a run live

```bash
flowbench run stress.flow.yaml --target staging --watch
```

`--watch` (load/stress/soak) serves a live page from the run's own process: elapsed, VUs, completions, RPS, error and throttle rates, updated twice a second over SSE — and an **abort** button, the server's one write path, which cancels the run cleanly and still saves the partial artifact. When the run finishes, the page hands off to the full read-only report of the stored run. `--addr` moves the bind address.

## Comparing runs over time

Because the store is plain files with full attribution, regression review is built in rather than bolted on: run the same scenario after a change, open **Compare**, and read the deltas with the thresholds' verdicts beside them. The [CI-gating recipe](../cookbook/ci-gating.md) covers wiring the exit code into a pipeline.
