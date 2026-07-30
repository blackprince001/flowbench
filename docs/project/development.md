# Development

Working on FlowBench itself: the layout, the checks, and the suites that gate a change. Go 1.26+ and, for the Python side, [uv](https://docs.astral.sh/uv/) with Python 3.10+.

## Layout

```text
cmd/flowbench          # the CLI: run, serve, target, version
cmd/flowbench-agent    # the target-metrics agent
internal/              # engine packages — parser, ir, planner, executor,
                       # adapters, collector, store, server, report, agent, …
sdk-python/            # the Python SDK (src layout, uv-managed)
examples/              # worked examples, each with a README and often a stub
tests/                 # conformance fixtures: flows/ (YAML+Python pairs),
                       # fixtures/, targets/
docs/                  # this book, the PRD, ADRs, planning, benchmarks, log
```

## The checks

CI runs exactly these; run them locally before pushing:

```bash
gofmt -l .                                   # must print nothing
go vet ./...
go build ./...

uv sync --locked --project sdk-python        # the conformance suite needs the venv
FLOWBENCH_PYTHON="$PWD/sdk-python/.venv/bin/python" \
FLOWBENCH_REQUIRE_PYTHON=1 \
  go test -race ./...

uv run --frozen --project sdk-python ruff check  --config sdk-python/ruff.toml sdk-python tests/flows
uv run --frozen --project sdk-python ruff format --check --config sdk-python/ruff.toml sdk-python tests/flows
uv run --locked --directory sdk-python pytest
```

Two flags matter more than they look. `FLOWBENCH_REQUIRE_PYTHON=1` turns the conformance suite's "no usable interpreter" skip into a failure — without it, green can mean skipped. And the agent's host-sampling tests read `/proc`, so they pass on Linux (CI) and fail on macOS/Windows; that is expected, not breakage.

## The conformance suite

`internal/conformance` holds the two-surface parity contract (ADR 0002): for each fixture in `tests/flows/`, the YAML file is parsed and the Python file is compiled, and the canonicalized IR must match byte for byte. A second half builds the real CLI and drives both producers against a shared stub, checking the run-store artifacts agree (ADR 0012). Adding authoring surface means adding a fixture pair.

```bash
FLOWBENCH_PYTHON="$PWD/sdk-python/.venv/bin/python" FLOWBENCH_REQUIRE_PYTHON=1 \
  go test -race ./internal/conformance/...
```

## The footprint benchmark

The 10k-VU claim is a measured one. The harness is `TestVUFootprintBenchmark` in `internal/executor` (skipped under `-short`), with knobs in the [environment reference](../reference/environment.md); methodology and the reference numbers live in [the benchmark doc](../benchmarks/10k-vu-footprint.md).

```bash
FLOWBENCH_BENCH_VUS=10000 FLOWBENCH_BENCH_DUR=5s \
  go test -run TestVUFootprintBenchmark -v -timeout 180s ./internal/executor/
```

## Running every example

```bash
./examples/run-all.sh --serve   # several examples exit nonzero by design
```

`examples/run-all.sh` drives every worked example into one run store and can serve it (`--serve`); it is the closest thing to an end-to-end smoke test of the whole surface.

## Conventions

- Trunk is `main`; changes land by PR, and the `build & test` check is required — its job name is load-bearing.
- Decisions worth remembering get an ADR in `docs/decisions/`; work worth narrating gets an entry at the top of [the log](../log.md).
- The vocabulary in [CONTEXT.md](../CONTEXT.md) is enforced prose-wide — a flow is not a "test case" in code comments either.
