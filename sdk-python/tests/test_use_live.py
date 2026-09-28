import json
import shutil
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import ClassVar

import pytest

from flowbench import Flow, FlowExecutionError, Profile, expect

REPO_ROOT = Path(__file__).parents[2]


@pytest.fixture(scope="module")
def flowbench_binary(tmp_path_factory):
  if shutil.which("go") is None:
    pytest.skip("no go toolchain on PATH")
  out = tmp_path_factory.mktemp("bin") / "flowbench"
  result = subprocess.run(
    ["go", "build", "-o", str(out), "./cmd/flowbench"],
    cwd=REPO_ROOT,
    capture_output=True,
    text=True,
  )
  if result.returncode != 0:
    pytest.skip(f"could not build flowbench binary: {result.stderr}")
  return str(out)


class _Handler(BaseHTTPRequestHandler):
  requests: ClassVar[list] = []

  def log_message(self, *args):
    pass

  def _read_body(self):
    length = int(self.headers.get("Content-Length", 0))
    return self.rfile.read(length) if length else b""

  def _respond(self, status, body):
    payload = json.dumps(body).encode()
    self.send_response(status)
    self.send_header("Content-Length", str(len(payload)))
    self.end_headers()
    self.wfile.write(payload)

  def do_POST(self):
    body = self._read_body()
    type(self).requests.append((self.path, body))
    if self.path == "/boom":
      self._respond(500, {"error": "nope"})
      return
    self._respond(200, {"data": {"access_token": "tok-9"}})

  def do_GET(self):
    self._respond(200, {"data": {"id": "abc"}})


@pytest.fixture
def server():
  _Handler.requests = []
  srv = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
  thread = threading.Thread(target=srv.serve_forever, daemon=True)
  thread.start()
  yield srv
  srv.shutdown()
  thread.join()


@pytest.fixture
def base_url(server):
  return f"http://127.0.0.1:{server.server_address[1]}"


def _python_login_flow():
  login = Flow("login", inputs={"password": None}, outputs=["token"])

  @login.step
  def login_step(ctx):
    r = ctx.http.post("/auth/login", json={"password": ctx.inputs["password"]})
    expect(r.status).to_be(200)
    ctx.vars["token"] = r.json_path("$.data.access_token")

  return login


def test_use_python_flow_reads_output_live(base_url, tmp_path):
  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(_python_login_flow(), with_={"password": "pw"})

  @checkout.step
  def create_order(ctx):
    r = ctx.http.post(
      "/orders", headers={"Authorization": f"Bearer {ctx.vars['auth.token']}"}
    )
    expect(r.status).to_be(200)

  checkout.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))

  paths = [p for p, _ in _Handler.requests]
  assert paths == ["/auth/login", "/orders"]

  index = json.loads((tmp_path / "index.json").read_text())
  assert index[0]["iterations"] == 1
  assert index[0]["error_rate"] == 0.0


def _traces(store_dir):
  index = json.loads((store_dir / "index.json").read_text())
  run_id = index[-1]["id"]
  return json.loads((store_dir / run_id / "traces.json").read_text())


def test_use_python_flow_span_nests_under_use_step(base_url, tmp_path):
  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(_python_login_flow(), with_={"password": "pw"})

  checkout.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))

  root = _traces(tmp_path)[0]
  assert root["Children"][0]["Name"] == "auth"
  assert root["Children"][0]["Children"][0]["Name"] == "login_step"


def test_use_python_flow_missing_required_input_raises(base_url, tmp_path):
  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(_python_login_flow())  # no password

  with pytest.raises(FlowExecutionError, match="requires input 'password'"):
    checkout.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))


def test_use_python_flow_failure_fails_the_use_step(base_url, tmp_path):
  # login posts to /boom, which the stub answers with a 500 -- the nested
  # step's own assertion fails, and that failure has to surface as the
  # *use* step failing, not go missing.
  boom_login = Flow("boom_login", inputs={"password": None}, outputs=["token"])

  @boom_login.step
  def login_step(ctx):
    r = ctx.http.post("/boom", json={"password": ctx.inputs["password"]})
    expect(r.status).to_be(200)
    ctx.vars["token"] = r.json_path("$.data.access_token")

  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(boom_login, with_={"password": "pw"})

  checkout.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))

  index = json.loads((tmp_path / "index.json").read_text())
  assert index[-1]["error_rate"] == 1.0

  root = _traces(tmp_path)[0]
  assert root["Outcome"] == "failed"
  assert root["Children"][0]["Name"] == "auth"
  assert root["Children"][0]["Outcome"] == "failed"


def _loaded_login_flow(tmp_path, flowbench_binary, monkeypatch):
  monkeypatch.setenv("FLOWBENCH_BIN", flowbench_binary)
  path = tmp_path / "login.flow.yaml"
  path.write_text(
    "flow: login\n"
    "inputs:\n"
    "  password:\n"
    "steps:\n"
    "  - id: login\n"
    "    call: POST /auth/login\n"
    '    body: { password: "{{ inputs.password }}" }\n'
    "    extract: { token: $.data.access_token }\n"
    "    assert: [ status == 200 ]\n"
    "outputs: [token]\n"
  )
  return Flow.load(str(path))


def test_use_loaded_yaml_flow_reads_output_live(
  base_url, tmp_path, flowbench_binary, monkeypatch
):
  loaded = _loaded_login_flow(tmp_path, flowbench_binary, monkeypatch)
  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(loaded, with_={"password": "pw"})

  @checkout.step
  def create_order(ctx):
    r = ctx.http.post(
      "/orders", headers={"Authorization": f"Bearer {ctx.vars['auth.token']}"}
    )
    expect(r.status).to_be(200)

  store = tmp_path / "store"
  checkout.run(Profile(mode="integration"), base_url=base_url, store=str(store))

  index = json.loads((store / "index.json").read_text())
  assert index[0]["iterations"] == 1
  assert index[0]["error_rate"] == 0.0
  paths = [p for p, _ in _Handler.requests]
  assert paths == ["/auth/login", "/orders"]


def test_use_loaded_yaml_flow_graphql_step_refused(
  base_url, tmp_path, flowbench_binary, monkeypatch
):
  monkeypatch.setenv("FLOWBENCH_BIN", flowbench_binary)
  path = tmp_path / "g.flow.yaml"
  path.write_text(
    "flow: g\n"
    "steps:\n"
    "  - id: q\n"
    "    graphql:\n"
    "      url: /graphql\n"
    '      query: "{ ping }"\n'
  )
  loaded = Flow.load(str(path))
  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(loaded)

  with pytest.raises(FlowExecutionError, match="supports only call steps"):
    checkout.run(Profile(mode="integration"), base_url=base_url, store=str(tmp_path))


def test_use_loaded_yaml_flow_throttle_stays_throttled_not_failed(
  tmp_path, flowbench_binary, monkeypatch
):
  # A 429 from inside a used YAML flow has to come out the same way a 429 at
  # the top level does: throttled, not failed -- the use span's outcome is
  # the worst of its children, not hardcoded to failed (live.py's `use`).
  hits = {"n": 0}

  class _ThrottleHandler(BaseHTTPRequestHandler):
    def log_message(self, *args):
      pass

    def do_POST(self):
      hits["n"] += 1
      if hits["n"] == 1:
        self.send_response(429)
        self.send_header("Content-Length", "0")
        self.end_headers()
        return
      payload = json.dumps({"data": {"access_token": "tok-9"}}).encode()
      self.send_response(200)
      self.send_header("Content-Length", str(len(payload)))
      self.end_headers()
      self.wfile.write(payload)

  srv = ThreadingHTTPServer(("127.0.0.1", 0), _ThrottleHandler)
  thread = threading.Thread(target=srv.serve_forever, daemon=True)
  thread.start()
  try:
    url = f"http://127.0.0.1:{srv.server_address[1]}"
    loaded = _loaded_login_flow(tmp_path, flowbench_binary, monkeypatch)
    checkout = Flow("checkout")

    @checkout.step
    def auth(ctx):
      ctx.use(loaded, with_={"password": "pw"})

    checkout.run(Profile(mode="integration"), base_url=url, store=str(tmp_path))
  finally:
    srv.shutdown()
    thread.join()

  index = json.loads((tmp_path / "index.json").read_text())
  assert index[-1]["throttle_rate"] == 1.0
  assert index[-1]["error_rate"] == 0.0

  root = _traces(tmp_path)[0]
  assert root["Outcome"] == "throttled"
  assert root["Children"][0]["Name"] == "auth"
  assert root["Children"][0]["Outcome"] == "throttled"
