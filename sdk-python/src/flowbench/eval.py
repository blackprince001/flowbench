"""Real assertion comparisons for live execution -- a port of
internal/eval/assert.go's compare/numericCompare/contains/valuesEqual, so a
Python-driven flow's expect(...) evaluates the same op semantics as the Go
engine's assert.go does for the compiled IR path.
"""

import re

from .errors import FlowExecutionError

_NUMERIC_OPS = {"lt", "lte", "gt", "gte"}


def to_float(v):
  """Mirrors Go's toFloat: bool is deliberately excluded (Go's toFloat only
  accepts numeric kinds, not booleans), so eq/ne on bools falls through to
  values_equal's identity comparison instead of numeric comparison.
  """
  if isinstance(v, bool):
    return None, False
  if isinstance(v, (int, float)):
    return float(v), True
  return None, False


def values_equal(a, b):
  af, aok = to_float(a)
  if aok:
    bf, bok = to_float(b)
    if bok:
      return af == bf
    return False
  # Python's True == 1 is permissive in a way Go's reflect.DeepEqual (the
  # non-numeric fallback comparator) is not -- a bool and a non-bool never
  # match here, mirroring Go's type-sensitive equality.
  if isinstance(a, bool) != isinstance(b, bool):
    return False
  return a == b


def numeric_compare(op, a, b):
  if op == "lt":
    return a < b
  if op == "lte":
    return a <= b
  if op == "gt":
    return a > b
  if op == "gte":
    return a >= b
  raise FlowExecutionError(f"{op!r} is not a numeric operator")


def contains(actual, expected):
  if isinstance(actual, str):
    if not isinstance(expected, str):
      raise FlowExecutionError("contains on a string needs a string operand")
    return expected in actual
  if isinstance(actual, list):
    return any(values_equal(el, expected) for el in actual)
  raise FlowExecutionError(
    f"contains needs a string or array subject, got {type(actual).__name__}"
  )


def compare(op, actual, expected):
  if op == "eq":
    return values_equal(actual, expected)
  if op == "ne":
    return not values_equal(actual, expected)
  if op in _NUMERIC_OPS:
    af, aok = to_float(actual)
    ef, eok = to_float(expected)
    if not aok or not eok:
      raise FlowExecutionError(
        f"{op} needs numeric operands, got {actual!r} and {expected!r}"
      )
    return numeric_compare(op, af, ef)
  if op == "contains":
    return contains(actual, expected)
  if op == "matches":
    if not isinstance(actual, str) or not isinstance(expected, str):
      raise FlowExecutionError(
        f"matches needs string operands, got {type(actual).__name__} "
        f"and {type(expected).__name__}"
      )
    try:
      pattern = re.compile(expected)
    except re.error as e:
      raise FlowExecutionError(f"matches pattern {expected!r} is invalid: {e}") from e
    return pattern.search(actual) is not None
  raise FlowExecutionError(f"unsupported operator {op!r}")
