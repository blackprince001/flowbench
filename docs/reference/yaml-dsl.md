# YAML DSL reference

The complete schema of a flow file and a target file. A flow file is exactly one YAML document; every error names its file, line, and column, and unknown keys fail loudly. The YAML surface and the Python SDK compile to the same canonical IR, held byte-identical by the conformance suite (ADR 0002).

## Top level

| Key | Type | Meaning |
| --- | --- | --- |
| `flow` | string | **required** — the flow name; `[A-Za-z_][A-Za-z0-9_-]*` |
| `steps` | list | ordered steps |
| `profile` | mapping | the execution contract; defaults to `mode: integration` when absent |
| `auth` | mapping | flow-level default auth, applied to every request-making step without its own |
| `data` | string | fixture path shorthand — binds a pool whose rows arrive as `{{ user.* }}` |

## Steps

Every step needs `id` (unique in the flow; dots and `@` are reserved for span names) and **exactly one** action key:

| Action | Form |
| --- | --- |
| `call` | `"METHOD /path"` — uppercase method, space, URL |
| `graphql` | mapping — see below |
| `ws` | mapping — see below |
| `grpc` | mapping — see below |
| `wait` | duration string (`500ms`, `30s`, `10m`) |
| `poll` | mapping — see below |

Common step keys:

| Key | Applies to | Meaning |
| --- | --- | --- |
| `headers` | call, graphql, ws (opening step), grpc (as metadata) | mapping of scalars |
| `query` | call, poll | URL query parameters |
| `body` | call, poll | any YAML, sent as JSON |
| `extract` | response-bearing steps | `var: $.json.path` — paths must start with `$` |
| `assert` | response-bearing steps | list of assertion expressions |
| `retry` | call, graphql, grpc | retry policy — not `ws` (a retry re-sends a request and reads a status) |
| `throttle` | call-shaped steps | author-mapped throttle statuses |
| `auth` | request-making steps | per-step auth, overriding the flow default |
| `on_failure` | all | `abort_flow` (default) \| `abort_run` \| `record` |

### `graphql:`

| Key | Meaning |
| --- | --- |
| `url` / `endpoint` | one of the two, not both |
| `query` | the operation document; block scalars supported; **never templated** — values travel as variables |
| `variables` | a JSON mapping; templating applies here |
| `operation_name` | optional |
| `on_errors` | `fail` (default) \| `allow_partial` \| `ignore` — a `200` with a non-empty `errors` array fails by default |

### `ws:`

| Key | Meaning |
| --- | --- |
| `url` / `endpoint` | presence means this step **opens** a session |
| `session` | session name; empty means "the flow's ws session" |
| `subprotocols` | list; opening step only |
| `send` | a JSON frame |
| `receive` | `{ match, timeout }`, or bare `receive:` for the next frame |

`match` is a **filter** — non-matching frames are skipped, not failed; the step's `assert` judges the matched frame. The validator refuses: joining a session nothing opens, reopening an open session, headers/subprotocols on a non-opening step, `extract`/`assert` on a step that receives nothing, and `status`/`header.*` assertions against a frame. Close code `1013` counts as throttled.

### `grpc:`

| Key | Meaning |
| --- | --- |
| `proto` | **required** — path to the `.proto`, relative to the flow file |
| `import_paths` | list of proto import roots |
| `method` | **required** — `package.Service/Method` (leading `/` optional) |
| `url` / `endpoint` | address only — `grpc://host:port` or `grpcs://`; a path is an error, the method is the path |
| `message` | a JSON mapping |

`headers` become gRPC metadata, so every auth scheme applies unchanged. `RESOURCE_EXHAUSTED` classifies as throttled. Streaming methods are a pre-run error (ADR 0019).

### `poll:`

| Key | Meaning |
| --- | --- |
| `call` | the `"METHOD /path"` shorthand |
| `headers` / `query` / `body` | as for `call` |
| `until` | list of assertions, at least one |
| `interval` | must be > 0 |
| `timeout` / `max_attempts` | at least one bound required |

### `retry:`

| Key | Meaning |
| --- | --- |
| `on_status` | list of status codes (100–599), at least one |
| `backoff` | `fixed` \| `exponential` \| `honor_retry_after` |
| `max_attempts` | ≥ 1 — runs stay bounded |
| `base_delay` | duration ≥ 0 |

Each attempt emits its own span, with `backoff` spans between.

### `throttle:`

| Key | Meaning |
| --- | --- |
| `statuses` | codes treated as throttled *in addition to* the always-on HTTP 429 |
| `as_error` | override the mode default (integration/system count throttles as failures; load/stress/soak count them as data) |

### `auth:`

`scheme` is required; fields from another scheme are an error, not a silent no-op:

| Scheme | Required | Optional |
| --- | --- | --- |
| `none` | — | — (explicit opt-out of the flow default) |
| `bearer` | `token` | |
| `basic` | `username`, `password` | |
| `api_key` | `name`, `value` | `in: header` (default) \| `query` |
| `cookie` | `name`, `value` | |
| `oauth2_client_credentials` | `token_url`, `client_id`, `client_secret` | `scopes` |
| `hmac` | `secret` | `algorithm: sha256`\|`sha512`, `encoding: hex`\|`base64`, `header`, `key_id`, `key_id_header`, `timestamp_header`, `sign` |

HMAC defaults: `sha256`, `hex`, header `X-Signature`, key-id header `X-Key-Id`, signing template `{method}\n{path}\n{timestamp}\n{body_sha256}`. `sign` placeholders use single braces — `{method} {path} {query} {body} {body_sha256} {timestamp} {key_id}` — and are never `{{ }}`-templated. The OAuth2 token is fetched once per run, shared across VUs, refreshed 30s before expiry, and the token URL is gated by the allow-list. See [authentication](../guide/auth.md).

## Assertions

`<subject> <op> <value>`:

- **Subjects** — `status`, `latency`, `$.json.path` (body), `header.<Name>`, or a variable an earlier step extracted.
- **Operators** — `==` `!=` `<` `<=` `>` `>=` `contains` `matches`.
- **Values** — JSON-typed: bare numbers and `true`/`false` stay typed; quotes are stripped from strings; `!= null` means *exists*, `== null` means *does not exist* (other operators with `null` are an error).

Asserting on a variable nothing extracts is a pre-run error.

## Templating

`{{ name }}` or `{{ name.path }}`; the root must match `[A-Za-z_][A-Za-z0-9_-]*`. Available roots: `env` (process environment, always registered for redaction), the data pool root (`user`), and variables extracted by **earlier** steps. Templated fields: URL, headers, query, body, GraphQL url/headers/variables (not the document), WS url/headers/send, gRPC url/headers/message (not proto/method), and all auth fields except `sign`. An unresolvable reference is a pre-run error quoting the template.

## `profile:`

| Key | Form |
| --- | --- |
| `mode` | `integration` \| `system` \| `load` \| `stress` \| `soak` |
| `vus` | integer, or `{ ramp: "0 -> 500 over 5m", hold: 10m }` |
| `hold` | duration (an error if also set inside `vus`) |
| `iterations` | integer |
| `arrival_cap` | `N/s`, `N/m`, `N/h`, or `N/<duration>` (e.g. `50/2s`) |
| `thresholds` | list of threshold expressions |

Threshold grammar: LHS is `error_rate`, `throttle_rate`, or `pNN(latency)` / `avg(latency)` / `max(latency)` / `min(latency)` (p1–p100); operators `<` `<=` `>` `>=`; RHS is a duration for latency, a number or percent for rates (`1%` ≡ `0.01`). Semantics per mode are in [profiles and thresholds](../guide/profiles-and-thresholds.md).

## Target files

Strictly decoded — an unknown key is an error:

| Key | Meaning |
| --- | --- |
| `name` | identifier |
| `base_urls` | ≥ 1 absolute URL; **this is the host allow-list** |
| `max_vus` / `max_rps` | safety ceilings; 0 or absent = unset |
| `request_timeout` | bounds one call; 0 = adapter default (30s) |
| `agent_addr` | a `flowbench-agent` to poll during runs |
| `disallowed_modes` | modes refused outright against this target |

Target files never carry credentials. See [targets and safety rails](../guide/targets-and-safety.md).

## Not yet reachable from YAML

The IR is richer than the YAML surface in a few places, deliberately: named data pools with `distribution` (`unique-per-vu` \| `round-robin` \| `random`) and `on_exhausted` (`fail` \| `cycle` \| `stop`) policies, per-step capture policy, and extraction from headers or status. The YAML `data:` shorthand takes the defaults (format inferred from the extension, `unique-per-vu`, `fail`). These surfaces exist in the IR and the engine honours them; YAML syntax for them is future work.
