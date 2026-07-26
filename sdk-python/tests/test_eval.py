import pytest

from flowbench.errors import FlowExecutionError
from flowbench.eval import compare, contains, numeric_compare, to_float, values_equal


def test_status_eq_ok():
  assert compare("eq", 200, 200) is True


def test_status_eq_bad():
  assert compare("eq", 200, 500) is False


def test_status_lt():
  assert compare("lt", 200, 300) is True


def test_ne():
  assert compare("ne", 200, 500) is True
  assert compare("ne", 200, 200) is False


def test_cross_type_numeric_equality():
  assert values_equal(200, 200.0) is True


def test_bool_not_numeric():
  assert to_float(True) == (None, False)
  assert values_equal(True, 1) is False


def test_numeric_ops_need_numeric_operands():
  with pytest.raises(FlowExecutionError, match="needs numeric operands"):
    compare("lt", "a", "b")


def test_contains_string():
  assert contains("hello world", "world") is True
  assert contains("hello world", "nope") is False


def test_contains_list():
  assert contains([1, 2, 3], 2) is True
  assert contains([1, 2, 3], 9) is False


def test_contains_needs_string_or_array():
  with pytest.raises(FlowExecutionError, match="string or array"):
    contains(42, 1)


def test_matches_regex():
  assert compare("matches", "order-123", r"^order-\d+$") is True
  assert compare("matches", "nope", r"^order-\d+$") is False


def test_matches_needs_strings():
  with pytest.raises(FlowExecutionError, match="needs string operands"):
    compare("matches", 42, "x")


def test_matches_invalid_pattern():
  with pytest.raises(FlowExecutionError, match="is invalid"):
    compare("matches", "x", "(")


def test_unsupported_operator():
  with pytest.raises(FlowExecutionError, match="unsupported operator"):
    compare("bogus", 1, 1)


def test_numeric_compare_ops():
  assert numeric_compare("lte", 5.0, 5.0) is True
  assert numeric_compare("gt", 6.0, 5.0) is True
  assert numeric_compare("gte", 5.0, 5.0) is True
