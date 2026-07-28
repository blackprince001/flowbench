"""The compile-time driver: records IR instead of doing anything for real.
Backs Flow.compile() -- the symbolic tracing surface from issue #22,
unchanged in behavior, now expressed as one of two driver implementations
Context/Http/VarsProxy/UserProxy/EnvProxy can delegate to (see live.py for
the other).
"""

from ..errors import FlowCompileError
from ..template import TemplateRef

_METHODS = ("get", "post", "put", "patch", "delete")


class Subject:
  """An assertable field of a Response: status or a named header."""

  def __init__(self, driver, source, key=None):
    self._builder = driver
    self.source = source
    self.key = key


class PendingExtraction:
  """The result of ``response.json_path(...)``: only valid as the RHS of
  ``ctx.vars[key] = ...``."""

  def __init__(self, driver, path):
    self._builder = driver
    self.path = path


class Response:
  def __init__(self, driver):
    self._builder = driver

  @property
  def status(self):
    return Subject(self._builder, source="status")

  def header(self, name):
    return Subject(self._builder, source="header", key=name)

  def json_path(self, path):
    return PendingExtraction(self._builder, path)


class TraceDriver:
  """Accumulates the pieces of one ``ir.Step`` as a step function traces.

  Implements the driver protocol Http/GraphQL/WS/GRPC/VarsProxy/UserProxy/
  EnvProxy call into: call(), graphql(), ws(), grpc(), set_var(), get_var(),
  get_user_field(), get_env(). kind/spec is the IR step type the traced
  request compiles to ("call", "graphql", "ws" or "grpc") and that type's
  block.
  """

  def __init__(self, step_id, available_vars):
    self.step_id = step_id
    self.available_vars = available_vars
    self.kind = None
    self.spec = None
    self.extract = []
    self.assert_ = []
    self.retry = None

  @property
  def call_spec(self):
    """The traced ``call`` block, or None for a step of another kind."""
    return self.spec if self.kind == "call" else None

  def call(self, method, url, *, json=None, headers=None, query=None):
    spec = {"method": method, "url": url}
    if headers:
      spec["headers"] = {k: str(v) for k, v in headers.items()}
    if query:
      spec["query"] = {k: str(v) for k, v in query.items()}
    if json is not None:
      spec["body"] = json
    self.set_request("call", spec)
    return Response(self)

  def graphql(
    self,
    url,
    *,
    query,
    variables=None,
    operation_name=None,
    headers=None,
    on_errors=None,
  ):
    spec = {"url": url, "query": query}
    if variables:
      spec["variables"] = variables
    if operation_name:
      spec["operation_name"] = operation_name
    if headers:
      spec["headers"] = {k: str(v) for k, v in headers.items()}
    if on_errors:
      spec["on_errors"] = on_errors
    self.set_request("graphql", spec)
    return Response(self)

  def ws(
    self,
    url=None,
    *,
    session=None,
    send=None,
    receive=None,
    timeout=None,
    headers=None,
    subprotocols=None,
  ):
    spec = {}
    if url is not None:
      spec["url"] = url
    if session:
      spec["session"] = session
    if headers:
      spec["headers"] = {k: str(v) for k, v in headers.items()}
    if subprotocols:
      spec["subprotocols"] = list(subprotocols)
    if send is not None:
      spec["send"] = send
    if receive is not None:
      block = {}
      if receive is not True:
        block["match"] = list(receive) if isinstance(receive, list) else [receive]
      if timeout:
        block["timeout"] = timeout
      spec["receive"] = block
    self.set_request("ws", spec)
    return Response(self)

  def grpc(
    self,
    method,
    *,
    proto,
    message=None,
    url=None,
    headers=None,
    import_paths=None,
  ):
    spec = {"proto": proto, "method": method}
    if url is not None:
      spec["url"] = url
    if import_paths:
      spec["import_paths"] = list(import_paths)
    if message is not None:
      spec["message"] = message
    if headers:
      spec["headers"] = {k: str(v) for k, v in headers.items()}
    self.set_request("grpc", spec)
    return Response(self)

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

  def set_var(self, key, value):
    if not isinstance(value, PendingExtraction):
      raise FlowCompileError(
        f"ctx.vars[{key!r}] = ... must be assigned a response.json_path(...) "
        f"extraction, got {type(value).__name__}"
      )
    self.add_extraction(key, value.path)

  def get_var(self, key):
    if key not in self.available_vars:
      raise FlowCompileError(
        f"ctx.vars[{key!r}] read before it was extracted by an earlier step "
        f"(available: {sorted(self.available_vars)!r})"
      )
    return TemplateRef(key, builder=self)

  def get_user_field(self, field):
    return TemplateRef(f"user.{field}")

  def get_env(self, name):
    return TemplateRef(f"env.{name}")

  def add_secret(self, value):
    # Compilation produces IR, not artifacts, so there is nothing to redact;
    # the declared value is a TemplateRef the Go engine resolves and registers
    # as a secret itself.
    return value
