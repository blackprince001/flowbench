# gRPC on the Python surface, paired with grpc_unary.flow.yaml.
# Compiles to equivalent IR (internal/conformance checks this).
#
# ctx.grpc compiles to a `grpc` step: one unary call, the schema named by its
# .proto and the method fully qualified. No url — a gRPC step has no path,
# because the method is the path, so a call against the target itself names no
# address. The response message arrives as JSON, so json_path reads it the way
# it reads any other body.
from flowbench import Flow, Profile, expect

flow = Flow("billing")


@flow.step
def charge(ctx):
  r = ctx.grpc(
    "billing.v1.Billing/Charge",
    proto="grpc/billing.proto",
    message={"account": "acct_1", "amountCents": "1200", "currency": "GHS"},
    headers={"x-request-id": "req-1"},
  )
  ctx.vars["charge_id"] = r.json_path("$.chargeId")
  # 0 is OK. A gRPC status is a small integer in its own numbering, and
  # nothing in it is 200.
  expect(r.status).to_be(0)
  expect(r.json_path("$.status")).to_be("captured")


@flow.step
def refund(ctx):
  r = ctx.grpc(
    "billing.v1.Billing/Refund",
    proto="grpc/billing.proto",
    message={"chargeId": ctx.vars["charge_id"]},
  )
  expect(r.status).to_be(0)
  expect(r.json_path("$.status")).to_be("refunded")


if __name__ == "__main__":
  flow.run(Profile(mode="integration"))
