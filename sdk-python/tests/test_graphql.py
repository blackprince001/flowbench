import re

import pytest

from flowbench import Flow, FlowCompileError, Profile, expect
from flowbench.context import Context, StepBuilder

QUERY = "query FindProduct($sku: String!) { product(sku: $sku) { id } }"


def build(step_id="s"):
  builder = StepBuilder(step_id, set())
  return builder, Context(builder, has_data_pool=False)


def test_graphql_records_the_operation_spec():
  builder, ctx = build()
  ctx.graphql("/graphql", query=QUERY, variables={"sku": "FB-001"})

  assert builder.kind == "graphql"
  assert builder.spec == {
    "url": "/graphql",
    "query": QUERY,
    "variables": {"sku": "FB-001"},
  }


def test_optional_fields_are_omitted_when_unset():
  """Matching the YAML surface, so both compile to the same IR."""
  builder, ctx = build()
  ctx.graphql("/graphql", query=QUERY)

  assert builder.spec == {"url": "/graphql", "query": QUERY}


def test_optional_fields_are_carried_when_set():
  builder, ctx = build()
  ctx.graphql(
    "/graphql",
    query=QUERY,
    variables={"sku": "FB-001"},
    operation_name="FindProduct",
    headers={"X-Trace": "abc"},
    on_errors="allow_partial",
  )

  assert builder.spec["operation_name"] == "FindProduct"
  assert builder.spec["headers"] == {"X-Trace": "abc"}
  assert builder.spec["on_errors"] == "allow_partial"


def test_unknown_error_policy_is_rejected():
  _, ctx = build()
  with pytest.raises(FlowCompileError, match="on_errors must be one of"):
    ctx.graphql("/graphql", query=QUERY, on_errors="shrug")


def test_a_step_still_makes_exactly_one_request():
  _, ctx = build()
  ctx.graphql("/graphql", query=QUERY)
  with pytest.raises(FlowCompileError, match=re.escape("more than one ctx.http call")):
    ctx.http.get("/health")


def test_graphql_step_compiles_to_a_graphql_ir_step():
  flow = Flow("shop")

  @flow.step
  def find(ctx):
    r = ctx.graphql("/graphql", query=QUERY, variables={"sku": "FB-001"})
    expect(r.status).to_be(200)
    ctx.vars["product_id"] = r.json_path("$.data.product.id")

  compiled = flow.compile(Profile(mode="integration"))
  step = compiled["flows"][0]["steps"][0]

  assert step["type"] == "graphql"
  assert step["graphql"]["query"] == QUERY
  assert "call" not in step
  assert step["extract"] == [{"var": "product_id", "path": "$.data.product.id"}]


def test_graphql_step_inherits_flow_auth():
  """A graphql step is a request-making step, so the flow default reaches it."""
  from flowbench import Bearer, env

  flow = Flow("shop", auth=Bearer(env("GRAPH_TOKEN")))

  @flow.step
  def find(ctx):
    ctx.graphql("/graphql", query=QUERY)

  step = flow.compile(Profile(mode="integration"))["flows"][0]["steps"][0]
  assert step["auth"] == {"scheme": "bearer", "token": "{{ env.GRAPH_TOKEN }}"}


def test_extracted_value_flows_into_a_mutation_variable():
  flow = Flow("shop")

  @flow.step
  def find(ctx):
    r = ctx.graphql("/graphql", query=QUERY, variables={"sku": "FB-001"})
    ctx.vars["product_id"] = r.json_path("$.data.product.id")

  @flow.step
  def order(ctx):
    ctx.graphql(
      "/graphql",
      query="mutation M($id: ID!) { placeOrder(productId: $id) { id } }",
      variables={"id": ctx.vars["product_id"], "quantity": 2},
    )

  steps = flow.compile(Profile(mode="integration"))["flows"][0]["steps"]
  variables = steps[1]["graphql"]["variables"]
  assert variables["id"] == "{{ product_id }}"
  # The number stays a number: the server types it against the schema.
  assert variables["quantity"] == 2
