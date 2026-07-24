from dataclasses import dataclass

from .errors import FlowCompileError

_BACKOFFS = {"fixed", "exponential", "honor_retry_after"}


@dataclass
class Retry:
  on_status: list
  backoff: str
  max_attempts: int
  base_delay: str | None = None

  def __post_init__(self):
    if self.backoff not in _BACKOFFS:
      raise FlowCompileError(
        f"Retry.backoff must be one of {sorted(_BACKOFFS)!r}, got {self.backoff!r}"
      )
    if self.max_attempts < 1:
      raise FlowCompileError("Retry.max_attempts must be >= 1")
    for status in self.on_status:
      if not (100 <= status <= 599):
        raise FlowCompileError(
          f"Retry.on_status entries must be in [100, 599], got {status!r}"
        )

  def to_ir(self):
    ir = {
      "on_status": list(self.on_status),
      "backoff": self.backoff,
      "max_attempts": self.max_attempts,
    }
    if self.base_delay:
      ir["base_delay"] = self.base_delay
    return ir
