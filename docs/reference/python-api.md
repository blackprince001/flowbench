# Python API reference

The public surface of the `flowbench` Python package: everything exported from `flowbench.__init__`, the `ctx` object step functions receive, and the response and observation objects they work with. For the narrative version, see the [Python SDK guide](../guide/python-sdk.md); for the equivalent YAML surface, the [YAML reference](../reference/yaml-dsl.md).

The package requires Python >= 3.10 and depends on `httpx>=0.27`. It is not on PyPI; install it from the repo's `sdk-python` directory:

```bash
uv sync --project sdk-python        # dev checkout: creates .venv with the package + dev deps
pip install ./sdk-python            # or into an environment of your own
```

The exported names are: `Flow`, `Profile`, `Retry`, `expect`, `frame`, `secret`, `env`, `FlowCompileError`, `FlowExecutionError`, `Bearer`, `Basic`, `ApiKey`, `Cookie`, `OAuth2ClientCredentials`, `Hmac`, `NoAuth`.

## Flow

```python
Flow(name, data=None, auth=None)
```

The unit of authorship: an ordered sequence of steps. `name` names the flow (it becomes the root span's identity). `data` binds a data pool — a CSV path relative to the flow file, one iteration per row, rows exposed as `ctx.user[...]`. `auth` sets a default auth scheme every step inherits unless it declares its own.

### @flow.step

```python
@flow.step
@flow.step(retry=None, auth=None)
```

Registers a step function, in declaration order. The function's name is the step's id — a structural span identity, so renaming it splits cross-run flame data. Each step function must make exactly one call (`ctx.http`, `ctx.graphql`, `ctx.ws`, or `ctx.grpc`); on the Python-driven path, a call your own client makes or a recorded prompt observation satisfies the rule too. `retry` takes a [`Retry`](#retry); `auth` takes an auth scheme, overriding the flow default (`NoAuth()` opts the step out).

### flow.compile

```python
flow.compile(profile=None)
```

Traces every step function once with symbolic values and returns the canonical IR as a dict. `profile` defaults to `Profile(mode="integration")`. You rarely call this yourself — `flowbench run flow.py` drives it via the `FLOWBENCH_COMPILE_ONLY` environment variable — but it is the fast way to see what a flow compiles to. Raises `FlowCompileError` for a flow the IR cannot express, including any flow that opens a prompt observation (observation is Python-driven only).

### flow.run

```python
flow.run(profile, *, target="local", targets_dir="targets", base_url=None, store="runs")
```

Executes the flow in-process — the Python-driven path (ADR 0012). Only `integration` and `system` modes execute; any other mode raises `FlowExecutionError` pointing at the Go engine. `target` names a target config, resolved by shelling out to `flowbench target <name> --targets-dir <targets_dir>`; `base_url` skips resolution (and the host allow-list — a warning says so) and uses the given base URL directly. `store` is the run-store directory the finished run is written to, the same store `flowbench serve` reads. Under `FLOWBENCH_COMPILE_ONLY`, prints the compiled IR as JSON instead of executing.

## Profile

```python
Profile(mode, vus=None, ramp=None, hold=None, iterations=None,
        arrival_cap=None, thresholds=None)
```

The execution contract that turns one flow into any of the test categories. `mode` is one of `integration`, `system`, `load`, `stress`, `soak`. `vus` is a VU count, or the PRD's ramp spelling `"ramp(0 -> 500, 5m)"`, which is translated onto `ramp`; `ramp` natively takes `"0 -> 500 over 5m"`. `hold` is a Go-style duration (`"10m"`). `iterations` bounds the run by count instead. `arrival_cap` is a self-imposed request-rate ceiling like `"300/s"`. `thresholds` is a list of threshold expressions, e.g. `"p95(latency) < 800ms"`. Invalid values raise `FlowCompileError` at construction.

## Retry

```python
Retry(on_status, backoff, max_attempts, base_delay=None)
```

A per-step retry policy, passed to `@flow.step(retry=...)`. `on_status` is a list of HTTP status codes in `[100, 599]` that trigger a retry. `backoff` is one of `"fixed"`, `"exponential"`, `"honor_retry_after"`. `max_attempts` must be >= 1. `base_delay` is a Go-style duration for the fixed and exponential strategies. Attempts and backoff waits appear as child spans of the step.

## Auth schemes

Declared on a `Flow` or a single step; each compiles to the same IR the YAML `auth:` block does. Credentials are never literals — pass `env("API_TOKEN")` for a value the process environment holds, or a value an earlier step extracted. The engine resolves the value at request time and registers it for redaction. Live execution does not yet apply auth schemes; a Python-driven run with one raises and points at `flowbench run`.

```python
Bearer(token)
```
`Authorization: Bearer <token>`, static or extracted at runtime.

```python
Basic(username, password)
```
HTTP basic auth. The engine base64-encodes and redacts the pair.

```python
ApiKey(name, value, location="header")
```
An API key in a header (the default) or, with `location="query"`, a query parameter.

```python
Cookie(name, value)
```
A session cookie, sent alongside whatever the VU's jar already holds.

```python
OAuth2ClientCredentials(token_url, client_id, client_secret, scopes=None)
```
The client-credentials grant. The token is fetched once per run and shared across VUs; `token_url` must sit inside the target's allow-list. Authorization-code is out of v1 scope (it needs a browser).

```python
Hmac(secret, algorithm=None, encoding=None, header=None, key_id=None,
     key_id_header=None, timestamp_header=None, sign=None)
```
HMAC request signing. `algorithm` is `"sha256"` or `"sha512"`; `encoding` is `"hex"` or `"base64"`. `sign` is the canonical string over the placeholders `{method}`, `{path}`, `{query}`, `{body}`, `{body_sha256}`, `{timestamp}`, and `{key_id}`; unset, it is `"{method}\n{path}\n{timestamp}\n{body_sha256}"`.

```python
NoAuth()
```
Opts one step out of the flow's auth default.

## The ctx object

Every step function receives a `ctx`. The same surface backs both paths — compilation traces it into IR, live execution runs it for real.

### ctx.http

```python
ctx.http.get(url, *, json=None, headers=None, query=None)
ctx.http.post(url, *, json=None, headers=None, query=None)
ctx.http.put(url, *, json=None, headers=None, query=None)
ctx.http.patch(url, *, json=None, headers=None, query=None)
ctx.http.delete(url, *, json=None, headers=None, query=None)
```

One HTTP call — at most one per step. `url` is resolved against the target's base URL when relative; an absolute URL must match the target's allow-list. `json` is the request body, `headers` and `query` are string mappings; all three accept extracted values and data-pool fields. Returns a [response](#responses).

A call you did not label carries `Content-Type: application/json` when it has a body, and `User-Agent: flowbench/<version> (python)` always. Passing either in `headers` overrides it; passing it as `""` sends no such header at all. Both paths follow the same rules as [the engine](yaml-dsl.md#headers-the-engine-adds), so a flow sends the same bytes whether it runs through `flowbench run` or `python flow.py`.

### ctx.graphql

```python
ctx.graphql(url, *, query, variables=None, operation_name=None,
            headers=None, on_errors=None)
```

One GraphQL operation, compiled to a `graphql` step rather than a hand-rolled POST, so the engine reads the `data`/`errors` shape and fails the step on an operation error that arrives inside a `200 OK`. `on_errors` is `"fail"` (the default when unset), `"allow_partial"`, or `"ignore"`.

### ctx.ws

```python
ctx.ws(url=None, *, session=None, send=None, receive=None,
       timeout=None, headers=None, subprotocols=None)
```

One step's worth of work on a WebSocket session. A `url` opens a session; without one the step joins the session an earlier step opened, and `session=` names it in either case. The session closes when the iteration ends, not when the step returns, which is what lets an exchange span several steps. `send` sends a frame. `receive=True` takes the next frame; `receive=frame(...)` (or a list of conditions) says which frame the step is waiting for — frames that arrive meanwhile are skipped, not failed on. `timeout` bounds a receive. `headers` and `subprotocols` ride on the handshake, so they belong to the call that opens the session.

### ctx.grpc

```python
ctx.grpc(method, *, proto, message=None, url=None, headers=None,
         import_paths=None)
```

One unary gRPC call. `method` is fully qualified, `"package.Service/Method"`. `proto` names the schema file relative to the flow file; `import_paths` adds proto import roots. `message` is the request message as a mapping of field to value. `url` is optional and only an address — the method is the path. `headers` are gRPC metadata (HTTP/2 headers on the wire), so every auth scheme reaches a gRPC call unchanged.

### ctx.prompt

```python
ctx.prompt(name, *, template=None, variant=None, timeout=None,
           pace=None, burst=None)
```

A context manager wrapping one LLM call the flow's own code makes — see the [prompt observation guide](../guide/prompt-observation.md) for semantics. `name` and `variant` must match `[A-Za-z_][A-Za-z0-9_-]*` (dots and `@` are reserved for span names); `variant` gives the observation the span identity `name@variant`. `template` is hashed as the prompt's identity. `timeout` is seconds or a duration string (`"30s"`), measured rather than enforced. `pace` is a client-side ceiling like `"20/m"` (a count over `s`, `m`, or `h`, e.g. `"100/15m"`); `burst` is a positive integer allowance and requires a `pace`. Python-driven only: compiling a flow that observes a prompt raises `FlowCompileError`.

The object yielded by the `with` block:

```python
p.record(prompt, completion, usage=None)
```

Hands the exchange over — exactly once per observation. `prompt` and `completion` may be strings or structures (a chat message list, a provider response object); non-strings are rendered as JSON so the structural diff has something to parse. `usage` is the provider's usage object or mapping, normalized to `prompt_tokens`/`completion_tokens`/`total_tokens` whether the provider says prompt/completion or input/output. After recording, `p.prompt` and `p.completion` are the rendered strings — real `str` values your code can parse and chain, and valid `expect(...)` subjects — and `p.prompt_hash` and `p.usage` carry the identity hash and normalized counts.

### ctx.vars, ctx.user, ctx.env, ctx.secret

```python
ctx.vars["token"] = r.json_path("$.data.access_token")   # extraction
ctx.vars["token"]                                        # later read
ctx.user["email"]                                        # data-pool field
ctx.env["API_HOST"]                                      # environment variable
ctx.secret(value)                                        # flag a computed value
```

`ctx.vars` holds flow variables: assignment must be a `json_path(...)` extraction, and a read before an earlier step extracted the name raises. `ctx.user` reads the current data-pool row and is only available when `Flow(..., data=...)` binds a pool. `ctx.env` reads a process environment variable and registers the value for redaction. `ctx.secret(value)` flags a value the step computed mid-flow — a minted token, a signed URL — as sensitive and returns it, so it can wrap an expression in place; credentials that exist before the run starts use module-level [`secret()`](#secret-and-env) instead.

## Responses

The object a `ctx.http`/`ctx.graphql`/`ctx.ws`/`ctx.grpc` call returns exposes three readers; each yields a subject for `expect(...)` or, for `json_path`, a value assignable into `ctx.vars`:

```python
r.status            # the response status code
r.header(name)      # one response header's value
r.json_path(path)   # a JSONPath read of the body, e.g. "$.data.id"
```

On the compile path these trace into IR assertions and extractions; on the Python-driven path they are real values read from the live response (`LiveResponse`). For a WebSocket step the body is the only surface — a frame has no status line and no headers.

## expect

```python
expect(subject)
```

Builds an assertion on a response field (`r.status`, `r.header(...)`, `r.json_path(...)`), an extracted `ctx.vars` value, or a recorded `p.prompt`/`p.completion`. Anything else raises — in particular `ctx.user` and `ctx.env` values, which are inputs, not results. Assertion failure aborts the flow iteration (the integration/system default), and each check appears as its own span.

The methods, identical on every subject:

| Method | Passes when |
| --- | --- |
| `to_be(value)` | subject equals `value`; `to_be(None)` asserts absence |
| `not_to_be(value)` | subject differs from `value`; `not_to_be(None)` asserts presence |
| `to_be_less_than(value)` | subject < `value` |
| `to_be_less_than_or_equal(value)` | subject <= `value` |
| `to_be_greater_than(value)` | subject > `value` |
| `to_be_greater_than_or_equal(value)` | subject >= `value` |
| `to_contain(value)` | subject contains `value` |
| `to_match(pattern)` | subject matches the regex `pattern` |
| `to_exist()` | subject is present |
| `to_not_exist()` | subject is absent |

## frame

```python
frame(path)
```

A condition on a received WebSocket frame, for `ctx.ws(receive=...)`. Chain one assertion method onto it — `frame("$.type").to_be("order_update")` — to say which frame the step is waiting for. A match is a filter, not an assertion: frames that do not satisfy it are skipped rather than failed on. Use `expect(...)` on the returned frame to judge the one the step actually matched.

## secret and env

```python
flowbench.secret(value)
flowbench.env(name)
```

`secret(value)` flags a value as sensitive so it is scrubbed from every artifact of every run the process writes, and returns it, so it wraps an expression in place — `OpenAI(api_key=flowbench.secret(os.environ["OPENAI_API_KEY"]))`. It is module-level because a Python-driven flow's clients are usually built at import scope, before any run exists.

`env(name)` is a `{{ env.NAME }}` reference to a process environment variable, for credentials declared outside a step function — on a `Flow` or a step decorator, where there is no `ctx` yet. Inside a step, `ctx.env[name]` covers the same ground.

## Errors

```python
FlowCompileError      # the flow is written wrong: caught at compile/trace time
FlowExecutionError    # the run cannot proceed: bad mode, unresolved target,
                      # a step that made no call, an unsupported live feature
```

Both are loud by design: a contract violation raises rather than becoming data in the run.
