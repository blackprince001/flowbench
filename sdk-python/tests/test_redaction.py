from flowbench.redaction import PLACEHOLDER, SecretSet


def test_redacts_registered_value():
  s = SecretSet()
  s.add("hunter2")
  assert s.redact("token=hunter2") == f"token={PLACEHOLDER}"


def test_empty_value_is_noop():
  s = SecretSet()
  s.add("")
  assert s.redact("hunter2") == "hunter2"


def test_no_secrets_is_identity():
  s = SecretSet()
  assert s.redact("plain text") == "plain text"


def test_longest_first_avoids_partial_leak():
  s = SecretSet()
  s.add("abc")
  s.add("abcdef")
  assert s.redact("abcdef") == PLACEHOLDER


def test_contains():
  s = SecretSet()
  s.add("hunter2")
  assert s.contains("hunter2")
  assert not s.contains("other")


def test_redact_bytes():
  s = SecretSet()
  s.add("hunter2")
  assert s.redact_bytes(b"token=hunter2") == f"token={PLACEHOLDER}".encode()


def test_redacts_every_occurrence():
  s = SecretSet()
  s.add("x")
  assert s.redact("x-x-x") == f"{PLACEHOLDER}-{PLACEHOLDER}-{PLACEHOLDER}"
