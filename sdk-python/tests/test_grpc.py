import pytest

from flowbench import Bearer, Flow, FlowCompileError, Profile, env, expect
from flowbench.context import Context
from flowbench.drivers.trace import TraceDriver


def build(step_id="s", available=None):
  builder = TraceDriver(step_id, available if available is not None else set())
  return builder, Context(builder, has_data_pool=False)


def test_a_unary_call_records_the_schema_and_the_method():
  builder, ctx = build()
  ctx.grpc(
    "billing.v1.Billing/Charge",
    proto="grpc/billing.proto",
    message={"account": "acct_1", "amountCents": "1200"},
    headers={"x-request-id": "req-1"},
  )

  assert builder.kind == "grpc"
  assert builder.spec == {
    "proto": "grpc/billing.proto",
    "method": "billing.v1.Billing/Charge",
    "message": {"account": "acct_1", "amountCents": "1200"},
    "headers": {"x-request-id": "req-1"},
  }


def test_a_call_against_the_target_itself_carries_no_url():
  """A gRPC step has no path — the method is the path — so the common case
  names no address, and the YAML surface spells it the same way."""
  builder, ctx = build()
  ctx.grpc("a.B/C", proto="a.proto")

  assert "url" not in builder.spec


def test_an_explicit_address_is_recorded():
  builder, ctx = build()
  ctx.grpc("a.B/C", proto="a.proto", url="grpc://billing.internal:50051")

  assert builder.spec["url"] == "grpc://billing.internal:50051"


def test_import_paths_ride_along():
  builder, ctx = build()
  ctx.grpc("a.B/C", proto="a.proto", import_paths=["../shared", "../vendor"])

  assert builder.spec["import_paths"] == ["../shared", "../vendor"]


def test_a_bare_method_name_is_refused():
  _, ctx = build()
  with pytest.raises(FlowCompileError, match="fully qualified"):
    ctx.grpc("Charge", proto="a.proto")


def test_a_message_that_is_not_a_mapping_is_refused():
  """A protobuf message is a mapping of fields; a list cannot become one."""
  _, ctx = build()
  with pytest.raises(FlowCompileError, match="mapping of field"):
    ctx.grpc("a.B/C", proto="a.proto", message=[1, 2])


def test_status_and_body_read_the_same_as_any_other_response():
  """The status is the numeric gRPC code and the message arrives as JSON, so
  nothing about assertions or extraction is gRPC-specific."""
  builder, ctx = build()
  r = ctx.grpc("a.B/C", proto="a.proto")
  ctx.vars["charge_id"] = r.json_path("$.chargeId")
  expect(r.status).to_be(0)

  assert builder.extract == [{"var": "charge_id", "path": "$.chargeId"}]
  assert builder.assert_ == [{"source": "status", "op": "eq", "value": 0}]


def test_a_grpc_step_inherits_flow_level_auth():
  """Metadata is HTTP/2 headers, so a credential reaches a gRPC call the same
  way it reaches a call step's — and the compile-time flattening has to agree
  with ir.Step.MakesRequest about that."""
  flow = Flow("billing", auth=Bearer(env("BILLING_TOKEN")))

  @flow.step
  def charge(ctx):
    ctx.grpc("a.B/C", proto="a.proto")

  scenario = flow.compile(Profile(mode="integration"))
  step = scenario["flows"][0]["steps"][0]
  assert step["auth"]["scheme"] == "bearer"


def test_live_execution_points_at_the_go_engine():
  from flowbench.drivers.live import LiveDriver
  from flowbench.errors import FlowExecutionError
  from flowbench.target import TargetConfig

  driver = LiveDriver(
    TargetConfig(name="t", base_urls=["http://localhost:1"]),
    has_data_pool=False,
    secrets=None,
  )
  try:
    with pytest.raises(FlowExecutionError, match="flowbench run"):
      driver.grpc("a.B/C", proto="a.proto")
  finally:
    driver.close()
