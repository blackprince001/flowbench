"""Loads tests/flows/authenticated_checkout.py (the PRD's example, plus the
reconciling assertion -- see that file's header comment) and checks its
compiled shape directly in Python. Complements, and doesn't depend on,
internal/conformance's Go<->Python structural diff against the YAML fixture.
"""

import importlib.util
from pathlib import Path

FIXTURE = Path(__file__).parents[2] / "tests" / "flows" / "authenticated_checkout.py"


def _load_fixture_flow():
  spec = importlib.util.spec_from_file_location(
    "authenticated_checkout_fixture", FIXTURE
  )
  module = importlib.util.module_from_spec(spec)
  spec.loader.exec_module(module)
  return module.flow


def test_fixture_compiles_three_call_steps_in_order():
  ir = _load_fixture_flow().compile()
  steps = ir["flows"][0]["steps"]
  assert [s["id"] for s in steps] == ["login", "create_order", "pay"]
  assert all(s["type"] == "call" for s in steps)


def test_fixture_data_pool_binding():
  ir = _load_fixture_flow().compile()
  assert ir["flows"][0]["data"] == "user"
  assert ir["data_pools"] == [{"name": "user", "source": "fixtures/users.csv"}]


def test_fixture_login_step_shape():
  ir = _load_fixture_flow().compile()
  login = ir["flows"][0]["steps"][0]
  assert login["call"] == {
    "method": "POST",
    "url": "/auth/login",
    "body": {"email": "{{ user.email }}", "password": "{{ user.password }}"},
  }
  assert login["extract"] == [{"var": "token", "path": "$.data.access_token"}]
  assert login["assert"] == [
    {"source": "status", "op": "eq", "value": 200},
    {"source": "var", "op": "exists", "key": "token"},
  ]


def test_fixture_create_order_step_has_retry_and_templated_header():
  ir = _load_fixture_flow().compile()
  create_order = ir["flows"][0]["steps"][1]
  assert create_order["call"]["headers"] == {"Authorization": "Bearer {{ token }}"}
  assert create_order["call"]["body"] == {"items": "{{ user.cart }}"}
  assert create_order["retry"] == {
    "on_status": [429, 503],
    "backoff": "honor_retry_after",
    "max_attempts": 5,
  }


def test_fixture_pay_step_templated_url():
  ir = _load_fixture_flow().compile()
  pay = ir["flows"][0]["steps"][2]
  assert pay["call"]["url"] == "/orders/{{ order_id }}/pay"
  assert pay["assert"] == [{"source": "status", "op": "eq", "value": 202}]


def test_fixture_profile_matches_prd_stress_example():
  flow = _load_fixture_flow()
  from flowbench import Profile

  ir = flow.compile(
    Profile(
      mode="stress",
      vus="ramp(0 -> 500, 5m)",
      hold="10m",
      arrival_cap="300/s",
      thresholds=["p95(latency) < 800ms", "error_rate < 1%"],
    )
  )
  assert ir["profile"] == {
    "mode": "stress",
    "ramp": "0 -> 500 over 5m",
    "hold": "10m",
    "arrival_cap": "300/s",
    "thresholds": ["p95(latency) < 800ms", "error_rate < 1%"],
  }
