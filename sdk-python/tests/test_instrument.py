"""Auto-instrumentation of HTTP the flow's own code makes (issue #24).

The calls under test deliberately do not go through ctx.http: they are made
with a plain httpx client the way the team's own SDK would make them, which
is the whole point -- FlowBench never sees them being set up, only that they
happened.
"""

import asyncio
import contextlib
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import httpx
import pytest

from flowbench import Flow, Profile, expect, secret
from flowbench.context import Context
from flowbench.drivers.live import FlowAbortedError, LiveDriver
from flowbench.errors import FlowExecutionError
from flowbench.instrument import _call_name
from flowbench.redaction import PLACEHOLDER, SecretSet
from flowbench.span import finalize
from flowbench.target import TargetConfig


class _Handler(BaseHTTPRequestHandler):
  def log_message(self, *args):
    pass

  def _respond(self, status, body=None, headers=None):
    self.send_response(status)
    for k, v in (headers or {}).items():
      self.send_header(k, v)
    payload = json.dumps(body).encode() if body is not None else b""
    self.send_header("Content-Length", str(len(payload)))
    self.end_headers()
    if payload:
      self.wfile.write(payload)

  def do_GET(self):
    self._dispatch("GET")

  def do_POST(self):
    self._dispatch("POST")

  def _dispatch(self, method):
    length = int(self.headers.get("Content-Length", 0))
    body = self.rfile.read(length) if length else b""
    handler = self.server.routes.get((method, self.path.split("?")[0]))
    if handler is None:
      self._respond(404, {"error": "not found"})
      return
    handler(self, body)


@pytest.fixture
def server():
  srv = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
  srv.routes = {}
  thread = threading.Thread(target=srv.serve_forever, daemon=True)
  thread.start()
  yield srv
  srv.shutdown()
  thread.join()


@pytest.fixture
def base_url(server):
  return f"http://127.0.0.1:{server.server_address[1]}"


@pytest.fixture
def cfg(base_url):
  return TargetConfig(name="test", base_urls=[base_url])


def run_step(cfg, func):
  driver = LiveDriver(cfg, has_data_pool=False)
  driver.begin_step("step", None)
  with contextlib.suppress(FlowAbortedError):
    func(Context(driver, has_data_pool=False))
  driver.end_step()
  driver.close()
  return driver.result()


def names(span):
  return [c.name for c in span.children]


# -- the acceptance: three calls the flow's own code made ------------------


def test_three_sdk_calls_resolve_as_three_spans_with_phases(base_url, server, tmp_path):
  for path in ("/one", "/two", "/three"):
    server.routes[("GET", path)] = lambda h, b: h._respond(200, {"ok": True})

  client = httpx.Client()  # the flow's own client, as an SDK would build it
  flow = Flow("nested")

  @flow.step
  def gather(ctx):
    for path in ("/one", "/two", "/three"):
      client.get(base_url + path)

  flow.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))
  client.close()

  traces = json.loads(next(tmp_path.glob("*/traces.json")).read_text())
  step_span = traces[0]["Children"][0]
  assert step_span["Name"] == "gather"
  calls = step_span["Children"]
  assert [c["Name"] for c in calls] == ["GET /one", "GET /two", "GET /three"]

  for call in calls:
    assert call["Duration"] > 0
    legs = [c for c in call["Children"] if c["Name"] == "http_call"]
    assert len(legs) == 1
    phases = {c["Name"]: c["Duration"] for c in legs[0]["Children"]}
    # No dns sibling: httpcore resolves inside connect_tcp and traces no
    # separate event, so connect covers resolution on the Python path.
    assert "ttfb" in phases
    assert "transfer" in phases
    assert "dns" not in phases
    assert all(d >= 0 for d in phases.values())

  # The first call opens a connection; the client reuses it for the rest.
  assert "connect" in {c["Name"] for c in calls[0]["Children"][0]["Children"]}

  # Folded the same way, so the flame graph resolves all three too.
  folded = json.loads(next(tmp_path.glob("*/folded.json")).read_text())
  step_node = folded["root"]["children"]["flow:nested"]["children"]["gather"]
  assert set(step_node["children"]) == {"GET /one", "GET /two", "GET /three"}
  one = step_node["children"]["GET /one"]["children"]["http_call"]
  assert "ttfb" in one["children"]


def test_sdk_call_spans_carry_call_identity(cfg, base_url, server):
  server.routes[("POST", "/orders")] = lambda h, b: h._respond(201, {"id": 7})

  def step(ctx):
    httpx.post(base_url + "/orders", json={"sku": "abc"})

  result = run_step(cfg, step)
  call = result.spans[0].children[0]
  finalize(call, SecretSet(), 2048)
  assert call.payload["method"] == "POST"
  assert call.payload["status"] == 201
  assert call.payload["url"].endswith("/orders")
  assert json.loads(call.payload["request"]) == {"sku": "abc"}
  assert json.loads(call.payload["response"]) == {"id": 7}


def test_step_needs_no_ctx_http_call_when_its_own_code_calls(cfg, base_url, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(200, {})

  def step(ctx):
    httpx.get(base_url + "/x")

  assert run_step(cfg, step).outcome == "ok"


def test_step_that_calls_nothing_at_all_is_still_rejected(cfg):
  with pytest.raises(FlowExecutionError, match=r"made no HTTP call"):
    run_step(cfg, lambda ctx: None)


# -- parity with the Go adapter's shape ------------------------------------


def test_ctx_http_keeps_the_engine_span_shape(cfg, server):
  server.routes[("GET", "/ping")] = lambda h, b: h._respond(200, {"ok": True})

  def step(ctx):
    r = ctx.http.get("/ping")
    expect(r.status).to_be(200)

  step_span = run_step(cfg, step).spans[0]
  # The step span *is* the call span, as in internal/adapters/http.go -- no
  # intermediate call span the Go path would have no counterpart for.
  assert "http_call" in names(step_span)
  assert not any(c.name.startswith("GET ") for c in step_span.children)
  assert "ttfb" in names(step_span.children[0])


def test_redirect_hops_each_get_their_own_leg(cfg, base_url, server):
  server.routes[("GET", "/from")] = lambda h, b: h._respond(
    302, None, {"Location": "/to"}
  )
  server.routes[("GET", "/to")] = lambda h, b: h._respond(200, {"ok": True})

  def step(ctx):
    httpx.get(base_url + "/from", follow_redirects=True)

  call = run_step(cfg, step).spans[0].children[0]
  assert names(call) == ["http_call", "http_call"]


def test_ids_in_the_path_fold_to_one_name():
  def name(url):
    return _call_name(httpx.Request("GET", url))

  assert name("http://h/tickets/4821/route") == "GET /tickets/:id/route"
  assert name("http://h/v1/chat/completions") == "GET /v1/chat/completions"
  assert name("http://h/o/123e4567-e89b-12d3-a456-426614174000") == "GET /o/:id"
  assert name("http://h/blobs/deadbeefcafe") == "GET /blobs/:id"
  assert name("http://h") == "GET /"


# -- outcome classification ------------------------------------------------


def test_429_from_an_sdk_call_throttles_the_step(cfg, base_url, server):
  server.routes[("GET", "/limited")] = lambda h, b: h._respond(429, {})

  def step(ctx):
    httpx.get(base_url + "/limited")

  result = run_step(cfg, step)
  assert result.throttled is True
  assert result.outcome == "throttled"
  assert result.spans[0].children[0].outcome == "throttled"


def test_transport_failure_marks_the_span_and_still_raises(cfg):
  def step(ctx):
    httpx.get("http://127.0.0.1:1/nope")

  driver = LiveDriver(cfg, has_data_pool=False)
  driver.begin_step("step", None)
  with pytest.raises(httpx.ConnectError):
    step(Context(driver, has_data_pool=False))
  call = driver._current_span.children[0]
  assert call.outcome == "failed"
  driver.close()


# -- redaction -------------------------------------------------------------


def test_flagged_value_is_redacted_from_stored_artifacts(base_url, server, tmp_path):
  token = "sk-live-4a9f2c7e"
  secret(token)
  server.routes[("POST", "/echo")] = lambda h, b: h._respond(200, json.loads(b))

  client = httpx.Client(headers={"Authorization": f"Bearer {token}"})
  flow = Flow("leaky")

  @flow.step
  def call_provider(ctx):
    assert client.post(base_url + "/echo", json={"key": token}).status_code == 200

  flow.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))
  client.close()

  artifacts = "".join(p.read_text() for p in tmp_path.rglob("*.json"))
  assert token not in artifacts
  assert PLACEHOLDER in artifacts


def test_ctx_secret_redacts_a_value_the_step_computed(cfg, base_url, server):
  minted = "tok-minted-mid-flow"
  server.routes[("GET", "/x")] = lambda h, b: h._respond(200, {"echo": minted})

  def step(ctx):
    ctx.secret(minted)
    httpx.get(base_url + "/x")

  driver = LiveDriver(cfg, has_data_pool=False)
  driver.begin_step("step", None)
  step(Context(driver, has_data_pool=False))
  driver.end_step()
  driver.close()

  call = driver.result().spans[0].children[0]
  finalize(call, driver._secrets, 2048)
  assert minted not in call.payload["response"]
  assert PLACEHOLDER in call.payload["response"]


# -- async clients ---------------------------------------------------------
#
# The step function stays sync -- the driver is -- so an async client is driven
# the way a flow would drive one: asyncio.run inside the step. The task copies
# the context, which is how the attached driver reaches it.


def test_an_async_client_is_recorded_with_its_phases(cfg, base_url, server):
  server.routes[("GET", "/async")] = lambda h, b: h._respond(200, {"ok": True})

  def step(ctx):
    async def go():
      async with httpx.AsyncClient() as client:
        await client.get(base_url + "/async")

    asyncio.run(go())

  call = run_step(cfg, step).spans[0].children[0]
  assert call.name == "GET /async"
  assert call.duration > 0
  leg = call.children[0]
  assert leg.name == "http_call"
  assert "ttfb" in names(leg)


def test_concurrent_calls_keep_their_own_phases(cfg, base_url, server):
  """The reason the current call lives in a ContextVar rather than a stack:
  three requests are open at once and their responses arrive out of order, so
  "the most recently started call" is the wrong owner for an arriving phase
  event. Each response must close its own ttfb.
  """
  server.routes[("GET", "/slow")] = lambda h, b: (
    time.sleep(0.2),
    h._respond(200, {}),
  )
  server.routes[("GET", "/mid")] = lambda h, b: (time.sleep(0.1), h._respond(200, {}))
  server.routes[("GET", "/fast")] = lambda h, b: h._respond(200, {})

  def step(ctx):
    async def go():
      async with httpx.AsyncClient() as client:
        await asyncio.gather(
          *(client.get(base_url + path) for path in ("/slow", "/mid", "/fast"))
        )

    asyncio.run(go())

  step_span = run_step(cfg, step).spans[0]
  calls = {c.name: c for c in step_span.children}
  assert set(calls) == {"GET /slow", "GET /mid", "GET /fast"}

  def ttfb(call):
    leg = call.children[0]
    return next(c.duration for c in leg.children if c.name == "ttfb")

  # Each call waited as long as its own endpoint slept, which is only true if
  # no phase crossed between the requests in flight.
  assert ttfb(calls["GET /slow"]) > ttfb(calls["GET /mid"]) > ttfb(calls["GET /fast"])
  assert ttfb(calls["GET /slow"]) == pytest.approx(0.2, abs=0.08)
  assert ttfb(calls["GET /fast"]) < 0.05


def test_429_from_an_async_call_throttles_the_step(cfg, base_url, server):
  server.routes[("GET", "/limited")] = lambda h, b: h._respond(429, {})

  def step(ctx):
    async def go():
      async with httpx.AsyncClient() as client:
        await client.get(base_url + "/limited")

    asyncio.run(go())

  result = run_step(cfg, step)
  assert result.outcome == "throttled"
  assert result.spans[0].children[0].outcome == "throttled"


def test_an_async_transport_failure_marks_the_span(cfg):
  def step(ctx):
    async def go():
      async with httpx.AsyncClient() as client:
        await client.get("http://127.0.0.1:1/nope")

    asyncio.run(go())

  driver = LiveDriver(cfg, has_data_pool=False)
  driver.begin_step("step", None)
  with pytest.raises(httpx.ConnectError):
    step(Context(driver, has_data_pool=False))
  assert driver._current_span.children[0].outcome == "failed"
  driver.close()


def test_async_requests_outside_a_run_are_untouched(base_url, server):
  server.routes[("GET", "/free")] = lambda h, b: h._respond(200, {"ok": True})

  async def go():
    async with httpx.AsyncClient() as client:
      return await client.get(base_url + "/free")

  assert asyncio.run(go()).status_code == 200


# -- staying out of the way ------------------------------------------------


def test_requests_outside_a_run_are_untouched(base_url, server):
  server.routes[("GET", "/free")] = lambda h, b: h._respond(200, {"ok": True})
  assert httpx.get(base_url + "/free").status_code == 200


def test_an_existing_trace_extension_still_fires(cfg, base_url, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(200, {})
  seen = []

  def step(ctx):
    with httpx.Client() as client:
      client.send(
        client.build_request(
          "GET", base_url + "/x", extensions={"trace": lambda n, i: seen.append(n)}
        )
      )

  step_span = run_step(cfg, step).spans[0]
  assert any(n.endswith("receive_response_headers.complete") for n in seen)
  assert "http_call" in names(step_span.children[0])
