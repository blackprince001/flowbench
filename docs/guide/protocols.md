# Protocols

A step speaks one of four protocols: `call` (HTTP), `graphql`, `ws`, and `grpc` (unary). All four compile to the same IR and run through the same machinery — `{{ }}` templating, JSONPath extraction, assertions, spans, throttle classification — so each protocol block carries only what is genuinely different about its wire. The step machinery they share is covered in [flows](flows.md); the auth they all accept in [authentication](auth.md).

## HTTP calls

`call` is a one-line shorthand: an uppercase method, a space, a URL. A relative path is filled in by the [target's](targets-and-safety.md) base URL at run time, which is what lets one flow run against many environments.

```yaml
- id: filter
  call: GET /filter
  query:
    type: "{{ user.type }}"
    participants: "{{ user.participants }}"
  assert:
    - status == 200
    - header.Content-Type contains "application/json"
```

(from `examples/bored-api/sweep.flow.yaml`)

`headers`, `query`, and `body` sit at the step level, next to `call`. Values are ordinary YAML — a `body` mapping converts to JSON, and any string field can carry `{{ }}` templates. These three keys belong to call steps; on the other protocols they either change meaning (`headers`, below) or are refused at parse time, because a GraphQL operation carries its values in variables and a `ws` or `grpc` step's payload is the frame or message it sends.

## GraphQL

GraphQL puts the transport's verdict in the status and the operation's verdict in the body, so a `graphql` step is a POST with two extra rules: values travel as variables, and the `errors` array is judged.

```yaml
- id: place_order
  graphql:
    url: /graphql
    query: |
      mutation PlaceOrder($productId: ID!, $quantity: Int!) {
        placeOrder(productId: $productId, quantity: $quantity) { id status quantity }
      }
    variables: { productId: "{{ product_id }}", quantity: 2 }
  assert:
    - status == 200
    - $.data.placeOrder.status == "PENDING"
```

(from `examples/graphql-local/chain.flow.yaml`)

The query document is sent verbatim and is never templated — a `{{ }}` inside it is deliberately not supported. Values reach the operation through `variables`, where the server types and escapes them, so an extracted value full of quotes and braces cannot rewrite the query. `operation_name` selects which operation to run when the document holds several.

A `200 OK` with a non-empty `errors` array fails the step by default, because the alternative is a flow that forgets one assertion and reports a broken query as green forever. The error's `path` is kept, so the failure names the field. Two ways out when that default is wrong, both on the `graphql` block:

- `on_errors: allow_partial` — fails only when the operation resolved **no** data. This is the federated case: one subgraph times out, the rest answer, and extraction still runs over the half that resolved.
- `on_errors: ignore` — hands the judgement back to the flow's own assertions (`- $.errors not_exists`).

Everything else is HTTP. `headers` are ordinary HTTP headers on an ordinary POST, the per-phase spans (dns/connect/tls/ttfb/transfer) appear under the same `http_call` child a `call` step gets, a `429` classifies as `throttled`, `retry:` works, and auth is declared exactly as [authentication](auth.md) shows.

## WebSocket sessions

A `ws` step either opens a session (`url` set) or works on one an earlier step opened. The session outlives the step: it is iteration-scoped, and the engine closes it when the iteration ends — nothing in the flow says so. A later step names the session it wants with `session:` (empty is a name like any other: the flow's single unnamed session), and a flow that names one no step opens is refused before the run starts, the same way an undefined `{{ variable }}` is.

```yaml
- id: subscribe
  ws:
    send: { op: subscribe, symbol: FB-001 }
    receive:
      match: $.type == "ack"     # a filter, not an assertion
      timeout: 2s
  extract:
    subscription: $.id
  assert:
    - $.status == "ok"           # judges the frame that matched
```

(from `examples/ws-local/session.flow.yaml`)

Nothing correlates an arriving frame to the message that preceded it — a duplex connection carries heartbeats and other subscriptions' traffic — so a receive says which frame it is about. `match` and `assert` look alike and do opposite things: **`match` selects** — frames that fail it are skipped, because traffic the step never asked for is not a failure — and **`assert` judges** the frame `match` selected. A bare `receive:` takes the next frame, whatever it is. When no frame matches within `timeout`, the failure lists the frames it passed over, and the timed-out session is closed (a half-read message cannot be resumed). v0 speaks JSON text frames, which is what lets the same JSONPath evaluator work on a frame unchanged.

A frame has no status line and no headers, so asserting on those is refused at parse time rather than answered with a zero. The handshake, though, is plain HTTP — a `GET` carrying `Upgrade: websocket` — so it gets the same phase spans a `call` gets, auth rides on the step that opens the session, and the target's allow-list gates it, with `ws://host` counting as the same origin as `http://host`.

Throttling can arrive after the connection is open: a server shedding sessions closes them with RFC 6455's close code `1013` ("try again later"), which classifies as `throttled` exactly as an HTTP `429` on the handshake does. Every other close code fails and names itself — `1011 internal error` is not the same event as `1013 try again later`.

## gRPC (unary)

A `grpc` step names a `.proto` file and a fully-qualified method; the engine compiles the schema to descriptors at run time and invokes the method dynamically — no code generation, nothing checked in, and a schema change is picked up on the next run (ADR 0018). Compilation happens once per run, before the first request, so a missing file or an unknown method is a pre-run error rather than something 10k VUs each discover separately.

```yaml
- id: charge
  grpc:
    proto: proto/billing.proto
    method: billing.v1.Billing/Charge
    message:
      account: acct_1
      amountCents: "1200"   # int64 travels as a string in protobuf's JSON mapping
      currency: GHS
  headers:
    x-request-id: req-1
  extract:
    charge_id: $.chargeId
  assert:
    - status == 0           # 0 is OK; a gRPC status is its own numbering, not HTTP
    - $.status == "captured"
```

(from `examples/grpc-local/charge.flow.yaml`)

`method` is `package.Service/Method` — literally the HTTP/2 `:path` gRPC puts on the wire. Because the method is the path, `url` is address-only (`grpc://host:port`, or `grpcs://` for TLS) and a URL carrying a path is refused; a step against the target itself names no `url` at all, since the target's base URL is the whole address. `proto` resolves relative to the flow file, with `import_paths` as extra roots for the schema's own imports.

`message` is JSON converted into the method's input type, so a field the schema does not declare is an error rather than something the server quietly ignores — and the response is converted back to JSON, which is why extraction, assertions, capture, and redaction are the same code paths a `call` step uses. `headers` are gRPC metadata — named for what they are on the wire, HTTP/2 headers — so the step-level `headers:` block and every [auth scheme](auth.md) that writes one reach a gRPC call unchanged.

A `RESOURCE_EXHAUSTED` status classifies as `throttled`, the same signal a `429` and a close code `1013` carry (see [retries and throttling](retries-and-throttling.md)). Streaming is out of v1 by decision rather than omission (ADR 0019): a stream is neither one step nor one span, and the profile model cannot say what a stream held open for minutes measures — so a streaming method is a pre-run error that names the unary alternatives.
