"""Tracks values that must never reach a stored run artifact.

Mirrors internal/secret/secret.go: every value resolved from ctx.env during
live execution is registered here, and every failure detail, span payload,
and meta field is scrubbed through it before anything is written.

The Go engine sees every credential a flow uses, because the flow declares
them as {{ env.* }}. A Python-driven flow does not: the team's own client is
usually built at import scope from an environment variable FlowBench never
touches, and auto-instrumentation (instrument.py) then captures that client's
requests. secret() is how the author hands such a value over, and it is
module-level for exactly that reason -- it has to work before any run exists.
"""

PLACEHOLDER = "[redacted]"

_flagged = set()


def secret(value):
  """Flags a value as sensitive, so it is scrubbed from every artifact of
  every run this process writes. Returns the value, so it can wrap an
  expression in place:

      client = OpenAI(api_key=flowbench.secret(os.environ["OPENAI_API_KEY"]))
  """
  text = value if isinstance(value, str) else str(value)
  if text:
    _flagged.add(text)
  return value


def flagged():
  """The values flagged so far. Each run seeds its SecretSet from this."""
  return frozenset(_flagged)


class SecretSet:
  def __init__(self, values=()):
    self._values = {v for v in values if v}

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
