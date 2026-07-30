# Login-chained flows

Most real flows start the same way: authenticate, take the token the response hands back, and spend it on every step after. FlowBench's whole authoring model is built around that shape — extraction pulls a value out of one step's response into a flow variable, templates inject it into later steps, and assertions can judge the variable itself. This recipe walks the canonical version, then the local variations you can run today.

## The canonical pair

From [`tests/flows/authenticated_checkout.flow.yaml`](../../tests/flows/authenticated_checkout.flow.yaml) — the PRD §11 sample flow, verbatim:

```yaml
flow: authenticated_checkout
data: fixtures/users.csv        # unique-per-vu by default
steps:
  - id: login
    call: POST /auth/login
    body: { email: "{{ user.email }}", password: "{{ user.password }}" }
    extract: { token: $.data.access_token }
    assert: [ status == 200, token != null ]

  - id: create_order
    call: POST /orders
    headers: { Authorization: "Bearer {{ token }}" }
    body: { items: "{{ user.cart }}" }
    extract: { order_id: $.data.id }
    retry:
      on_status: [429, 503]
      backoff: honor_retry_after
      max_attempts: 5

  - id: pay
    call: POST /orders/{{ order_id }}/pay
    headers: { Authorization: "Bearer {{ token }}" }
    assert: [ status == 202 ]
```

Three chaining mechanics are all here. `extract: { token: $.data.access_token }` captures a value by JSONPath into a flow variable; `{{ token }}` injects it into a later header and `{{ order_id }}` into a later path; and `token != null` asserts on the extracted variable itself, so a login that answers `200` with no token fails at the login step instead of three steps later with a mystery `401`. Variables are per-VU — each virtual user logs in as its own fixture row and carries its own token.

The same flow on the Python surface compiles to the same IR ([`tests/flows/authenticated_checkout.py`](../../tests/flows/authenticated_checkout.py)):

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
    expect(ctx.vars["token"]).not_to_be(None)

@flow.step(retry=Retry(on_status=[429, 503], backoff="honor_retry_after", max_attempts=5))
def create_order(ctx):
    r = ctx.http.post(
        "/orders",
        headers={"Authorization": f"Bearer {ctx.vars['token']}"},
        json={"items": ctx.user["cart"]},
    )
    ctx.vars["order_id"] = r.json_path("$.data.id")
```

## A version you can run now

[`examples/bored-api/smoke.flow.yaml`](../../examples/bored-api/smoke.flow.yaml) is the same pattern minus the auth, against a public target — filter, take the first result's key, look it up:

```yaml
  - id: filter_education
    call: GET /filter
    query:
      type: education
      participants: "1"
    extract:
      key: $[0].key
    assert:
      - status == 200
      - $[0].type == "education"

  - id: fetch_by_key
    call: GET /activity/{{ key }}
    assert:
      - status == 200
      - $.type == "education"
```

```bash
flowbench run examples/bored-api/smoke.flow.yaml --target examples/bored-api/target.yaml
```

## Flow-level auth or an explicit login step

You do not always need a login step. When the credential fits one of the built-in schemes, declare it once at the top of the flow and let every step inherit it — from [`examples/auth-local/schemes.flow.yaml`](../../examples/auth-local/schemes.flow.yaml):

```yaml
flow: auth_schemes
auth:
  scheme: bearer
  token: "{{ env.DEMO_API_TOKEN }}"
```

Use the `auth:` block when the credential is static or issued by a standard mechanism (bearer, basic, API key, session cookie, OAuth2 client-credentials, HMAC — see [Authentication](../guide/auth.md)); the OAuth2 token, for instance, is fetched once and shared across every VU rather than re-granted per iteration. Write an explicit login step when the token comes from your application's own endpoint and logging in is itself part of what you are testing — the `authenticated_checkout` shape above.

## Secrets, with proof

Credentials come from the environment as `{{ env.* }}`, never from the flow file, and any value resolved that way is scrubbed before the run store is written. [`examples/load-local/login.flow.yaml`](../../examples/load-local/login.flow.yaml) sends an env-sourced secret to an endpoint that echoes it straight back — so it lands in the request and the response — and it still reaches nothing on disk:

```bash
DEMO_SECRET=hunter2-do-not-leak flowbench run examples/load-local/login.flow.yaml \
  --target examples/load-local/target.yaml

grep -rc 'hunter2-do-not-leak' runs/     # → 0, never stored
```

The captured payload in `traces.json` shows what was stored instead: `{"password":"[redacted]","username":"ada"}`, on both sides of the exchange.

## Why it works

Extraction and assertion each emit their own span, so when a chain breaks the run says where: a missing `$.data.access_token` is an extraction failure at `login`, not a `401` at `pay`. Open the run (`flowbench serve --store runs`), pick an iteration in the waterfall, and the chain reads top to bottom in causal order — extraction spans under the step that captured, the dependent call after it. The same flow file runs under any [profile](../guide/profiles-and-thresholds.md): once with assertions in integration mode, or ten thousand times under the stress ramp in the profile above.
