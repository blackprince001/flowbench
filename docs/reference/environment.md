# Environment variables

Configuration FlowBench reads from the process environment. Everything here is optional; the defaults are the dev-checkout behavior.

## Authoring and templating

| Variable | Read by | Meaning |
| --- | --- | --- |
| anything via `{{ env.NAME }}` | the engine | injected into templated fields at run time; every value read this way is registered for redaction and appears as `[redacted]` in stored payloads |

## The Python bridge

| Variable | Read by | Meaning |
| --- | --- | --- |
| `FLOWBENCH_COMPILE_ONLY` | the SDK | set by `flowbench run flow.py`: `flow.run()` prints compiled IR instead of executing — not usually set by hand |
| `FLOWBENCH_PYTHON` | `flowbench run`, conformance suite | the Python interpreter to compile `.py` flows with; default is `sdk-python/.venv`'s, then `python3` on `PATH` (3.10+ enforced) |
| `FLOWBENCH_SDK_PATH` | `flowbench run` | where `sdk-python/` lives; the default searches upward from the flow file, which assumes a dev checkout |
| `FLOWBENCH_BIN` | the SDK | the `flowbench` binary used to resolve named targets; default is `flowbench` on `PATH` |

## Test and benchmark harnesses

| Variable | Read by | Meaning |
| --- | --- | --- |
| `FLOWBENCH_REQUIRE_PYTHON` | conformance suite | turn "no usable interpreter" from a skip into a failure — CI sets it so green can never mean skipped |
| `FLOWBENCH_BENCH_VUS` | `TestVUFootprintBenchmark` | VU count for the footprint benchmark (default 1000) |
| `FLOWBENCH_BENCH_DUR` | 〃 | benchmark duration (default 2s) |
| `FLOWBENCH_BENCH_LATENCY` | 〃 | simulated target latency (default 100ms) |

The benchmark methodology and reference numbers live in [the 10k-VU footprint doc](../benchmarks/10k-vu-footprint.md).
