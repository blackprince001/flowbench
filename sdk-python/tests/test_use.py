import re

import pytest

from flowbench import Flow, FlowCompileError, env, expect


def _login_flow():
  inputs = {"email": env("SHOP_USER"), "password": None}
  login = Flow("login", inputs=inputs, outputs=["token"])

  @login.step
  def login_step(ctx):
    r = ctx.http.post(
      "/auth/login",
      json={"email": ctx.inputs["email"], "password": ctx.inputs["password"]},
    )
    expect(r.status).to_be(200)
    ctx.vars["token"] = r.json_path("$.data.access_token")

  return login


def test_use_step_compiles_to_use_ir():
  checkout = Flow("checkout", data="fixtures/users.csv")

  @checkout.step
  def auth(ctx):
    ctx.use(_login_flow(), with_={"password": ctx.user["password"]})

  @checkout.step
  def create_order(ctx):
    ctx.http.post(
      "/orders", headers={"Authorization": f"Bearer {ctx.vars['auth.token']}"}
    )

  ir = checkout.compile()
  auth_step = ir["flows"][0]["steps"][0]
  assert auth_step["type"] == "use"
  assert auth_step["use"]["path"] == ""
  assert auth_step["use"]["with"] == {"password": "{{ user.password }}"}
  used = auth_step["use"]["flow"]
  assert used["name"] == "login"
  assert used["outputs"] == ["token"]
  assert used["inputs"] == [
    {"name": "email", "default": "{{ env.SHOP_USER }}"},
    {"name": "password"},
  ]
  create_order_step = ir["flows"][0]["steps"][1]
  auth_header = create_order_step["call"]["headers"]["Authorization"]
  assert auth_header == "Bearer {{ auth.token }}"


def test_use_output_unavailable_before_its_use_step():
  checkout = Flow("checkout")

  @checkout.step
  def create_order(ctx):
    ctx.http.get(f"/x?t={ctx.vars['auth.token']}")

  @checkout.step
  def auth(ctx):
    ctx.use(_login_flow(), with_={"password": "pw"})

  with pytest.raises(FlowCompileError, match="read before it was extracted"):
    checkout.compile()


def test_missing_required_with_raises():
  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(_login_flow())  # no password

  with pytest.raises(FlowCompileError, match=re.escape("requires input 'password'")):
    checkout.compile()


def test_undeclared_with_key_raises():
  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(_login_flow(), with_={"password": "pw", "nickname": "ada"})

  with pytest.raises(FlowCompileError, match="does not declare as an input"):
    checkout.compile()


def test_use_step_return_value_refuses_response_operations():
  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    r = ctx.use(_login_flow(), with_={"password": "pw"})
    expect(r.status).to_be(200)

  with pytest.raises(FlowCompileError, match="does not apply to a use step"):
    checkout.compile()


def test_ctx_inputs_outside_declared_inputs_raises():
  f = Flow("f")

  @f.step
  def a(ctx):
    ctx.http.get(f"/x?e={ctx.inputs['email']}")

  with pytest.raises(FlowCompileError, match=re.escape("ctx.inputs is only available")):
    f.compile()


def test_ctx_inputs_undeclared_name_raises():
  f = Flow("f", inputs={"email": None})

  @f.step
  def a(ctx):
    ctx.http.get(f"/x?e={ctx.inputs['mail']}")

  with pytest.raises(FlowCompileError, match="is not declared by this flow's inputs"):
    f.compile()


def test_same_python_flow_used_twice_produces_equal_ir():
  checkout = Flow("checkout")
  login = _login_flow()

  @checkout.step
  def first(ctx):
    ctx.use(login, with_={"password": "pw"})

  @checkout.step
  def second(ctx):
    ctx.use(login, with_={"password": "pw"})

  ir = checkout.compile()
  a, b = ir["flows"][0]["steps"]
  assert a["use"]["flow"] == b["use"]["flow"]


def test_python_flow_use_cycle_raises():
  a = Flow("a")
  b = Flow("b")

  @a.step
  def step_a(ctx):
    ctx.use(b)

  @b.step
  def step_b(ctx):
    ctx.use(a)

  with pytest.raises(FlowCompileError, match="uses itself"):
    a.compile()


def test_python_flow_use_self_cycle_raises():
  a = Flow("a")

  @a.step
  def step_a(ctx):
    ctx.use(a)

  with pytest.raises(FlowCompileError, match="uses itself"):
    a.compile()


def test_nested_python_flow_use_two_levels():
  leaf = Flow("leaf")

  @leaf.step
  def ping(ctx):
    ctx.http.get("/ping")

  mid = Flow("mid", outputs=[])

  @mid.step
  def relay(ctx):
    ctx.use(leaf)

  top = Flow("top")

  @top.step
  def auth(ctx):
    ctx.use(mid)

  ir = top.compile()
  auth_step = ir["flows"][0]["steps"][0]
  assert auth_step["use"]["flow"]["steps"][0]["use"]["flow"]["name"] == "leaf"
