from .errors import FlowCompileError
from .template import TemplateRef

_METHODS = ("get", "post", "put", "patch", "delete")

# Matches ir.GraphQLErrorPolicy. Unset means "fail", so a broken query is a
# failed step rather than a pass nobody asserted on.
_ERROR_POLICIES = ("fail", "allow_partial", "ignore")


class StepBuilder:
  """Accumulates the pieces of one ``ir.Step`` as a step function traces."""

  def __init__(self, step_id, available_vars):
    self.step_id = step_id
    self.available_vars = available_vars
    # kind is the IR step type the traced request compiles to ("call" or
    # "graphql"); spec is that type's block.
    self.kind = None
    self.spec = None
    self.extract = []
    self.assert_ = []
    self.retry = None

  @property
  def call_spec(self):
    """The traced ``call`` block, or None for a step of another kind."""
    return self.spec if self.kind == "call" else None

  def set_request(self, kind, spec):
    if self.spec is not None:
      raise FlowCompileError(
        f"step {self.step_id!r} makes more than one ctx.http call; "
        "each @flow.step function must make exactly one call "
        "(split it into two steps)"
      )
    self.kind, self.spec = kind, spec

  def add_extraction(self, var, path):
    self.extract.append({"var": var, "path": path})
    self.available_vars.add(var)

  def add_assertion(self, assertion):
    self.assert_.append(assertion)


class Subject:
  """An assertable field of a Response: status or a named header."""

  def __init__(self, builder, source, key=None):
    self._builder = builder
    self.source = source
    self.key = key


class PendingExtraction:
  """The result of ``response.json_path(...)``: only valid as the RHS of
  ``ctx.vars[key] = ...``."""

  def __init__(self, builder, path):
    self._builder = builder
    self.path = path


class Response:
  def __init__(self, builder):
    self._builder = builder

  @property
  def status(self):
    return Subject(self._builder, source="status")

  def header(self, name):
    return Subject(self._builder, source="header", key=name)

  def json_path(self, path):
    return PendingExtraction(self._builder, path)


class Http:
  def __init__(self, builder):
    self._builder = builder

  def _call(self, method, url, *, json=None, headers=None, query=None):
    spec = {"method": method, "url": url}
    if headers:
      spec["headers"] = {k: str(v) for k, v in headers.items()}
    if query:
      spec["query"] = {k: str(v) for k, v in query.items()}
    if json is not None:
      spec["body"] = json
    self._builder.set_request("call", spec)
    return Response(self._builder)


class GraphQL:
  """``ctx.graphql(...)`` — one GraphQL operation.

  It compiles to a ``graphql`` step, not a hand-rolled POST, so the engine
  reads the ``data``/``errors`` shape and fails the step on an operation
  error that arrives inside a ``200 OK``.
  """

  def __init__(self, builder):
    self._builder = builder

  def __call__(
    self,
    url,
    *,
    query,
    variables=None,
    operation_name=None,
    headers=None,
    on_errors=None,
  ):
    if on_errors is not None and on_errors not in _ERROR_POLICIES:
      raise FlowCompileError(
        f"on_errors must be one of {list(_ERROR_POLICIES)!r}, got {on_errors!r}"
      )
    spec = {"url": url, "query": query}
    if variables:
      spec["variables"] = variables
    if operation_name:
      spec["operation_name"] = operation_name
    if headers:
      spec["headers"] = {k: str(v) for k, v in headers.items()}
    if on_errors:
      spec["on_errors"] = on_errors
    self._builder.set_request("graphql", spec)
    return Response(self._builder)


def _make_method(verb):
  def method(self, url, *, json=None, headers=None, query=None):
    return self._call(verb.upper(), url, json=json, headers=headers, query=query)

  method.__name__ = verb
  return method


for _verb in _METHODS:
  setattr(Http, _verb, _make_method(_verb))
del _verb


class VarsProxy:
  def __init__(self, builder):
    self._builder = builder

  def __setitem__(self, key, value):
    if not isinstance(value, PendingExtraction):
      raise FlowCompileError(
        f"ctx.vars[{key!r}] = ... must be assigned a response.json_path(...) "
        f"extraction, got {type(value).__name__}"
      )
    self._builder.add_extraction(key, value.path)

  def __getitem__(self, key):
    if key not in self._builder.available_vars:
      raise FlowCompileError(
        f"ctx.vars[{key!r}] read before it was extracted by an earlier step "
        f"(available: {sorted(self._builder.available_vars)!r})"
      )
    return TemplateRef(key, builder=self._builder)


class UserProxy:
  def __getitem__(self, field):
    return TemplateRef(f"user.{field}")


class EnvProxy:
  def __getitem__(self, name):
    return TemplateRef(f"env.{name}")


class Context:
  def __init__(self, builder, has_data_pool):
    self._builder = builder
    self.http = Http(builder)
    self.graphql = GraphQL(builder)
    self.vars = VarsProxy(builder)
    self.env = EnvProxy()
    self._has_data_pool = has_data_pool

  @property
  def user(self):
    if not self._has_data_pool:
      raise FlowCompileError(
        "ctx.user is only available when Flow(..., data=...) binds a data pool"
      )
    return UserProxy()
