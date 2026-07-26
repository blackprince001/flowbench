import shutil
import subprocess
from pathlib import Path

import pytest

from flowbench.errors import FlowExecutionError
from flowbench.target import TargetConfig, allows, resolve_target_via_binary

REPO_ROOT = Path(__file__).parents[2]


def test_from_json_defaults():
  cfg = TargetConfig.from_json(
    {"name": "local", "base_urls": ["http://localhost:8080"]}
  )
  assert cfg.name == "local"
  assert cfg.max_vus == 0
  assert cfg.disallowed_modes == []


def test_base_url_strips_trailing_slash():
  cfg = TargetConfig(name="t", base_urls=["http://localhost:8080/"])
  assert cfg.base_url == "http://localhost:8080"


def test_allows_relative_url():
  cfg = TargetConfig(name="t", base_urls=["http://localhost:8080"])
  assert allows(cfg, "/auth/login") is True


def test_allows_matching_origin():
  cfg = TargetConfig(name="t", base_urls=["http://localhost:8080"])
  assert allows(cfg, "http://localhost:8080/orders") is True


def test_allows_rejects_other_origin():
  cfg = TargetConfig(name="t", base_urls=["http://localhost:8080"])
  assert allows(cfg, "http://evil.example.com/x") is False


def test_allows_case_insensitive():
  cfg = TargetConfig(name="t", base_urls=["http://LocalHost:8080"])
  assert allows(cfg, "HTTP://localhost:8080/x") is True


def test_allows_multiple_base_urls():
  cfg = TargetConfig(name="t", base_urls=["http://a.test", "http://b.test"])
  assert allows(cfg, "http://b.test/x") is True
  assert allows(cfg, "http://c.test/x") is False


def test_resolve_via_binary_missing_raises(monkeypatch):
  monkeypatch.delenv("FLOWBENCH_BIN", raising=False)
  monkeypatch.setattr(shutil, "which", lambda name: None)
  with pytest.raises(FlowExecutionError, match="no flowbench binary found"):
    resolve_target_via_binary("local")


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


def test_resolve_via_binary_real(flowbench_binary, monkeypatch):
  monkeypatch.setenv("FLOWBENCH_BIN", flowbench_binary)
  cfg = resolve_target_via_binary("local", str(REPO_ROOT / "tests" / "targets"))
  assert cfg.name == "local"
  assert cfg.base_urls == ["http://localhost:8080"]
  assert cfg.max_vus == 200


def test_resolve_via_binary_unknown_target_raises(flowbench_binary, monkeypatch):
  monkeypatch.setenv("FLOWBENCH_BIN", flowbench_binary)
  with pytest.raises(FlowExecutionError, match="nope"):
    resolve_target_via_binary("nope", str(REPO_ROOT / "tests" / "targets"))
