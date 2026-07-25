import json

import pytest

from flowbench.errors import FlowExecutionError
from flowbench.jsonpath import query_json

BODY = json.dumps(
  {"data": {"access_token": "tok-9", "items": [{"id": "a"}, {"id": "b"}]}}
).encode()


def test_nested_key():
  assert query_json(BODY, "$.data.access_token") == ("tok-9", True)


def test_array_index():
  assert query_json(BODY, "$.data.items[1].id") == ("b", True)


def test_negative_array_index():
  assert query_json(BODY, "$.data.items[-1].id") == ("b", True)


def test_bracket_key():
  assert query_json(BODY, "$['data']['access_token']") == ("tok-9", True)


def test_missing_key_not_found():
  assert query_json(BODY, "$.data.nope") == (None, False)


def test_index_out_of_bounds_not_found():
  assert query_json(BODY, "$.data.items[9].id") == (None, False)


def test_whole_root():
  value, found = query_json(BODY, "$")
  assert found is True
  assert value == json.loads(BODY)


def test_empty_body_not_found():
  assert query_json(b"", "$.data") == (None, False)


def test_path_must_start_with_dollar():
  with pytest.raises(FlowExecutionError, match=r"must start with \$"):
    query_json(BODY, "data.access_token")


def test_recursive_descent_rejected():
  with pytest.raises(FlowExecutionError, match="recursive descent"):
    query_json(BODY, "$..access_token")


def test_non_json_body_raises():
  with pytest.raises(FlowExecutionError, match="not JSON"):
    query_json(b"not json", "$.data")


def test_non_integer_index_rejected():
  with pytest.raises(FlowExecutionError, match="not an integer"):
    query_json(BODY, "$.data.items[x]")
