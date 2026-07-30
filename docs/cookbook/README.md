# Cookbook

Worked patterns, each one runnable from [`examples/`](../../examples/README.md) in the repo. Every recipe follows the same arc: the situation, the flow, what to look at when it runs, and why it works. The YAML is quoted from the example files, so you can run each recipe as you read it.

- [Login-chained flows](login-chained-flows.md) — the login → extract token → act → assert shape, with flow-level auth and redaction proof.
- [Retry and backoff](retry-backoff.md) — recovering throttles and flaky 5xx, and reading per-attempt spans in the waterfall.
- [Rate limits and capacity](rate-limits-and-capacity.md) — arrival caps, throttle-vs-error classification across protocols, and the stress knee.
- [Prompt testing](prompt-testing.md) — pinned-params regression, variants, pace guards, and the prompt diff view.
- [Gating on exit codes](ci-gating.md) — thresholds as the run's verdict, and the `0/1/2` contract a pipeline can gate on.
- [Data-driven sweeps](data-driven-sweeps.md) — one iteration per fixture row, distribution policies, and pool exhaustion.
