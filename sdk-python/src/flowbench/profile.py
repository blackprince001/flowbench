import re
from dataclasses import dataclass

from .errors import FlowCompileError

_MODES = {"integration", "system", "load", "stress", "soak"}

# Go's time.ParseDuration grammar: one or more signed decimal number+unit
# pairs, e.g. "10m", "1h30m", "300ms". This is a friendly early check, not a
# full parity implementation -- ir.Duration re-normalizes on decode anyway.
_DURATION_RE = re.compile(r"^-?(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$")
_ARRIVAL_CAP_RE = re.compile(r"^\d+/.+$")

# Native planner grammar: "0 -> 500 over 5m".
_NATIVE_RAMP_RE = re.compile(r"^\s*\S+\s*->\s*\S+\s+over\s+\S+\s*$")
# PRD's alternate spelling: "ramp(0 -> 500, 5m)".
_RAMP_FN_RE = re.compile(r"^\s*ramp\(\s*(\S+)\s*->\s*(\S+)\s*,\s*(\S+)\s*\)\s*$")


def _check_duration(value, field):
  if value is None:
    return
  if not _DURATION_RE.match(value):
    raise FlowCompileError(
      f'Profile.{field} {value!r} is not a valid duration (e.g. "10m", "30s")'
    )


def _translate_ramp(value):
  if value is None:
    return None
  m = _RAMP_FN_RE.match(value)
  if m:
    start, end, duration = m.groups()
    return f"{start} -> {end} over {duration}"
  if _NATIVE_RAMP_RE.match(value):
    return value
  raise FlowCompileError(
    f'Profile.ramp {value!r} must look like "0 -> 500 over 5m" or "ramp(0 -> 500, 5m)"'
  )


@dataclass
class Profile:
  mode: str
  vus: str | int | None = None
  ramp: str | None = None
  hold: str | None = None
  iterations: int | None = None
  arrival_cap: str | None = None
  thresholds: list | None = None

  def __post_init__(self):
    if self.mode not in _MODES:
      raise FlowCompileError(
        f"Profile.mode must be one of {sorted(_MODES)!r}, got {self.mode!r}"
      )

    # The PRD spells a ramp profile as vus="ramp(0 -> 500, 5m)"; the IR
    # wants it split across `vus` (peak count, unused for ramps) and
    # `ramp` (the "start -> end over duration" string). Accept the
    # PRD form on either field and translate it onto `ramp`.
    if isinstance(self.vus, str) and (
      self.vus.strip().startswith("ramp(") or "->" in self.vus
    ):
      self.ramp = self.vus
      self.vus = None
    if self.ramp is not None:
      self.ramp = _translate_ramp(self.ramp)

    _check_duration(self.hold, "hold")
    if self.arrival_cap is not None and not _ARRIVAL_CAP_RE.match(self.arrival_cap):
      raise FlowCompileError(
        f'Profile.arrival_cap {self.arrival_cap!r} must look like "300/s"'
      )

  def to_ir(self):
    ir = {"mode": self.mode}
    if self.vus:
      ir["vus"] = self.vus
    if self.ramp:
      ir["ramp"] = self.ramp
    if self.hold:
      ir["hold"] = self.hold
    if self.iterations:
      ir["iterations"] = self.iterations
    if self.arrival_cap:
      ir["arrival_cap"] = self.arrival_cap
    if self.thresholds:
      ir["thresholds"] = list(self.thresholds)
    return ir
