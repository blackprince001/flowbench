# Python SDK

The Python surface authors the same flows YAML does — a `Flow`, its steps, extractions, assertions — and compiles them to the same canonical IR. What Python adds is code: computed values, real control flow, and [prompt observation](prompt-observation.md), which YAML cannot express because it cannot call your SDKs.

## Two ways to execute

A Python flow file has two execution paths (ADR 0012), and which one you pick decides what runs your code:

- **Compile path** — `flowbench run flow.py`. The Go CLI runs your file once in compile-only mode, your step functions are traced into the canonical IR, and the Go engine executes that IR at full VU scale. No Python runs at execution time; this is exactly the YAML path with a different front end.
- **Python-driven path** — `python flow.py`. `flow.run(profile)` executes the flow in-process: real httpx calls, real assertions, spans accumulated as it goes. It is restricted to `integration` and `system` modes — `load`, `stress`, and `soak` raise a `FlowExecutionError` pointing you at the engine, because a single Python process is honestly not a VU-scale scheduler. Iterations run one after another, one per data-pool row.

Both paths write to the same run store, so a Python-driven run sits next to an engine run in `flowbench serve` and the two are comparable — same span model, same flame graphs, same waterfall. The Python-driven path exists for the one thing only it can do: run the flow's own Python, which is where prompt observation lives.

## Authoring a flow

A flow is a `Flow` object plus decorated step functions, executed in declaration order. Each step makes exactly one call, and everything else in the function — extraction, assertion, computation — hangs off it:

```python
from flowbench import Flow, Profile, Retry, expect

flow = Flow("authenticated_checkout", data="fixtures/users.csv")

@flow.step
def login(ctx):
    r = ctx.http.post("/auth/login", json={
        "email": ctx.user["email"],
        "password": ctx.user["password"],
    })
    expect(r.status).to_be(200)
    ctx.vars["token"] = r.json_path("$.data.access_token")

@flow.step(retry=Retry(on_status=[429, 503], backoff="honor_retry_after", max_attempts=5))
def create_order(ctx):
    r = ctx.http.post(
        "/orders",
        headers={"Authorization": f"Bearer {ctx.vars['token']}"},
        json={"items": ctx.user["cart"]},
    )
    ctx.vars["order_id"] = r.json_path("$.data.id")

if __name__ == "__main__":
    flow.run(Profile(mode="integration"))
```

`Flow(name, data=...)` binds a data pool; `ctx.user["email"]` reads the current row. `@flow.step(retry=..., auth=...)` attaches a per-step retry policy or auth scheme, and `Flow(..., auth=...)` sets a default every step inherits unless it declares its own (`NoAuth()` opts a step out). The auth classes — `Bearer`, `Basic`, `ApiKey`, `Cookie`, `OAuth2ClientCredentials`, `Hmac`, `NoAuth` — compile to the same IR the YAML `auth:` block does; pass `env("API_TOKEN")` rather than a literal credential:

```python
from flowbench import Bearer, env

flow = Flow("orders", auth=Bearer(env("API_TOKEN")))
```

Full signatures for all of these are in the [Python API reference](../reference/python-api.md).

## Running it

The same file runs both ways:

```bash
# compile path: the Go engine executes, at whatever scale the profile asks for
flowbench run checkout.py --target staging

# python-driven path: this process executes, integration/system only
python checkout.py
```

A Python-driven run prints one line per iteration and tells you where the run landed:

```text
  authenticated_checkout [1/3]  ok
  authenticated_checkout [2/3]  ok
  authenticated_checkout [3/3]  ok
3 iteration(s): 3 passed, 0 failed
run saved to runs/20260730T140211.482000000Z
```

Like an engine run, it records the initiator, the target, and the flow file's git commit (and whether the file had uncommitted changes), so a run in the store is attributable either way. `flowbench serve` then reads the run store and shows both kinds of run side by side.

## The ctx surface

`ctx` is what a step function receives, and it is the whole authoring vocabulary:

- `ctx.http.get/post/put/patch/delete(url, json=, headers=, query=)` — one HTTP call, returning a response whose `.status`, `.header(name)`, and `.json_path(path)` feed `expect(...)` and `ctx.vars[...] = ...`.
- `ctx.graphql(url, query=...)` — a GraphQL operation as a real `graphql` step, so the engine reads the `data`/`errors` shape and fails on an operation error inside a `200 OK`.
- `ctx.ws(...)` — one step's worth of work on a WebSocket session; a session opened in one step can be used by later ones.
- `ctx.grpc(method, proto=...)` — a unary gRPC call.
- `ctx.prompt(name, ...)` — a [prompt observation](prompt-observation.md) around an LLM call your own code makes.
- `ctx.vars[...]` — flow variables: assign a `json_path` extraction, read it in a later step.
- `ctx.user[...]` — the current data-pool row (only with `Flow(..., data=...)`).
- `ctx.env[...]` — a process environment variable, registered for redaction.
- `ctx.secret(value)` — flags a value the step computed (a token minted mid-flow) as sensitive.

Everything compiles, but live execution does not yet cover the whole surface: `ctx.graphql`, `ctx.ws`, `ctx.grpc`, and the auth schemes raise a `FlowExecutionError` on the Python-driven path today, pointing you at `flowbench run`, rather than silently sending the wrong thing.

## httpx auto-instrumentation

On the Python-driven path, the calls that matter are often not `ctx.http`'s — they are made by your own clients and SDKs, which never hear of FlowBench. So the SDK patches `httpx.Client.send` and `httpx.AsyncClient.send` once, at the class level: every request made while a flow is executing is recorded, and requests made when no flow is running pass through untouched, so importing `flowbench` does not instrument an unrelated process.

Recorded calls take the Go adapter's span shape, so the run store cannot tell which producer wrote a run: a call span, an `http_call` leg per redirect hop, and `connect`/`tls`/`ttfb`/`transfer` phase children as they occur. Async clients are recorded too, including a gathered batch — each request's phases land on its own span.

Three known gaps, deliberately visible rather than papered over: there is no `dns` span (httpcore resolves the hostname inside its connect, so a Python-produced `connect` covers name resolution); non-httpx clients — `requests`, raw sockets — pass through unrecorded; and a thread your step spawns does not inherit the instrumentation context, so calls made on it are not recorded (asyncio tasks are fine).

## Targets and where the pieces live

`flow.run(profile, target="staging")` shells out to `flowbench target staging` to resolve the named target config — the Go CLI stays the single source of truth for target-file parsing, so the two paths can never disagree about a base URL or an allow-list. Set `$FLOWBENCH_BIN` if the binary is not on your `PATH`, or pass `base_url="http://localhost:8080"` to skip resolution entirely (which also skips the host allow-list, and says so on stderr).

Going the other direction, `flowbench run flow.py` has to find the Python SDK to compile with. It assumes a dev checkout: it searches upward from the working directory and the flow file for a sibling `sdk-python/` directory, and uses its uv-managed `.venv` interpreter. `$FLOWBENCH_SDK_PATH` is the escape hatch when the SDK lives elsewhere, and `$FLOWBENCH_PYTHON` overrides the interpreter. The SDK requires Python >= 3.10.

## One flow, one IR

The contract behind the compile path is that a flow written twice compiles once: the Go conformance suite compiles each Python fixture in `tests/flows/` and diffs the resulting IR against the YAML parser's output for the `.flow.yaml` of the same name, byte for byte. If the two surfaces ever drift, the build breaks before you find out at run time. The [YAML reference](../reference/yaml-dsl.md) and the [Python API reference](../reference/python-api.md) describe the same underlying flow model for that reason.
