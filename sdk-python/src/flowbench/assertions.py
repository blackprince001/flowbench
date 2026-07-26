from .drivers.live import LiveAssertionBuilder, LiveValue, PendingLiveExtraction
from .drivers.trace import PendingExtraction, Subject
from .errors import FlowCompileError, FlowExecutionError
from .template import TemplateRef


class AssertionBuilder:
  def __init__(self, builder, source, key=None):
    self._builder = builder
    self._source = source
    self._key = key

  def _append(self, op, value=None, *, has_value=False):
    assertion = {"source": self._source, "op": op}
    if self._key is not None:
      assertion["key"] = self._key
    if has_value:
      assertion["value"] = value
    return self._builder.add_assertion(assertion)

  def to_be(self, value):
    if value is None:
      return self._append("not_exists")
    return self._append("eq", value, has_value=True)

  def not_to_be(self, value):
    if value is None:
      return self._append("exists")
    return self._append("ne", value, has_value=True)

  def to_be_less_than(self, value):
    return self._append("lt", value, has_value=True)

  def to_be_less_than_or_equal(self, value):
    return self._append("lte", value, has_value=True)

  def to_be_greater_than(self, value):
    return self._append("gt", value, has_value=True)

  def to_be_greater_than_or_equal(self, value):
    return self._append("gte", value, has_value=True)

  def to_contain(self, value):
    return self._append("contains", value, has_value=True)

  def to_match(self, pattern):
    return self._append("matches", pattern, has_value=True)

  def to_exist(self):
    return self._append("exists")

  def to_not_exist(self):
    return self._append("not_exists")


def expect(subject):
  if isinstance(subject, TemplateRef):
    if "." in subject.ref:
      raise FlowCompileError(
        f"expect() cannot assert on {subject.ref!r} directly; "
        "only extracted ctx.vars values support assertions"
      )
    return AssertionBuilder(subject._builder, source="var", key=subject.ref)
  if isinstance(subject, Subject):
    return AssertionBuilder(subject._builder, source=subject.source, key=subject.key)
  # r.json_path(...) reads as an assertion subject as readily as an extraction
  # target, and for a ws step the body is the only thing there is to assert on
  # -- a frame has no status line and no headers.
  if isinstance(subject, PendingExtraction):
    return AssertionBuilder(subject._builder, source="body", key=subject.path)
  if isinstance(subject, PendingLiveExtraction):
    return LiveAssertionBuilder(
      LiveValue(subject.value, kind="body", driver=subject._driver, key=subject.path)
    )
  if isinstance(subject, LiveValue):
    if subject.kind in ("user", "env"):
      raise FlowExecutionError(
        f"expect() cannot assert on {subject.kind}.{subject.key} directly; "
        "only extracted ctx.vars values support assertions"
      )
    return LiveAssertionBuilder(subject)
  raise FlowCompileError(
    f"expect() only accepts a response field (r.status, r.header(...), "
    f"r.json_path(...)) or an extracted ctx.vars value, "
    f"got {type(subject).__name__}"
  )


class _MatchSink:
  """Collects an assertion instead of appending it to a step.

  ``frame(...)`` builds a condition to hand to ``ctx.ws(receive=...)``, so its
  builder has no step to append to — it returns the assertion to the caller.
  """

  def add_assertion(self, assertion):
    return assertion


def frame(path):
  """A condition on a received WebSocket frame, for ``ctx.ws(receive=...)``.

  A match is a filter, not an assertion: a duplex connection carries frames
  this step never asked for, and one that does not satisfy the condition is
  skipped rather than failed on. Use ``expect(...)`` on the returned frame to
  judge the one the step actually matched.
  """
  return AssertionBuilder(_MatchSink(), source="body", key=path)
