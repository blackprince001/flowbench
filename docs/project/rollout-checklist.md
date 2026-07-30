# Rollout checklist

The dogfood exit criteria from [PRD §18](../prd.md) and the M4 exit line in [the milestone plan](../planning/milestones.md), tracked to done. Phasing: engine + CLI dogfooded by the authoring team on one real service → two pilot teams write flows for their own services → org-wide availability of the toolkit packages.

## Entry criteria

- [x] **Conformance suite green across both surfaces.** `TestTwoSurfaceParity` diffs canonicalized IR for every fixture pair in `tests/flows/`, and the live-execution half drives both producers against a shared stub and compares the stored artifacts. Runs in CI on every push with `FLOWBENCH_REQUIRE_PYTHON=1`, so a missing interpreter fails rather than skips. See [development](development.md).

- [x] **10k-VU benchmark met on the reference node.** 10,000 VUs on a single node with the generator at 3.0 cores (70% headroom), 68.8 KiB per VU, 672 MiB peak heap. Methodology, reference hardware, and the regression assertions are in [the benchmark doc](../benchmarks/10k-vu-footprint.md).

- [x] **A stress-run finding reproduced against a known bottleneck, flame graph pointing at the right step.** `examples/load-local/stress.flow.yaml` against the bundled rate-limited stub: 40% `throttle_rate` against ~0.1% `error_rate`, with the `checkout` step's `http_call` dominating the flame graph. Worked through in [the capacity recipe](../cookbook/rate-limits-and-capacity.md).

- [x] **At least one reproduced case of a stress run correctly identifying a throttled (not degraded) knee point.** Issue #39's acceptance runs as CLI-level tests in `cmd/flowbench/knee_test.go`: against a rate-limited stub with a flat-CPU agent the run reports `knee_point_found: throttled`; against a saturating stub with a climbing agent it reports `degraded`. The classification is persisted in the run's meta and rendered on the run page.

## Distribution

- [x] **Engine/CLI and agent as versioned binaries.** Built and attached per release; see [installation](../getting-started/installation.md).
- [x] **SDK as an internal Python package.** Built as a wheel from `sdk-python/`, attached to releases; installable with `pip install ./sdk-python` from a checkout.

## Docs

- [x] **Quickstart** — one YAML flow to first stress run and flame graph in under ten minutes: [quickstart](../getting-started/quickstart.md). Acceptance: a new user following it reaches a flame graph without outside help.
- [x] **DSL/SDK reference** — [YAML DSL](../reference/yaml-dsl.md), [CLI](../reference/cli.md), [Python API](../reference/python-api.md).
- [x] **Cookbook** — [login-chained](../cookbook/login-chained-flows.md), [retry/backoff](../cookbook/retry-backoff.md), and [prompt-observation patterns](../cookbook/prompt-testing.md) including the pinned-params regression recipe and pace guards.

## Still open (v1 caveats, stated rather than hidden)

- Install-to-first-run time has a `[X]` placeholder in the PRD — measure it with the pilot teams rather than inventing a number here.
- The agent samples Linux hosts only; other platforms run with no overlay.
- CI integration and gating recipes beyond the exit-code contract are v2 scope by design.
