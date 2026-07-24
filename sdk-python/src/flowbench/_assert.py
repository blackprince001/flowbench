from ._context import _Subject
from ._errors import FlowCompileError
from ._template import TemplateRef


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
        self._builder.add_assertion(assertion)

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
    if isinstance(subject, _Subject):
        return AssertionBuilder(subject._builder, source=subject.source, key=subject.key)
    raise FlowCompileError(
        f"expect() only accepts a response field (r.status, r.header(...)) or an "
        f"extracted ctx.vars value, got {type(subject).__name__}"
    )
