import pytest

from flowbench import (
  ApiKey,
  Basic,
  Bearer,
  Cookie,
  Flow,
  FlowCompileError,
  Hmac,
  NoAuth,
  OAuth2ClientCredentials,
  Profile,
  env,
)


def compile_steps(flow):
  return flow.compile(Profile(mode="integration"))["flows"][0]["steps"]


def one_step_flow(**flow_kwargs):
  """A flow with a single trivial step, for exercising auth in isolation."""
  flow = Flow("f", **flow_kwargs)

  @flow.step
  def only(ctx):
    ctx.http.get("/x")

  return flow


def test_env_renders_as_a_template_reference():
  assert env("API_TOKEN") == "{{ env.API_TOKEN }}"


def test_bearer_to_ir():
  assert Bearer(token=env("T")).to_ir() == {
    "scheme": "bearer",
    "token": "{{ env.T }}",
  }


def test_basic_to_ir():
  assert Basic(username="u", password=env("P")).to_ir() == {
    "scheme": "basic",
    "username": "u",
    "password": "{{ env.P }}",
  }


def test_api_key_omits_the_default_location():
  # Matching the YAML surface, where `in:` is optional and defaults to header.
  assert ApiKey(name="X-Api-Key", value="v").to_ir() == {
    "scheme": "api_key",
    "name": "X-Api-Key",
    "value": "v",
  }
  assert ApiKey(name="k", value="v", location="query").to_ir()["in"] == "query"


def test_cookie_to_ir():
  assert Cookie(name="session", value=env("S")).to_ir() == {
    "scheme": "cookie",
    "name": "session",
    "value": "{{ env.S }}",
  }


def test_oauth2_to_ir():
  assert OAuth2ClientCredentials(
    token_url="https://issuer.example/token",
    client_id=env("ID"),
    client_secret=env("SECRET"),
    scopes=["a", "b"],
  ).to_ir() == {
    "scheme": "oauth2_client_credentials",
    "token_url": "https://issuer.example/token",
    "client_id": "{{ env.ID }}",
    "client_secret": "{{ env.SECRET }}",
    "scopes": ["a", "b"],
  }


def test_oauth2_omits_absent_scopes():
  ir = OAuth2ClientCredentials(
    token_url="https://i.example/t", client_id="i", client_secret="s"
  )
  assert "scopes" not in ir.to_ir()


def test_hmac_omits_unset_options():
  assert Hmac(secret=env("SIGNING_SECRET")).to_ir() == {
    "scheme": "hmac",
    "secret": "{{ env.SIGNING_SECRET }}",
  }


def test_hmac_carries_every_option():
  assert Hmac(
    secret="s",
    algorithm="sha512",
    encoding="base64",
    header="X-Sig",
    key_id="k",
    key_id_header="X-Client",
    timestamp_header="X-Ts",
    sign="{method}|{path}",
  ).to_ir() == {
    "scheme": "hmac",
    "secret": "s",
    "algorithm": "sha512",
    "encoding": "base64",
    "header": "X-Sig",
    "key_id": "k",
    "key_id_header": "X-Client",
    "timestamp_header": "X-Ts",
    "sign": "{method}|{path}",
  }


@pytest.mark.parametrize(
  ("scheme", "message"),
  [
    (lambda: Bearer(token=""), "Bearer.token is required"),
    (lambda: Basic(username="u", password=None), "Basic.password is required"),
    (lambda: ApiKey(name="k", value="v", location="body"), "ApiKey.location"),
    (lambda: Hmac(secret="s", algorithm="md5"), "Hmac.algorithm"),
    (lambda: Hmac(secret="s", encoding="rot13"), "Hmac.encoding"),
  ],
)
def test_invalid_auth_is_refused(scheme, message):
  with pytest.raises(FlowCompileError, match=message):
    scheme().to_ir()


def test_step_auth_lands_on_the_step():
  flow = Flow("f")

  @flow.step(auth=Bearer(token=env("T")))
  def only(ctx):
    ctx.http.get("/x")

  assert compile_steps(flow)[0]["auth"] == {"scheme": "bearer", "token": "{{ env.T }}"}


def test_flow_auth_is_flattened_onto_every_step():
  # The same flattening internal/parser does, so both surfaces hand the
  # executor per-step auth and the IR never needs flow context.
  flow = Flow("f", auth=Bearer(token=env("T")))

  @flow.step
  def inherits(ctx):
    ctx.http.get("/a")

  @flow.step(auth=NoAuth())
  def opted_out(ctx):
    ctx.http.get("/b")

  @flow.step(auth=ApiKey(name="X-Api-Key", value=env("K")))
  def overrides(ctx):
    ctx.http.get("/c")

  inherits_ir, opted_out_ir, overrides_ir = compile_steps(flow)

  assert inherits_ir["auth"]["scheme"] == "bearer"
  assert "auth" not in opted_out_ir
  assert overrides_ir["auth"]["scheme"] == "api_key"


def test_no_auth_without_a_flow_default_is_still_bare():
  flow = Flow("f")

  @flow.step(auth=NoAuth())
  def only(ctx):
    ctx.http.get("/x")

  assert "auth" not in compile_steps(flow)[0]


def test_flow_without_auth_emits_none():
  assert "auth" not in compile_steps(one_step_flow())[0]
