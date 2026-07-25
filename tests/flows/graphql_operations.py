# GraphQL on the Python surface, paired with graphql_operations.flow.yaml.
# Compiles to equivalent IR (internal/conformance checks this).
#
# ctx.graphql compiles to a `graphql` step rather than a hand-rolled POST, so
# the engine reads the data/errors shape and fails the step on an operation
# error that arrives inside a 200.
from flowbench import Bearer, Flow, Profile, Retry, env, expect

FIND_PRODUCT = """query FindProduct($sku: String!) {
  product(sku: $sku) { id name priceCents }
}
"""

PLACE_ORDER = """mutation PlaceOrder($productId: ID!, $quantity: Int!) {
  placeOrder(productId: $productId, quantity: $quantity) { id status }
}
"""

REVIEWS = """query Reviews($sku: String!) {
  reviews(sku: $sku) { rating }
}
"""

flow = Flow("graphql_operations", auth=Bearer(env("GRAPH_TOKEN")))


@flow.step
def find_product(ctx):
  r = ctx.graphql(
    "/graphql",
    query=FIND_PRODUCT,
    variables={"sku": "FB-001"},
    operation_name="FindProduct",
  )
  expect(r.status).to_be(200)
  ctx.vars["product_id"] = r.json_path("$.data.product.id")


@flow.step(retry=Retry(on_status=[429], backoff="honor_retry_after", max_attempts=3))
def place_order(ctx):
  r = ctx.graphql(
    "/graphql",
    query=PLACE_ORDER,
    variables={"productId": ctx.vars["product_id"], "quantity": 2},
    headers={"X-Trace": env("TRACE_ID")},
  )
  expect(r.status).to_be(200)


@flow.step
def reviews_may_be_degraded(ctx):
  r = ctx.graphql(
    "/graphql",
    query=REVIEWS,
    variables={"sku": "FB-001"},
    on_errors="allow_partial",
  )
  expect(r.status).to_be(200)


if __name__ == "__main__":
  flow.run(Profile(mode="integration"))
