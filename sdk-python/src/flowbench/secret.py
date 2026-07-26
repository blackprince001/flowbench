"""Tracks values that must never reach a stored run artifact.

Mirrors internal/secret/secret.go: every value resolved from ctx.env during
live execution is registered here, and every failure detail, span payload,
and meta field is scrubbed through it before anything is written.
"""

PLACEHOLDER = "[redacted]"


class SecretSet:
  def __init__(self):
    self._values = set()

  def add(self, value):
    if value == "":
      return
    self._values.add(value)

  def redact(self, text):
    for value in self._ordered():
      text = text.replace(value, PLACEHOLDER)
    return text

  def redact_bytes(self, data):
    return self.redact(data.decode("utf-8", errors="replace")).encode("utf-8")

  def contains(self, value):
    return value in self._values

  def _ordered(self):
    return sorted(self._values, key=len, reverse=True)
