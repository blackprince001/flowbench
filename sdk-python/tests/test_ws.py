import pytest

from flowbench import Bearer, Flow, FlowCompileError, Profile, env, expect, frame
from flowbench.context import Context
from flowbench.drivers.trace import TraceDriver


def build(step_id="s", available=None):
  builder = TraceDriver(step_id, available if available is not None else set())
  return builder, Context(builder, has_data_pool=False)


def test_opening_a_session_records_the_handshake():
  builder, ctx = build()
  ctx.ws("/feed", subprotocols=["flowbench.v1"], headers={"X-Client": "flowbench"})

  assert builder.kind == "ws"
  assert builder.spec == {
    "url": "/feed",
    "subprotocols": ["flowbench.v1"],
    "headers": {"X-Client": "flowbench"},
  }


def test_a_step_on_an_open_session_carries_no_url():
  """Matching the YAML surface, so both compile to the same IR."""
  builder, ctx = build()
  ctx.ws(send={"op": "ping"}, receive=True)

  assert builder.spec == {"send": {"op": "ping"}, "receive": {}}


def test_match_conditions_are_a_list_either_way():
  builder, ctx = build()
  ctx.ws(
    receive=[frame("$.type").to_be("tick"), frame("$.symbol").to_be("FB-001")],
    timeout="5s",
  )

  assert builder.spec["receive"] == {
    "match": [
      {"source": "body", "key": "$.type", "op": "eq", "value": "tick"},
      {"source": "body", "key": "$.symbol", "op": "eq", "value": "FB-001"},
    ],
    "timeout": "5s",
  }

  builder, ctx = build()
  ctx.ws(receive=frame("$.type").to_be("ack"))
  assert builder.spec["receive"]["match"] == [
    {"source": "body", "key": "$.type", "op": "eq", "value": "ack"}
  ]


def test_frame_conditions_do_not_become_step_assertions():
  """A match filters which frame the step is about; the step's own
  assertions judge the one it matched."""
  builder, ctx = build()
  ctx.ws(receive=frame("$.type").to_be("ack"))

  assert builder.assert_ == []


def test_expect_asserts_on_the_frame_body():
  builder, ctx = build()
  r = ctx.ws(receive=True)
  expect(r.json_path("$.status")).to_be("ok")

  assert builder.assert_ == [
    {"source": "body", "key": "$.status", "op": "eq", "value": "ok"}
  ]


def test_a_step_must_do_something():
  _, ctx = build()
  with pytest.raises(FlowCompileError, match="open a session"):
    ctx.ws()


def test_handshake_options_need_a_handshake():
  _, ctx = build()
  with pytest.raises(FlowCompileError, match="belong to the call that opens"):
    ctx.ws(send={"op": "ping"}, headers={"X-Trace": "abc"})


def test_timeout_needs_something_to_bound():
  _, ctx = build()
  with pytest.raises(FlowCompileError, match="bounds a receive"):
    ctx.ws(send={"op": "ping"}, timeout="2s")


def test_receive_rejects_anything_but_true_or_frame_conditions():
  _, ctx = build()
  with pytest.raises(FlowCompileError, match=r"frame\(\.\.\.\) conditions"):
    ctx.ws(receive="$.type == 'ack'")


def test_flow_auth_reaches_the_handshake_and_stops_there():
  """ir.Step.MakesRequest: credentials ride on the handshake, and a step on an
  already-open session has no request left to decorate."""
  flow = Flow("feed", auth=Bearer(env("FEED_TOKEN")))

  @flow.step
  def open_feed(ctx):
    ctx.ws("/feed")

  @flow.step
  def ping(ctx):
    ctx.ws(send={"op": "ping"}, receive=True)

  steps = flow.compile(Profile(mode="integration"))["flows"][0]["steps"]
  assert steps[0]["auth"]["scheme"] == "bearer"
  assert "auth" not in steps[1]
