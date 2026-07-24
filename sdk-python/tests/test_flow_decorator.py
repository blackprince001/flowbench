import re

import pytest

from flowbench import Flow, FlowCompileError, Profile, Retry


def test_bare_decorator_registers_step_in_order():
  flow = Flow("f")

  @flow.step
  def a(ctx):
    ctx.http.get("/a")

  @flow.step
  def b(ctx):
    ctx.http.get("/b")

  ir = flow.compile()
  assert [s["id"] for s in ir["flows"][0]["steps"]] == ["a", "b"]


def test_decorator_with_retry_kwarg():
  flow = Flow("f")

  @flow.step(retry=Retry(on_status=[429], backoff="fixed", max_attempts=2))
  def a(ctx):
    ctx.http.get("/a")

  ir = flow.compile()
  assert ir["flows"][0]["steps"][0]["retry"] == {
    "on_status": [429],
    "backoff": "fixed",
    "max_attempts": 2,
  }


def test_step_with_no_http_call_raises():
  flow = Flow("f")

  @flow.step
  def a(ctx):
    pass

  with pytest.raises(FlowCompileError, match=re.escape("never made a ctx.http call")):
    flow.compile()


def test_step_id_is_function_name():
  flow = Flow("f")

  @flow.step
  def login(ctx):
    ctx.http.post("/login")

  ir = flow.compile()
  assert ir["flows"][0]["steps"][0]["id"] == "login"


def test_extraction_available_to_later_step_only():
  flow = Flow("f")

  @flow.step
  def login(ctx):
    r = ctx.http.post("/login")
    ctx.vars["token"] = r.json_path("$.token")

  @flow.step
  def use(ctx):
    ctx.http.get(f"/x?token={ctx.vars['token']}")

  ir = flow.compile()
  assert ir["flows"][0]["steps"][1]["call"]["url"] == "/x?token={{ token }}"


def test_extraction_unavailable_before_its_own_step_raises():
  flow = Flow("f")

  @flow.step
  def use(ctx):
    ctx.http.get(f"/x?token={ctx.vars['token']}")

  @flow.step
  def login(ctx):
    r = ctx.http.post("/login")
    ctx.vars["token"] = r.json_path("$.token")

  with pytest.raises(FlowCompileError, match="read before it was extracted"):
    flow.compile()


def test_compile_defaults_to_integration_profile():
  flow = Flow("f")

  @flow.step
  def a(ctx):
    ctx.http.get("/a")

  ir = flow.compile()
  assert ir["profile"] == {"mode": "integration"}


def test_data_bound_flow_sets_pool_fields():
  flow = Flow("f", data="fixtures/users.csv")

  @flow.step
  def a(ctx):
    ctx.http.get(f"/u/{ctx.user['email']}")

  ir = flow.compile()
  assert ir["flows"][0]["data"] == "user"
  assert ir["data_pools"] == [{"name": "user", "source": "fixtures/users.csv"}]
  assert ir["flows"][0]["steps"][0]["call"]["url"] == "/u/{{ user.email }}"


def test_run_without_compile_only_env_raises_not_implemented(monkeypatch):
  monkeypatch.delenv("FLOWBENCH_COMPILE_ONLY", raising=False)
  flow = Flow("f")

  @flow.step
  def a(ctx):
    ctx.http.get("/a")

  with pytest.raises(NotImplementedError, match="issue #25"):
    flow.run(Profile(mode="integration"))


def test_run_with_compile_only_env_prints_json(monkeypatch, capsys):
  monkeypatch.setenv("FLOWBENCH_COMPILE_ONLY", "1")
  flow = Flow("f")

  @flow.step
  def a(ctx):
    ctx.http.get("/a")

  flow.run(Profile(mode="integration"))
  out = capsys.readouterr().out
  assert '"mode": "integration"' in out
