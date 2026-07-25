"""Real retry/backoff execution for live calls -- a port of
internal/executor/retry.go's attempt/backoff loop, so a Python-driven flow's
retry timing matches the Go engine's numbers exactly. Operates on the same
dict shape Retry.to_ir() produces (on_status, backoff, max_attempts,
base_delay) -- no separate policy representation.
"""

import re
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime

from .errors import FlowExecutionError

# Paces fixed/exponential retries whose policy sets no base_delay, and
# honor_retry_after responses with no Retry-After, so a retry loop never
# becomes a hot loop hammering the target.
DEFAULT_BACKOFF = 0.1  # 100ms, matches Go's defaultBackoff

# Caps a single computed backoff so a large attempt count cannot wait
# absurdly long.
MAX_BACKOFF = 120.0  # 2m, matches Go's maxBackoff

_UNIT_SECONDS = {
  "ns": 1e-9,
  "us": 1e-6,
  "µs": 1e-6,
  "ms": 1e-3,
  "s": 1.0,
  "m": 60.0,
  "h": 3600.0,
}
_DURATION_TOKEN_RE = re.compile(r"(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)")


def parse_duration_seconds(s):
  negative = s.startswith("-")
  body = s[1:] if negative else s
  total = 0.0
  pos = 0
  for m in _DURATION_TOKEN_RE.finditer(body):
    if m.start() != pos:
      break
    total += float(m.group(1)) * _UNIT_SECONDS[m.group(2)]
    pos = m.end()
  if pos != len(body) or pos == 0:
    raise FlowExecutionError(f"invalid duration {s!r}")
  return -total if negative else total


def retryable(policy, status):
  return status in policy["on_status"]


def base_delay(policy):
  raw = policy.get("base_delay")
  if raw:
    return parse_duration_seconds(raw)
  return DEFAULT_BACKOFF


def clamp_backoff(seconds):
  if seconds < 0 or seconds > MAX_BACKOFF:
    return MAX_BACKOFF
  return seconds


def retry_after_seconds(headers):
  """Reads Retry-After as delta-seconds or an HTTP date, mirroring
  retry.go's retryAfter. Returns None when absent or unparseable.
  """
  if headers is None:
    return None
  value = headers.get("Retry-After")
  if not value:
    return None
  try:
    secs = int(value)
    return max(secs, 0)
  except ValueError:
    pass
  try:
    dt = parsedate_to_datetime(value)
  except (TypeError, ValueError, IndexError):
    return None
  if dt is None:
    return None
  if dt.tzinfo is None:
    dt = dt.replace(tzinfo=timezone.utc)
  delta = (dt - datetime.now(timezone.utc)).total_seconds()
  return max(delta, 0)


def backoff_delay(policy, attempt, response_headers):
  backoff = policy.get("backoff", "fixed")
  if backoff == "honor_retry_after":
    delay = retry_after_seconds(response_headers)
    if delay is not None:
      return clamp_backoff(delay)
    return base_delay(policy)
  if backoff == "exponential":
    shift = min(attempt - 1, 16)
    return clamp_backoff(base_delay(policy) * (2**shift))
  return base_delay(policy)  # fixed
