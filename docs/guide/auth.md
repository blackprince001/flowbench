# Authentication

Every credential in a flow is a reference — a `{{ env.* }}` template resolved at request time — never a literal, so every flow and target file stays safe to commit (ADR 0005). This page covers the seven schemes, how a flow declares them, and what the engine does to keep the resolved values out of the run store.

## One default, per-step overrides

The `auth:` block at the top of a flow is the flow-level default: every step that makes a request inherits it unless it declares its own, and `auth: { scheme: none }` opts a step out. Both authoring surfaces flatten this onto the steps at compile time, so the executor sees auth per step and needs no flow context to resolve it.

```yaml
flow: auth_schemes
auth:
  scheme: bearer
  token: "{{ env.DEMO_API_TOKEN }}"
steps:
  # Inherits the flow default.
  - id: orders
    call: GET /orders
    assert: [ status == 200 ]

  # Opts out. The stub's /health refuses a credential, so an inherited
  # default leaking onto this step fails the run rather than passing unnoticed.
  - id: health
    call: GET /health
    auth: { scheme: none }
    assert: [ status == 200 ]

  # Overrides the default with its own scheme.
  - id: reports
    call: GET /reports
    auth:
      scheme: basic
      username: "{{ env.DEMO_USER }}"
      password: "{{ env.DEMO_PASSWORD }}"
    assert: [ status == 200 ]
```

(from `examples/auth-local/schemes.flow.yaml`)

Auth applies to steps that make a request: `call`, `graphql`, `grpc`, `poll`, and the `ws` step that opens a session — credentials go on the handshake, and a step working on a session someone else opened has no request left to decorate. Declaring auth on a `wait` step is a validation error.

## The seven schemes

| Scheme | Required | Optional | Rides as |
| --- | --- | --- | --- |
| `none` | — | — | nothing; the explicit opt-out |
| `bearer` | `token` | — | `Authorization: Bearer <token>` |
| `basic` | `username`, `password` | — | `Authorization: Basic <base64>` |
| `api_key` | `name`, `value` | `in` (`header` default, or `query`) | a header or query parameter called `name` |
| `cookie` | `name`, `value` | — | a `name=value` cookie |
| `oauth2_client_credentials` | `token_url`, `client_id`, `client_secret` | `scopes` | `Authorization: Bearer <fetched token>` |
| `hmac` | `secret` | `algorithm`, `encoding`, `header`, `key_id`, `key_id_header`, `timestamp_header`, `sign` | a per-request signature header |

A field outside its scheme's vocabulary is a parse error rather than a silent no-op — a `password` under `scheme: bearer` is caught at parse time instead of authenticating as nothing at request time. A `bearer` token can be static, env-sourced, or a variable an earlier step extracted, which is how a login-then-act flow carries its token forward.

Because gRPC metadata is HTTP/2 headers on the wire, every scheme that writes a header reaches a `grpc` step unchanged — see [protocols](protocols.md).

## HMAC request signing

The defaults cover what most request-signing schemes agree on: `algorithm: sha256` (or `sha512`), `encoding: hex` (or `base64`), the signature in `X-Signature`, and the key id — when `key_id` is set — in `X-Key-Id`. Setting `timestamp_header` stamps the signing timestamp onto the request as well.

The canonical string defaults to `"{method}\n{path}\n{timestamp}\n{body_sha256}"`, and `sign` overrides it for services that sign something else, over the placeholders `{method}`, `{path}`, `{query}`, `{body}`, `{body_sha256}`, `{timestamp}`, and `{key_id}`. These are single braces on purpose: the vocabulary belongs to the signer, not the templater, and a `sign` string is never `{{ }}`-resolved.

```yaml
- id: signed_webhook
  call: POST /webhooks/replay
  body: { event_id: "evt_123", attempt: 2 }
  auth:
    scheme: hmac
    secret: "{{ env.DEMO_SIGNING_SECRET }}"
    key_id: "{{ env.DEMO_SIGNING_KEY_ID }}"
    timestamp_header: X-Timestamp
  assert: [ status == 202 ]
```

(from `examples/auth-local/schemes.flow.yaml`)

The signature is stamped per attempt, not per step: a service that rejects signatures older than 30 seconds would otherwise reject a request signed once and replayed through [retry backoff](retries-and-throttling.md).

## OAuth2 client credentials

The token endpoint is fetched once per run and the token shared by every VU, refreshed 30 seconds before expiry so a request in flight never carries one that dies mid-connection. Without that, a 10k-VU run would open by rate-limiting itself on its own auth server — the `auth-local` example's stub logs exactly one `issued access token` line across a 20k-iteration run.

Fetching a token is a real outbound request carrying the client credentials, so `token_url` is gated by the target's host allow-list like any call: pre-run when the URL is a literal, at request time when it is templated. Point it at a host missing from the [target config](targets-and-safety.md) and the run refuses it. (OAuth2 authorization-code is deliberately absent — it needs browser interaction and is out of v1 scope.)

## Credentials come from the environment

There is no FlowBench secrets mechanism to configure, and that is the design (ADR 0005): the environment supplies, the engine redacts. Any value resolved from `{{ env.* }}` is registered as a secret at resolution time and scrubbed to `[redacted]` from captured bodies, traces, logs, and served views before anything reaches the run store — including the values the engine *derived* rather than resolved, like the base64 Basic blob and the fetched OAuth2 access token.

The one deliberate exception is the HMAC signature itself: it is per-request and not reversible to the secret, and registering one per request would grow the redaction set without bound at 10k VUs.
