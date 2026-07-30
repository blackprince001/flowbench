# Installation

FlowBench ships as two Go binaries and a Python package. The engine + CLI (`flowbench`) is all you need to author, run, and read tests; the agent (`flowbench-agent`) is copied onto the host under test when you want resource overlays; the Python SDK is optional, for teams authoring flows in Python.

## The engine + CLI

From a [GitHub release](https://github.com/blackprince001/flowbench/releases), download the archive for your platform, unpack it, and put `flowbench` on your `PATH`:

```bash
tar -xzf flowbench_<version>_<os>_<arch>.tar.gz
sudo mv flowbench /usr/local/bin/
flowbench version
```

Or build from source (Go 1.26+):

```bash
git clone https://github.com/blackprince001/flowbench.git
cd flowbench
go build -o flowbench ./cmd/flowbench
```

Every dependency is pure Go — there is no CGO, so cross-compiling with `GOOS`/`GOARCH` works without a toolchain for the target.

## The agent

The agent runs on the host under test, not the machine generating load. Download `flowbench-agent` from the same release (or `go build ./cmd/flowbench-agent`), copy it over, and start it:

```bash
flowbench-agent --addr :9090
```

Host sampling is implemented for **Linux only** in v1 — the agent reads `/proc`. It compiles everywhere, but on other platforms it serves an error and the run proceeds without an overlay (fail-open). See [the agent guide](../guide/agent.md).

## The Python SDK

The SDK requires Python 3.10+ and depends only on `httpx`:

```bash
pip install flowbench
# or
uv add flowbench
```

To work against a checkout instead — developing the SDK itself, or tracking `main`:

```bash
pip install ./sdk-python
# or, working inside the repo with uv:
uv sync --project sdk-python
```

A release also attaches the wheel and sdist, which install the same way. The SDK shells out to the `flowbench` binary to resolve named targets, so install the CLI too — or pass `base_url=` to `flow.run()` and skip target resolution entirely. See [the Python SDK guide](../guide/python-sdk.md).

## Verifying the install

```bash
flowbench version         # prints the build identity
flowbench help            # the four commands: run, serve, target, version
```

Next: the [Quickstart](quickstart.md).
