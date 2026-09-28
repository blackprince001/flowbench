import shutil
import subprocess
from pathlib import Path

import pytest

from flowbench import Flow, FlowCompileError

REPO_ROOT = Path(__file__).parents[2]

LOGIN_FLOW_YAML = """flow: login
inputs:
  email:
  password:
steps:
  - id: login
    call: POST /auth/login
    body: { email: "{{ inputs.email }}", password: "{{ inputs.password }}" }
    extract: { token: $.data.access_token }
    assert: [ status == 200 ]
outputs: [token]
"""


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


def test_load_reads_a_yaml_flow_s_ir(tmp_path, flowbench_binary, monkeypatch):
  monkeypatch.setenv("FLOWBENCH_BIN", flowbench_binary)
  path = tmp_path / "login.flow.yaml"
  path.write_text(LOGIN_FLOW_YAML)

  loaded = Flow.load(str(path))
  shape = loaded._compiled_flow_shape()
  assert shape["name"] == "login"
  assert shape["outputs"] == ["token"]
  assert [i["name"] for i in shape["inputs"]] == ["email", "password"]
  assert loaded._use_path() == str(path)


def test_use_embeds_a_loaded_yaml_flow(tmp_path, flowbench_binary, monkeypatch):
  monkeypatch.setenv("FLOWBENCH_BIN", flowbench_binary)
  path = tmp_path / "login.flow.yaml"
  path.write_text(LOGIN_FLOW_YAML)
  loaded = Flow.load(str(path))

  checkout = Flow("checkout")

  @checkout.step
  def auth(ctx):
    ctx.use(loaded, with_={"email": "a@b.com", "password": "pw"})

  ir = checkout.compile()
  use = ir["flows"][0]["steps"][0]["use"]
  assert use["path"] == str(path)
  assert use["flow"]["steps"][0]["call"]["url"] == "/auth/login"
  assert use["with"] == {"email": "a@b.com", "password": "pw"}


def test_load_missing_file_raises(flowbench_binary, monkeypatch):
  monkeypatch.setenv("FLOWBENCH_BIN", flowbench_binary)
  with pytest.raises(FlowCompileError, match="loading"):
    Flow.load("/nonexistent/nope.flow.yaml")


def test_load_invalid_flow_raises(tmp_path, flowbench_binary, monkeypatch):
  monkeypatch.setenv("FLOWBENCH_BIN", flowbench_binary)
  path = tmp_path / "bad.flow.yaml"
  path.write_text("flow: bad\nsteps: []\n")
  with pytest.raises(FlowCompileError, match="at least one step"):
    Flow.load(str(path))
