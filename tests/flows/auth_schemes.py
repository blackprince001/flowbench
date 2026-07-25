# Every auth scheme on the Python surface (issue #30). Compiles to IR
# equivalent to auth_schemes.flow.yaml -- internal/conformance checks this.
#
# `env("NAME")` is the Python spelling of YAML's `{{ env.NAME }}`: a reference
# resolved at run time, not a credential, so this file is safe to commit
# (ADR 0005).
from flowbench import (
  ApiKey,
  Basic,
  Bearer,
  Cookie,
  Flow,
  Hmac,
  NoAuth,
  OAuth2ClientCredentials,
  Profile,
  env,
  expect,
)

# The flow-level default: every step inherits it unless it says otherwise.
flow = Flow("auth_schemes", auth=Bearer(token=env("FB_API_TOKEN")))


@flow.step
def inherits_flow_default(ctx):
  r = ctx.http.get("/orders")
  expect(r.status).to_be(200)


@flow.step(auth=NoAuth())
def opted_out(ctx):
  r = ctx.http.get("/health")
  expect(r.status).to_be(200)


@flow.step(auth=Basic(username=env("FB_USER"), password=env("FB_PASSWORD")))
def basic_auth(ctx):
  r = ctx.http.get("/reports")
  expect(r.status).to_be(200)


@flow.step(auth=ApiKey(name="X-Api-Key", value=env("FB_API_KEY")))
def api_key_header(ctx):
  r = ctx.http.get("/search")
  expect(r.status).to_be(200)


@flow.step(auth=ApiKey(name="api_key", value=env("FB_API_KEY"), location="query"))
def api_key_query(ctx):
  r = ctx.http.get("/legacy/search")
  expect(r.status).to_be(200)


@flow.step(auth=Cookie(name="session", value=env("FB_SESSION")))
def session_cookie(ctx):
  r = ctx.http.get("/account")
  expect(r.status).to_be(200)


@flow.step(
  auth=OAuth2ClientCredentials(
    token_url="https://issuer.example/oauth/token",
    client_id=env("FB_CLIENT_ID"),
    client_secret=env("FB_CLIENT_SECRET"),
    scopes=["payments:write"],
  )
)
def client_credentials(ctx):
  r = ctx.http.post("/payments", json={"amount": 250})
  expect(r.status).to_be(202)


@flow.step(
  auth=Hmac(
    secret=env("FB_SIGNING_SECRET"),
    key_id=env("FB_SIGNING_KEY_ID"),
    timestamp_header="X-Timestamp",
  )
)
def signed_webhook(ctx):
  r = ctx.http.post("/webhooks/replay", json={"event_id": "evt_123"})
  expect(r.status).to_be(202)


if __name__ == "__main__":
  flow.run(Profile(mode="integration"))
