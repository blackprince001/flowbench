"""Target resolution for live execution.

Not a port of internal/target/target.go's YAML parsing -- Python shells out
to the `flowbench target <name>` subcommand (single source of truth for
target-file parsing) and gets back the resolved config as JSON. Only the
origin-matching allow-list check (Target.Allows) is reimplemented here,
since that's a per-call runtime decision, not a parsing concern.
"""

import json
import os
import shutil
import subprocess
from urllib.parse import urlparse

from .errors import FlowExecutionError


class TargetConfig:
  def __init__(
    self,
    name,
    base_urls,
    max_vus=0,
    max_rps=0,
    agent_addr="",
    disallowed_modes=None,
  ):
    self.name = name
    self.base_urls = base_urls
    self.max_vus = max_vus
    self.max_rps = max_rps
    self.agent_addr = agent_addr
    self.disallowed_modes = disallowed_modes or []

  @property
  def base_url(self):
    return self.base_urls[0].rstrip("/")

  @classmethod
  def from_json(cls, data):
    return cls(
      name=data["name"],
      base_urls=data["base_urls"],
      max_vus=data.get("max_vus", 0),
      max_rps=data.get("max_rps", 0),
      agent_addr=data.get("agent_addr", ""),
      disallowed_modes=data.get("disallowed_modes", []),
    )


def _find_binary():
  binary = os.environ.get("FLOWBENCH_BIN")
  if binary:
    return binary
  found = shutil.which("flowbench")
  if found:
    return found
  raise FlowExecutionError(
    "no flowbench binary found (set $FLOWBENCH_BIN, build one with "
    "`go build -o flowbench ./cmd/flowbench`, or pass base_url= directly "
    "to flow.run() to skip target resolution)"
  )


def resolve_target_via_binary(name, targets_dir="targets"):
  binary = _find_binary()
  proc = subprocess.run(
    [binary, "target", name, "--targets-dir", targets_dir],
    capture_output=True,
    text=True,
    check=False,
  )
  if proc.returncode != 0:
    raise FlowExecutionError(f"resolving target {name!r}: {proc.stderr.strip()}")
  return TargetConfig.from_json(json.loads(proc.stdout))


def _origins(cfg):
  origins = set()
  for raw in cfg.base_urls:
    u = urlparse(raw)
    origins.add(f"{u.scheme.lower()}://{u.netloc.lower()}")
  return origins


def allows(cfg, url):
  """Mirrors internal/target/target.go's Target.Allows: a relative URL is
  always allowed (it resolves against the base URL); an absolute URL must
  match one of the target's declared origins.
  """
  u = urlparse(url)
  if not u.scheme and not u.netloc:
    return True
  return f"{u.scheme.lower()}://{u.netloc.lower()}" in _origins(cfg)
