# flowbench (Python SDK)

The Python authoring surface: declarative flows that compile to the canonical
IR (ADR 0002) and execute on the Go engine at full VU scale, or run here
directly (`python file.py`) as the Python-driven producer, writing to the same
run store (ADR 0012).

## Prompt observation

FlowBench never makes the LLM call, templates a prompt, or sets a model
parameter (ADR 0009). Your code calls whatever SDK you already use, and
`ctx.prompt(...)` wraps that call so the exchange is recorded:

```python
client = OpenAI(api_key=flowbench.secret(os.environ["OPENAI_API_KEY"]))

@flow.step
def classify(ctx):
    messages = [{"role": "system", "content": SYSTEM},
                {"role": "user", "content": str(ctx.user["ticket"])}]
    with ctx.prompt("classify", template=SYSTEM, pace="20/m", burst=3) as p:
        reply = client.chat.completions.create(model="gpt-4o-mini", messages=messages)
        p.record(messages, reply.choices[0].message.content, usage=reply.usage)
    expect(p.completion).to_contain("refund")
```

- **The pair is always captured**, every iteration, whatever the outcome — a
  diff needs both sides. Redaction and the size cap still apply.
- **`template=` is the prompt's identity.** It is hashed, so the diff view can
  tell "the prompt changed" from "the output changed under the same prompt";
  without one the recorded content is hashed instead, which moves whenever a
  substituted value does.
- **`variant="concise"` is a label**, not machinery: what varies is your code,
  and the label gives that version its own span identity (`classify@concise`)
  so folding, metrics and diffs stay per-variant. Because it is an identity,
  renaming one splits a flame graph into a before and an after — a clean run
  warns on stderr when a name earlier runs of the same scenario recorded is
  no longer there.
- **`pace="20/m"` is a client-side ceiling** keyed to the observation name, so
  a run over 500 fixture rows doesn't trip the provider's rate limit in the
  first place. `burst=` lets the first N calls go unspaced. A wait shows up as
  its own `pace` span beside the observation.
- **`timeout=` is measured, not enforced** — the call is your SDK's, and
  FlowBench has no handle to cancel it. An overrun fails the observation after
  the fact; pass the same bound to your SDK's own timeout to actually cut it
  short.
- The provider's HTTP resolves as child spans of the observation, so the round
  trip is never opaque in the flame graph. A provider `429` classifies as
  `throttled`, not `failed` (ADR 0006). Async clients count: a batch gathered
  inside one observation lands under it, call by call. Two observations open
  concurrently do not — gather the calls inside one, or take them in turn.

Observation only runs on the Python-driven path: `flowbench run file.py`
compiles to IR the Go engine executes, and there is no Python there to wrap, so
compiling a flow that observes a prompt is refused rather than silently
dropped.

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

The Go conformance suite (`internal/conformance`) compiles each `tests/flows/*.py`
fixture and diffs the resulting IR against the YAML parser's output for the
`.flow.yaml` of the same name — a flow written twice must compile to one
representation. It picks up `sdk-python/.venv` automatically, so run `uv sync`
before `go test ./...`, or point `FLOWBENCH_PYTHON` at an interpreter of your
own. Without either it skips rather than fails.
