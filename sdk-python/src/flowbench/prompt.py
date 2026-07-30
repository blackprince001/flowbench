"""Prompt observation, the parts that do not touch execution (issue #43,
ADR 0009).

FlowBench never makes the LLM call. The flow's own code calls whatever SDK it
already uses, and `with ctx.prompt(...) as p:` wraps that call so the exchange
is recorded: identity hashing, token counts, and the client-side pace ceiling
that keeps a run over 500 fixture rows from tripping the provider's rate limit
in the first place. This module holds those pieces, which are pure functions
over values; the wrapper that emits the span lives in drivers/live.py with the
rest of live execution.

Pace state is module-level on purpose. A LiveDriver lives for one iteration,
and a ceiling that reset every iteration would not be a ceiling -- the guard
has to remember what the last iteration already spent.
"""

import hashlib
import json
import re
import threading
import time

from .errors import FlowCompileError, FlowExecutionError
from .retry_exec import parse_duration_seconds

# "20/m", "5/s", "100/h" -- the arrival cap's grammar (PRD 10.3) at the scale
# of one observation.
_PACE_RE = re.compile(r"^\s*(\d+(?:\.\d+)?)\s*/\s*(\d+)?\s*(s|m|h)\s*$")

_PER_SECOND = {"s": 1.0, "m": 60.0, "h": 3600.0}

_buckets = {}
_lock = threading.Lock()


def parse_pace(spec, burst):
  """`"20/m"` plus a burst allowance into (tokens per second, capacity)."""
  m = _PACE_RE.match(spec)
  if m is None:
    raise FlowCompileError(
      f'pace {spec!r} must look like "20/m" -- a count over s, m or h'
    )
  count, per, unit = float(m.group(1)), float(m.group(2) or 1), m.group(3)
  window = per * _PER_SECOND[unit]
  if count <= 0 or window <= 0:
    raise FlowCompileError(f"pace {spec!r} must allow at least one call")
  if burst is None:
    burst = 1
  if not isinstance(burst, int) or isinstance(burst, bool) or burst < 1:
    raise FlowCompileError(f"burst must be a positive integer, got {burst!r}")
  return count / window, float(burst)


def wait_for_pace(key, spec, burst=None, *, sleep=time.sleep, clock=time.monotonic):
  """Blocks until the ceiling declared for `key` allows another call, and
  returns how long that took (0.0 when it did not have to wait).

  A token bucket, so `burst=` really means the first N calls run unspaced. The
  wait is charged to the bucket before it is slept, which is what makes two
  callers queue behind each other rather than both waking at the same instant.

  Keyed by the observation's *name* rather than its span identity: two variants
  of one prompt hit the same provider, so they share the ceiling declared for
  it. A later call declaring a different pace re-rates the bucket in place --
  last declaration wins, rather than silently keeping the first.
  """
  rate, capacity = parse_pace(spec, burst)
  with _lock:
    now = clock()
    bucket = _buckets.get(key)
    if bucket is None:
      bucket = _buckets[key] = _Bucket(rate, capacity, now)
    bucket.rerate(rate, capacity)
    wait = bucket.take(now)
  if wait > 0:
    sleep(wait)
  return wait


def reset_pacing():
  """Forgets every bucket. For tests, and for a process running several runs
  that should each start with a full allowance.
  """
  with _lock:
    _buckets.clear()


class _Bucket:
  def __init__(self, rate, capacity, now):
    self.rate = rate
    self.capacity = capacity
    self.tokens = capacity
    self.at = now

  def rerate(self, rate, capacity):
    self.rate = rate
    self.capacity = capacity
    self.tokens = min(self.tokens, capacity)

  def take(self, now):
    self.tokens = min(self.capacity, self.tokens + (now - self.at) * self.rate)
    self.at = now
    if self.tokens >= 1:
      self.tokens -= 1
      return 0.0
    wait = (1 - self.tokens) / self.rate
    self.tokens = 0.0
    self.at = now + wait
    return wait


def hash_prompt(template, rendered):
  """The observation's identity: the author-supplied template when there is
  one -- stable across iterations whose variable values differ -- else the
  prompt as it was actually recorded. So the diff view (#45) can tell "the
  prompt changed" from "the output changed under the same prompt".
  """
  basis = rendered if template is None else render(template)
  return hashlib.sha256(basis.encode("utf-8")).hexdigest()


def render(value):
  """The text a diff is taken over. A string passes through; anything else --
  a chat message list, a provider's response object -- is JSON, so the
  structural diff has something to parse.
  """
  if isinstance(value, str):
    return value
  if isinstance(value, bytes):
    return value.decode("utf-8", errors="replace")
  return json.dumps(value, default=_jsonable)


def _jsonable(value):
  for attr in ("model_dump", "dict", "to_dict"):
    method = getattr(value, attr, None)
    if callable(method):
      try:
        return method()
      except TypeError:
        pass
  if hasattr(value, "__dict__"):
    return {k: v for k, v in vars(value).items() if not k.startswith("_")}
  return str(value)


# One vocabulary for token counts, whatever the provider calls them: OpenAI
# says prompt/completion, Anthropic says input/output, and a run store that
# stored both would make the per-observation table provider-specific.
_USAGE_ALIASES = {
  "prompt_tokens": ("prompt_tokens", "input_tokens"),
  "completion_tokens": ("completion_tokens", "output_tokens"),
  "total_tokens": ("total_tokens",),
}


def normalize_usage(usage):
  """A provider's usage object or mapping reduced to prompt/completion/total
  token counts. Returns None for None; raises when handed something no count
  can be read out of, because a silently dropped count is worse than a loud
  one.
  """
  if usage is None:
    return None
  counts = {}
  for field, aliases in _USAGE_ALIASES.items():
    for alias in aliases:
      value = _read(usage, alias)
      if value is not None:
        counts[field] = int(value)
        break
  if not counts:
    raise FlowExecutionError(
      "record(usage=...) takes a mapping or an object carrying token counts "
      "(prompt_tokens/completion_tokens, or input_tokens/output_tokens), "
      f"got {type(usage).__name__}"
    )
  if "total_tokens" not in counts:
    counts["total_tokens"] = counts.get("prompt_tokens", 0) + counts.get(
      "completion_tokens", 0
    )
  return counts


def _read(usage, key):
  if isinstance(usage, dict):
    return usage.get(key)
  return getattr(usage, key, None)


def parse_timeout(value):
  """Seconds as a number, or a Go-style duration string like "30s"."""
  if isinstance(value, bool):
    raise FlowCompileError(f"timeout must be seconds or a duration, got {value!r}")
  if isinstance(value, (int, float)):
    return float(value)
  if isinstance(value, str):
    return parse_duration_seconds(value)
  raise FlowCompileError(
    f'timeout must be seconds (10) or a duration ("10s"), got {type(value).__name__}'
  )
