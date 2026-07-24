# flowbench (Python SDK)

The Python authoring surface: declarative flows that compile to the canonical
IR (ADR 0002) and execute on the Go engine. Nothing here runs at test time —
step functions run once, at compile time, against a symbolic tracing context.

## Development

The toolchain is [uv](https://docs.astral.sh/uv/). It provisions the
interpreter pinned in `.python-version` and the dev dependencies in the
lockfile, so no manual venv or `pip install` is needed.

```sh
uv sync                 # create .venv and install the package + dev deps
uv run pytest           # unit tests
uv run ruff check .     # lint
uv run ruff format .    # format
```

The Go conformance suite (`internal/conformance`) compiles
`tests/flows/authenticated_checkout.py` and diffs the resulting IR against the
YAML parser's. It picks up `sdk-python/.venv` automatically, so run `uv sync`
before `go test ./...`, or point `FLOWBENCH_PYTHON` at an interpreter of your
own. Without either it skips rather than fails.
