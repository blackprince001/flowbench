import pytest

from flowbench.redaction import SecretSet
from flowbench.span import Span, finalize


def test_child_appends_and_returns():
  root = Span("step", 0.0)
  c = root.child("http_call", 0.1)
  assert root.children == [c]
  assert c.name == "http_call"
  assert c.start == 0.1


def test_self_time_subtracts_children():
  root = Span("step", 0.0)
  root.duration = 1.0
  root.child("a", 0.0).duration = 0.3
  root.child("b", 0.3).duration = 0.2
  assert root.self_time() == pytest.approx(0.5)


def test_self_time_floors_at_zero():
  root = Span("step", 0.0)
  root.duration = 0.1
  root.child("a", 0.0).duration = 1.0
  assert root.self_time() == 0.0


def test_to_dict_shape_and_ns_conversion():
  root = Span("login", 0.0)
  root.duration = 0.25
  d = root.to_dict()
  assert d == {
    "Name": "login",
    "Start": 0,
    "Duration": 250_000_000,
    "Outcome": "ok",
    "Children": [],
  }


def test_to_dict_includes_payload_only_when_set():
  root = Span("login", 0.0)
  assert "Payload" not in root.to_dict()
  root.payload = {"status": 200}
  assert root.to_dict()["Payload"] == {"status": 200}


def test_to_dict_nests_children():
  root = Span("login", 0.0)
  root.child("assert_status", 0.1)
  d = root.to_dict()
  assert len(d["Children"]) == 1
  assert d["Children"][0]["Name"] == "assert_status"


def test_finalize_builds_payload_from_call_and_raw_bodies():
  sp = Span("login", 0.0)
  sp.set_call("POST", "/auth/login", 200, "")
  sp.set_raw(b'{"email":"a"}', b'{"token":"x"}')
  finalize(sp, SecretSet(), 2048)
  assert sp.payload == {
    "method": "POST",
    "status": 200,
    "req_bytes": 13,
    "resp_bytes": 13,
    "url": "/auth/login",
    "request": '{"email":"a"}',
    "response": '{"token":"x"}',
  }
  assert sp._raw_req is None
  assert sp._raw_resp is None


def test_finalize_omits_empty_fields():
  sp = Span("login", 0.0)
  sp.set_call("POST", "/x", 200, "")
  finalize(sp, SecretSet(), 2048)
  assert "retry_after" not in sp.payload
  assert "request" not in sp.payload
  assert "response" not in sp.payload
  assert "truncated" not in sp.payload


def test_finalize_skips_span_with_nothing_captured():
  sp = Span("wait", 0.0)
  finalize(sp, SecretSet(), 2048)
  assert sp.payload is None


def test_finalize_redacts_before_truncating():
  secrets = SecretSet()
  secrets.add("hunter2")
  sp = Span("login", 0.0)
  sp.set_call("POST", "/x?token=hunter2", 200, "")
  sp.set_raw(b"body with hunter2 in it", b"resp with hunter2 too")
  finalize(sp, secrets, 2048)
  assert "hunter2" not in sp.payload["url"]
  assert "hunter2" not in sp.payload["request"]
  assert "hunter2" not in sp.payload["response"]


def test_finalize_truncates_at_max_bytes():
  sp = Span("login", 0.0)
  sp.set_call("POST", "/x", 200, "")
  sp.set_raw(b"x" * 100, None)
  finalize(sp, SecretSet(), 10)
  assert len(sp.payload["request"]) == 10
  assert sp.payload["truncated"] is True


def test_finalize_negative_max_bytes_captures_no_bodies():
  sp = Span("login", 0.0)
  sp.set_call("POST", "/x", 200, "")
  sp.set_raw(b"body", b"resp")
  finalize(sp, SecretSet(), -1)
  assert "request" not in sp.payload
  assert "response" not in sp.payload


def test_finalize_records_failure():
  sp = Span("assert_status", 0.0)
  sp.set_failure("status: 500 eq 200")
  finalize(sp, SecretSet(), 2048)
  assert sp.payload["failure"] == "status: 500 eq 200"


def test_finalize_recurses_into_children():
  root = Span("login", 0.0)
  child = root.child("assert_status", 0.1)
  child.set_failure("boom")
  finalize(root, SecretSet(), 2048)
  assert child.payload["failure"] == "boom"
