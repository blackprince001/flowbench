import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from flowbench import Flow, FlowExecutionError, Profile, expect
from flowbench.target import TargetConfig


class _Handler(BaseHTTPRequestHandler):
  def log_message(self, *args):
    pass

  def _respond(self, status, body):
    payload = json.dumps(body).encode()
    self.send_response(status)
    self.send_header("Content-Length", str(len(payload)))
    self.end_headers()
    self.wfile.write(payload)

  def do_GET(self):
    self._respond(200, {"data": {"id": "abc"}})

  def do_POST(self):
    length = int(self.headers.get("Content-Length", 0))
    self.rfile.read(length) if length else b""
    self._respond(200, {"data": {"access_token": "tok-9"}})


@pytest.fixture
def server():
  srv = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
  thread = threading.Thread(target=srv.serve_forever, daemon=True)
  thread.start()
  yield srv
  srv.shutdown()
  thread.join()


@pytest.fixture
def base_url(server):
  port = server.server_address[1]
  return f"http://127.0.0.1:{port}"


def test_run_executes_and_writes_store(base_url, tmp_path, capsys):
  flow = Flow("ping")

  @flow.step
  def check(ctx):
    r = ctx.http.get("/ping")
    expect(r.status).to_be(200)

  flow.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))

  index = json.loads((tmp_path / "index.json").read_text())
  assert len(index) == 1
  assert index[0]["mode"] == "integration"
  assert index[0]["iterations"] == 1
  assert index[0]["error_rate"] == 0.0

  out = capsys.readouterr().out
  assert "1 iteration(s): 1 passed, 0 failed" in out
  assert "run saved to" in out


def test_run_with_data_pool_iterates_every_row(base_url, tmp_path, capsys):
  csv_path = tmp_path / "users.csv"
  csv_path.write_text("email,password\na@b.com,pw1\nc@d.com,pw2\n")

  flow = Flow("login", data=str(csv_path))
  seen = []

  @flow.step
  def login(ctx):
    seen.append(str(ctx.user["email"]))
    r = ctx.http.post("/login")
    ctx.vars["token"] = r.json_path("$.data.access_token")

  flow.run(
    Profile(mode="integration"), base_url=base_url, store=str(tmp_path / "store")
  )

  assert seen == ["a@b.com", "c@d.com"]
  index = json.loads((tmp_path / "store" / "index.json").read_text())
  assert index[0]["iterations"] == 2


def test_run_reports_failures_in_store(base_url, tmp_path):
  flow = Flow("bad")

  @flow.step
  def check(ctx):
    r = ctx.http.get("/ping")
    expect(r.status).to_be(999)

  flow.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))

  index = json.loads((tmp_path / "index.json").read_text())
  assert index[0]["error_rate"] == 1.0

  run_id = index[0]["id"]
  traces = json.loads((tmp_path / run_id / "traces.json").read_text())
  assert traces[0]["Outcome"] == "failed"


def test_run_stress_mode_needs_go_engine(base_url, tmp_path):
  flow = Flow("f")

  @flow.step
  def a(ctx):
    ctx.http.get("/a")

  with pytest.raises(FlowExecutionError, match="Go engine"):
    flow.run(Profile(mode="stress"), base_url=base_url, store=str(tmp_path))


def test_run_disallowed_mode_via_target_config(monkeypatch, base_url, tmp_path):
  flow = Flow("f")

  @flow.step
  def a(ctx):
    ctx.http.get("/a")

  stub_cfg = TargetConfig(
    name="strict", base_urls=[base_url], disallowed_modes=["system"]
  )
  monkeypatch.setattr(
    "flowbench.flow.resolve_target_via_binary", lambda *a, **kw: stub_cfg
  )

  with pytest.raises(FlowExecutionError, match="disallows"):
    flow.run(Profile(mode="system"), store=str(tmp_path))


def test_run_base_url_warns_on_stderr(base_url, tmp_path, capsys):
  flow = Flow("f")

  @flow.step
  def a(ctx):
    ctx.http.get("/a")

  flow.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))
  err = capsys.readouterr().err
  assert "no host allow-list enforced" in err
