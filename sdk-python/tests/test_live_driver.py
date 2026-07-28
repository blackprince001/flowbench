import contextlib
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from flowbench.assertions import expect
from flowbench.context import Context
from flowbench.drivers.live import FlowAbortedError, LiveDriver
from flowbench.errors import FlowExecutionError
from flowbench.redaction import SecretSet
from flowbench.retry import Retry
from flowbench.span import finalize
from flowbench.target import TargetConfig


class _Handler(BaseHTTPRequestHandler):
  def log_message(self, *args):
    pass

  def _read_body(self):
    length = int(self.headers.get("Content-Length", 0))
    return self.rfile.read(length) if length else b""

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
    server = self.server
    body = self._read_body()
    handler = server.routes.get((method, self.path.split("?")[0]))
    if handler is None:
      self._respond(404, {"error": "not found"})
      return
    handler(self, body)


@pytest.fixture
def server():
  srv = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
  srv.routes = {}
  srv.hits = {}
  thread = threading.Thread(target=srv.serve_forever, daemon=True)
  thread.start()
  yield srv
  srv.shutdown()
  thread.join()


@pytest.fixture
def cfg(server):
  port = server.server_address[1]
  return TargetConfig(name="test", base_urls=[f"http://127.0.0.1:{port}"])


def run_step(cfg, func, *, has_data_pool=False, row=None, retry=None):
  driver = LiveDriver(cfg, has_data_pool=has_data_pool)
  driver.set_row(row)
  driver.begin_step("step", retry)
  ctx = Context(driver, has_data_pool=has_data_pool)
  with contextlib.suppress(FlowAbortedError):
    func(ctx)
  driver.end_step()
  driver.close()
  return driver.result()


def child_named(span, name):
  """Extraction and assertion children sit alongside the http_call leg
  auto-instrumentation adds, so they are looked up by name, not position.
  """
  return next(c for c in span.children if c.name == name)


def test_successful_call_records_ok_span(cfg, server):
  server.routes[("GET", "/ping")] = lambda h, b: h._respond(200, {"ok": True})

  def step(ctx):
    r = ctx.http.get("/ping")
    expect(r.status).to_be(200)

  result = run_step(cfg, step)
  assert result.outcome == "ok"
  assert result.failures == []
  assert len(result.spans) == 1
  span = result.spans[0]
  assert span.name == "step"
  finalize(span, SecretSet(), 2048)
  assert span.payload["method"] == "GET"
  assert span.payload["status"] == 200


def test_extraction_and_downstream_use(cfg, server):
  server.routes[("POST", "/login")] = lambda h, b: h._respond(
    200, {"data": {"access_token": "tok-9"}}
  )

  captured = {}

  def step(ctx):
    r = ctx.http.post("/login")
    ctx.vars["token"] = r.json_path("$.data.access_token")
    captured["token"] = str(ctx.vars["token"])

  result = run_step(cfg, step)
  assert result.outcome == "ok"
  assert captured["token"] == "tok-9"


def test_extraction_not_found_aborts_and_records_failure(cfg, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(200, {"data": {}})

  def step(ctx):
    r = ctx.http.get("/x")
    ctx.vars["token"] = r.json_path("$.data.access_token")

  result = run_step(cfg, step)
  assert result.outcome == "failed"
  assert len(result.failures) == 1
  step_id, detail = result.failures[0]
  assert step_id == "step"
  assert "token" in detail
  extract_child = child_named(result.spans[0], "token")
  assert extract_child.outcome == "failed"


def test_assertion_failure_aborts_and_records_child_span(cfg, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(500, {})

  def step(ctx):
    r = ctx.http.get("/x")
    expect(r.status).to_be(200)

  result = run_step(cfg, step)
  assert result.outcome == "failed"
  assert len(result.failures) == 1
  assert child_named(result.spans[0], "assert_status").outcome == "failed"


def test_passing_assertion_creates_ok_child_span(cfg, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(200, {})

  def step(ctx):
    r = ctx.http.get("/x")
    expect(r.status).to_be(200)

  result = run_step(cfg, step)
  assert result.outcome == "ok"
  assert child_named(result.spans[0], "assert_status").outcome == "ok"


def test_throttle_429_marks_throttled_and_aborts(cfg, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(429, {})

  def step(ctx):
    ctx.http.get("/x")

  result = run_step(cfg, step)
  assert result.throttled is True
  assert result.spans[0].outcome == "throttled"
  assert len(result.failures) == 1
  assert "throttled" in result.failures[0][1]


def test_retry_succeeds_after_429(cfg, server):
  hits = {"n": 0}

  def handler(h, b):
    hits["n"] += 1
    if hits["n"] == 1:
      h._respond(429, {})
    else:
      h._respond(200, {})

  server.routes[("GET", "/x")] = handler

  def step(ctx):
    r = ctx.http.get("/x")
    expect(r.status).to_be(200)

  retry = Retry(on_status=[429], backoff="fixed", max_attempts=3, base_delay="1ms")
  result = run_step(cfg, step, retry=retry)
  assert result.outcome == "ok"
  assert hits["n"] == 2
  step_span = result.spans[0]
  attempt_children = [c for c in step_span.children if c.name.startswith("attempt")]
  backoff_children = [c for c in step_span.children if c.name == "backoff"]
  assert len(attempt_children) == 2
  assert len(backoff_children) == 1


def test_retry_exhausted_still_throttled(cfg, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(429, {})

  def step(ctx):
    ctx.http.get("/x")

  retry = Retry(on_status=[429], backoff="fixed", max_attempts=2, base_delay="1ms")
  result = run_step(cfg, step, retry=retry)
  assert result.throttled is True
  step_span = result.spans[0]
  attempt_children = [c for c in step_span.children if c.name.startswith("attempt")]
  assert len(attempt_children) == 2


def test_allow_list_rejects_out_of_origin_call(cfg):
  def step(ctx):
    ctx.http.get("http://evil.example.com/x")

  result = run_step(cfg, step)
  assert result.outcome == "failed"
  assert "allow-list" in result.failures[0][1]


def test_data_pool_row_access(cfg, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(200, {})

  captured = {}

  def step(ctx):
    captured["email"] = str(ctx.user["email"])
    ctx.http.get("/x")

  run_step(cfg, step, has_data_pool=True, row={"email": "a@b.com", "password": "pw"})
  assert captured["email"] == "a@b.com"


def test_env_value_registered_as_secret_and_redacted(cfg, server, monkeypatch):
  monkeypatch.setenv("FLOWBENCH_TEST_SECRET", "hunter2")
  server.routes[("GET", "/x")] = lambda h, b: h._respond(
    500, {"error": "hunter2 leaked"}
  )

  def step(ctx):
    _ = ctx.env["FLOWBENCH_TEST_SECRET"]
    r = ctx.http.get("/x")
    expect(r.status).to_be(200)

  result = run_step(cfg, step)
  assert result.outcome == "failed"
  assert "hunter2" not in result.failures[0][1]
  secrets = SecretSet()
  secrets.add("hunter2")
  finalize(result.spans[0], secrets, 2048)
  assert "hunter2" not in result.spans[0].payload.get("response", "")


def test_json_body_preserves_native_types(cfg, server):
  received = {}

  def handler(h, b):
    received["body"] = json.loads(b)
    h._respond(200, {})

  server.routes[("POST", "/x")] = handler

  def step(ctx):
    r = ctx.http.post("/x", json={"count": 3, "active": True})
    expect(r.status).to_be(200)

  run_step(cfg, step)
  assert received["body"] == {"count": 3, "active": True}


def test_vars_setitem_rejects_non_extraction(cfg, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(200, {})

  def step(ctx):
    ctx.vars["token"] = "not-an-extraction"

  driver = LiveDriver(cfg, has_data_pool=False)
  driver.begin_step("step", None)
  ctx = Context(driver, has_data_pool=False)
  with pytest.raises(FlowExecutionError, match="json_path"):
    step(ctx)
  driver.close()


def test_end_step_rejects_step_that_never_called(cfg):
  driver = LiveDriver(cfg, has_data_pool=False)
  driver.begin_step("step", None)
  with pytest.raises(FlowExecutionError, match=r"made no HTTP call"):
    driver.end_step()
  driver.close()


def test_call_rejects_second_call_in_same_step(cfg, server):
  server.routes[("GET", "/x")] = lambda h, b: h._respond(200, {})

  def step(ctx):
    ctx.http.get("/x")
    ctx.http.get("/x")

  driver = LiveDriver(cfg, has_data_pool=False)
  driver.begin_step("step", None)
  ctx = Context(driver, has_data_pool=False)
  with pytest.raises(FlowExecutionError, match=r"more than one ctx\.http call"):
    step(ctx)
  driver.close()
