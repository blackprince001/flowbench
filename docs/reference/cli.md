# CLI reference

Two binaries: `flowbench` (engine, CLI, results server) and `flowbench-agent` (target-host metrics). Flags use the Go standard library's `flag` conventions: `--flag value` or `--flag=value`; single-dash forms work too.

## Exit codes

Both binaries share the contract, and it is what CI gates on:

| Code | Meaning |
| --- | --- |
| `0` | clean pass — thresholds held, no recorded failures |
| `1` | recorded failures, a threshold breach, an abort, or an interrupt |
| `2` | pre-run error — bad arguments, parse/validation failure, safety-gate refusal |

## `flowbench run <scenario> [flags]`

Runs a scenario: parse, validate, gate against the target, execute per the profile. The scenario is a `.flow.yaml` file, or a `.py` file authored with the SDK (compiled to IR, then executed by this engine). The positional path may come before or after the flags.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--target` | `local` | target config name, or a path to a target file |
| `--targets-dir` | `targets` | directory `--target` names resolve in |
| `--seed` | `1` | seed for random data-pool draws |
| `--store` | `runs` | run store directory to save artifacts into |
| `--watch` | off | serve a live, abortable view of the run (load/stress/soak; ignored with a warning elsewhere) |
| `--addr` | `127.0.0.1:7580` | bind address for the `--watch` server |

`--target local` with no `targets/local.yaml` present falls back to `http://localhost:8080`. Ctrl-C cancels the run cleanly; the partial run is still saved. Integration/system print per-iteration `ok` / `FAIL` lines naming failing steps; load-family runs print the aggregate summary, threshold verdicts, and — for a breaching stress run — the `knee_point_found:` line.

## `flowbench serve [flags]`

Serves run stores read-only in a browser.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--store` | `runs` | a store to serve as a project; repeatable; `name=dir` labels one |
| `--addr` | `127.0.0.1:7580` | bind address |

Binding beyond loopback prints a warning — runs carry captured bodies.

## `flowbench target <name> [--targets-dir dir]`

Resolves a target config exactly as `run` would and prints it as JSON. This is the single source of truth the Python SDK shells out to for target resolution.

## `flowbench version`

Prints the build identity (`flowbench <version> (commit <sha>)`). Also available as `--version`.

## `flowbench-agent [flags]`

Runs on the host under test, serving `GET /metrics` with the current resource sample. Host sampling is Linux-only in v1; see [the agent guide](../guide/agent.md).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--addr` | `:9090` | listen address; non-loopback is the normal case |
| `--version` | — | print the build identity and exit (`flowbench-agent version` is *not* a subcommand) |
