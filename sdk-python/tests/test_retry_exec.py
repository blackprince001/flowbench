import pytest

from flowbench.errors import FlowExecutionError
from flowbench.retry_exec import (
  DEFAULT_BACKOFF,
  MAX_BACKOFF,
  backoff_delay,
  base_delay,
  clamp_backoff,
  parse_duration_seconds,
  retry_after_seconds,
  retryable,
)


def test_parse_duration_simple():
  assert parse_duration_seconds("200ms") == pytest.approx(0.2)
  assert parse_duration_seconds("1s") == pytest.approx(1.0)
  assert parse_duration_seconds("2m") == pytest.approx(120.0)


def test_parse_duration_compound():
  assert parse_duration_seconds("1h30m") == pytest.approx(5400.0)


def test_parse_duration_invalid():
  with pytest.raises(FlowExecutionError):
    parse_duration_seconds("not-a-duration")


def test_retryable():
  policy = {"on_status": [429, 503], "backoff": "fixed", "max_attempts": 3}
  assert retryable(policy, 429) is True
  assert retryable(policy, 500) is False


def test_base_delay_default_when_unset():
  policy = {"on_status": [429], "backoff": "fixed", "max_attempts": 3}
  assert base_delay(policy) == DEFAULT_BACKOFF


def test_base_delay_from_policy():
  policy = {
    "on_status": [429],
    "backoff": "fixed",
    "max_attempts": 3,
    "base_delay": "500ms",
  }
  assert base_delay(policy) == pytest.approx(0.5)


def test_clamp_backoff_caps_at_max():
  assert clamp_backoff(MAX_BACKOFF + 10) == MAX_BACKOFF


def test_clamp_backoff_negative_becomes_max():
  assert clamp_backoff(-1) == MAX_BACKOFF


def test_retry_after_seconds_from_header():
  assert retry_after_seconds({"Retry-After": "5"}) == 5


def test_retry_after_seconds_negative_clamped_to_zero():
  assert retry_after_seconds({"Retry-After": "-5"}) == 0


def test_retry_after_seconds_missing():
  assert retry_after_seconds({}) is None
  assert retry_after_seconds(None) is None


def test_retry_after_seconds_http_date():
  from datetime import datetime, timedelta, timezone
  from email.utils import format_datetime

  future = datetime.now(timezone.utc) + timedelta(seconds=10)
  header_value = format_datetime(future, usegmt=True)
  delta = retry_after_seconds({"Retry-After": header_value})
  assert delta is not None
  assert 8 <= delta <= 11


def test_backoff_delay_fixed():
  policy = {
    "on_status": [429],
    "backoff": "fixed",
    "max_attempts": 3,
    "base_delay": "300ms",
  }
  assert backoff_delay(policy, 1, {}) == pytest.approx(0.3)
  assert backoff_delay(policy, 5, {}) == pytest.approx(0.3)


def test_backoff_delay_exponential():
  policy = {
    "on_status": [429],
    "backoff": "exponential",
    "max_attempts": 5,
    "base_delay": "100ms",
  }
  assert backoff_delay(policy, 1, {}) == pytest.approx(0.1)
  assert backoff_delay(policy, 2, {}) == pytest.approx(0.2)
  assert backoff_delay(policy, 3, {}) == pytest.approx(0.4)


def test_backoff_delay_exponential_caps_shift_at_16():
  policy = {
    "on_status": [429],
    "backoff": "exponential",
    "max_attempts": 30,
    "base_delay": "1s",
  }
  assert backoff_delay(policy, 30, {}) == MAX_BACKOFF


def test_backoff_delay_honor_retry_after_present():
  policy = {"on_status": [429], "backoff": "honor_retry_after", "max_attempts": 3}
  assert backoff_delay(policy, 1, {"Retry-After": "2"}) == 2


def test_backoff_delay_honor_retry_after_absent_falls_back():
  policy = {
    "on_status": [429],
    "backoff": "honor_retry_after",
    "max_attempts": 3,
    "base_delay": "50ms",
  }
  assert backoff_delay(policy, 1, {}) == pytest.approx(0.05)
