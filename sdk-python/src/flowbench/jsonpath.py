"""The JSONPath subset internal/eval/jsonpath.go supports: a leading "$", dot
keys ($.a.b), bracket keys ($['a.b']), and array indices ($.a[0], $.a[-1]).
Filters, wildcards, and recursive descent are intentionally unsupported --
this is a deliberate port of Go's restricted grammar, not a general JSONPath
library, so Python accepts exactly what the Go engine accepts, never more.
"""

import json

from .errors import FlowExecutionError


class _Key:
  __slots__ = ("key",)

  def __init__(self, key):
    self.key = key


class _Index:
  __slots__ = ("index",)

  def __init__(self, index):
    self.index = index


def _is_ident_byte(c):
  return c == "_" or c == "-" or c.isalnum()


def parse_path(path):
  if path == "" or path[0] != "$":
    raise FlowExecutionError(f"path {path!r} must start with $")

  segments = []
  i = 1
  n = len(path)
  while i < n:
    c = path[i]
    if c == ".":
      i += 1
      if i < n and path[i] == ".":
        raise FlowExecutionError(f"recursive descent (..) is not supported: {path!r}")
      start = i
      while i < n and _is_ident_byte(path[i]):
        i += 1
      if i == start:
        raise FlowExecutionError(f"empty key after '.' in {path!r}")
      segments.append(_Key(path[start:i]))
    elif c == "[":
      seg, i = _parse_bracket(path, i)
      segments.append(seg)
    else:
      raise FlowExecutionError(f"unexpected {c!r} at position {i} in {path!r}")
  return segments


def _parse_bracket(path, i):
  i += 1  # past '['
  n = len(path)
  if i < n and path[i] in ("'", '"'):
    quote = path[i]
    i += 1
    start = i
    while i < n and path[i] != quote:
      i += 1
    if i >= n:
      raise FlowExecutionError(f"unterminated quoted key in {path!r}")
    key = path[start:i]
    i += 1  # past closing quote
    if i >= n or path[i] != "]":
      raise FlowExecutionError(f"expected ] after quoted key in {path!r}")
    return _Key(key), i + 1

  start = i
  while i < n and path[i] != "]":
    i += 1
  if i >= n:
    raise FlowExecutionError(f"unterminated [ in {path!r}")
  raw = path[start:i].strip()
  try:
    index = int(raw)
  except ValueError:
    raise FlowExecutionError(
      f"array index {raw!r} in {path!r} is not an integer"
    ) from None
  return _Index(index), i + 1


def query_json(body, path):
  """Returns (value, found). Raises FlowExecutionError on invalid JSON or a
  malformed path. found is False when the body is valid JSON but the path
  names nothing -- mirrors internal/eval/jsonpath.go's queryJSON exactly.
  """
  segments = parse_path(path)
  if not body:
    return None, False

  try:
    cur = json.loads(body)
  except (json.JSONDecodeError, UnicodeDecodeError) as e:
    raise FlowExecutionError(f"response body is not JSON: {e}") from e

  for seg in segments:
    if isinstance(seg, _Key):
      if not isinstance(cur, dict) or seg.key not in cur:
        return None, False
      cur = cur[seg.key]
    else:
      if not isinstance(cur, list):
        return None, False
      idx = seg.index
      if idx < 0:
        idx += len(cur)
      if idx < 0 or idx >= len(cur):
        return None, False
      cur = cur[idx]
  return cur, True
