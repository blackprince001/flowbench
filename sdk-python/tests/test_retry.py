import pytest

from flowbench import FlowCompileError, Retry


def test_valid_retry_to_ir():
  r = Retry(on_status=[429, 503], backoff="honor_retry_after", max_attempts=5)
  assert r.to_ir() == {
    "on_status": [429, 503],
    "backoff": "honor_retry_after",
    "max_attempts": 5,
  }


def test_base_delay_included_when_set():
  r = Retry(on_status=[429], backoff="fixed", max_attempts=3, base_delay="200ms")
  assert r.to_ir()["base_delay"] == "200ms"


def test_base_delay_omitted_when_unset():
  r = Retry(on_status=[429], backoff="fixed", max_attempts=3)
  assert "base_delay" not in r.to_ir()


def test_invalid_backoff_rejected():
  with pytest.raises(FlowCompileError, match="backoff"):
    Retry(on_status=[429], backoff="bogus", max_attempts=3)


def test_max_attempts_must_be_positive():
  with pytest.raises(FlowCompileError, match="max_attempts"):
    Retry(on_status=[429], backoff="fixed", max_attempts=0)


def test_status_out_of_range_rejected():
  with pytest.raises(FlowCompileError, match="on_status"):
    Retry(on_status=[999], backoff="fixed", max_attempts=1)
