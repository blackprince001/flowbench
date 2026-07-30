# Gating on exit codes

Thresholds are the run's verdict: a profile declares what "good" means (`p95(latency) < 800ms`, `error_rate < 1%`), the collector evaluates them when the run ends, and the exit code carries the result. That is the entire v1 contract with CI — a pipeline runs `flowbench run` and gates on the code, nothing more.

## The contract

| Code | Meaning |
|---|---|
| `0` | every iteration passed and every threshold held |
| `1` | ran, but assertions or thresholds failed |
| `2` | pre-run error — bad arguments, a parse/validation failure, or the safety gate (host allow-list, disallowed mode) |

`2` is worth gating separately from `1`: it means nothing was measured — the run was refused or misconfigured, which is a broken pipeline, not a slow service.

## The demo

[`examples/load-local/breach.flow.yaml`](../../examples/load-local/breach.flow.yaml) demands p95 under 5 ms from a target that answers in ~15 ms, so the gate fires:

```yaml
flow: checkout_tight_slo
steps:
  - id: checkout
    call: POST /checkout
    assert:
      - status == 200
profile:
  mode: load
  vus: 20
  arrival_cap: 150/s
  hold: 3s
  thresholds:
    - p95(latency) < 5ms # target serves in ~15ms → BREACH
    - error_rate < 1%
```

```text
  p95(latency) < 5ms: BREACH  (p95(latency) = 16.404ms, want < 5ms)
  error_rate < 1%: ok
```

Exit `1`, breach named. The run artifact is still saved to `runs/`, so a failed gate leaves evidence to open in the results server. Loosen the threshold and the same flow passes.

## A minimal gate

```bash
# in any CI step or pre-deploy script
flowbench run flows/checkout.flow.yaml --target targets/staging.yaml || exit 1
```

Or, when you want `1` and `2` handled differently:

```bash
flowbench run flows/checkout.flow.yaml --target targets/staging.yaml
case $? in
  0) echo "thresholds held" ;;
  1) echo "threshold breach — see runs/" ; exit 1 ;;
  2) echo "run refused before starting — fix the pipeline" ; exit 2 ;;
esac
```

Remember that a `429` classifies as `throttled` and does not count against `error_rate` — gate on a `throttle_rate` threshold too if being rate-limited should fail your pipeline. See [Profiles and thresholds](../guide/profiles-and-thresholds.md) for the threshold surface.

## Honest scope

This is the whole story in v1, on purpose. The PRD excludes CI integration from v1 — the CLI's design must not preclude it (clean exit codes, machine-readable output), but CI recipes, gating semantics, and pipeline ergonomics are explicitly future work, deferred to v2. What exists today is the exit-code contract above, and it is enough for a `|| exit 1` gate.
