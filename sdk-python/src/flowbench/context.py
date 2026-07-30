"""Driver-agnostic ctx surface: Http/VarsProxy/UserProxy/EnvProxy delegate
every operation to whichever driver a Context was built with -- the
compile-time TraceDriver (drivers/trace.py) or the real-execution LiveDriver
(drivers/live.py). Neither driver's types leak in here.
"""

import re

from .errors import FlowCompileError

_METHODS = ("get", "post", "put", "patch", "delete")

# ir.validate.go's identRe. A span name is a structural identity, and `.` and
# `@` are reserved for dot-paths and for `name@variant` respectively -- an
# observation called "a.b" would fold as two levels of nothing.
_IDENT_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_-]*$")

# Matches ir.GraphQLErrorPolicy. Unset means "fail", so a broken query is a
# failed step rather than a pass nobody asserted on.
_ERROR_POLICIES = ("fail", "allow_partial", "ignore")


class Http:
  def __init__(self, driver):
    self._driver = driver

  def _call(self, method, url, *, json=None, headers=None, query=None):
    return self._driver.call(method, url, json=json, headers=headers, query=query)


class GraphQL:
  """``ctx.graphql(...)`` — one GraphQL operation.

  It compiles to a ``graphql`` step, not a hand-rolled POST, so the engine
  reads the ``data``/``errors`` shape and fails the step on an operation
  error that arrives inside a ``200 OK``.
  """

  def __init__(self, driver):
    self._driver = driver

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
    return self._driver.graphql(
      url,
      query=query,
      variables=variables,
      operation_name=operation_name,
      headers=headers,
      on_errors=on_errors,
    )


class WS:
  """``ctx.ws(...)`` — one step's worth of work on a WebSocket session.

  A ``url`` opens a session; without one the step joins the one an earlier
  step opened, and ``session=`` names it in both cases. The session is closed
  when the *iteration* ends rather than when the step returns, which is what
  lets an exchange span several steps.

  ``receive=True`` takes the next frame; ``receive=frame(...)`` (or a list of
  them) says which frame the step is waiting for, and the ones that arrive
  meanwhile are skipped rather than failed on.
  """

  def __init__(self, driver):
    self._driver = driver

  def __call__(
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
    if url is None and send is None and receive is None:
      raise FlowCompileError(
        "ctx.ws() must open a session (a url), send a frame, or receive one"
      )
    if url is None and (headers or subprotocols):
      raise FlowCompileError(
        "ctx.ws() headers and subprotocols ride on the handshake, so they "
        "belong to the call that opens the session"
      )
    if timeout is not None and receive is None:
      raise FlowCompileError(
        "ctx.ws(timeout=...) bounds a receive, and this step receives nothing"
      )
    if receive is not None and receive is not True:
      conditions = receive if isinstance(receive, list) else [receive]
      for condition in conditions:
        if not isinstance(condition, dict):
          raise FlowCompileError(
            "ctx.ws(receive=...) takes True for the next frame, or frame(...) "
            f"conditions selecting which one, got {type(condition).__name__}"
          )
    return self._driver.ws(
      url,
      session=session,
      send=send,
      receive=receive,
      timeout=timeout,
      headers=headers,
      subprotocols=subprotocols,
    )


class GRPC:
  """``ctx.grpc(...)`` — one unary gRPC call.

  ``proto`` names the schema (relative to the flow file) and ``method`` the
  fully-qualified ``package.Service/Method``. ``url`` is optional and is only
  an address: a gRPC step has no path, because the method *is* the path, so a
  call against the target itself names no url at all.

  ``headers`` are gRPC metadata — HTTP/2 headers on the wire — so every auth
  scheme reaches a gRPC call unchanged.
  """

  def __init__(self, driver):
    self._driver = driver

  def __call__(
    self,
    method,
    *,
    proto,
    message=None,
    url=None,
    headers=None,
    import_paths=None,
  ):
    if "/" not in method.strip("/"):
      raise FlowCompileError(
        "ctx.grpc(method=...) must be fully qualified as "
        f"'package.Service/Method', got {method!r}"
      )
    if message is not None and not isinstance(message, dict):
      raise FlowCompileError(
        "ctx.grpc(message=...) is the request message as a mapping of field "
        f"to value, got {type(message).__name__}"
      )
    return self._driver.grpc(
      method,
      proto=proto,
      message=message,
      url=url,
      headers=headers,
      import_paths=import_paths,
    )


class Prompt:
  """``ctx.prompt(...)`` — wraps one LLM call the flow's own code makes.

  FlowBench never makes the call, templates the prompt, or sets a model
  parameter (ADR 0009): the block's own code does that with whatever SDK it
  already uses, and hands the exchange over with ``p.record(...)``.

      with ctx.prompt("classify", template=SYSTEM, pace="20/m") as p:
          reply = client.chat.completions.create(model=..., messages=msgs)
          p.record(msgs, reply.choices[0].message.content, usage=reply.usage)

  ``variant=`` is a label, not machinery: what varies is the block's own code,
  and the label is what gives that version its own span identity
  (``classify@concise``) so folding, metrics and diffs stay per-variant.
  """

  def __init__(self, driver):
    self._driver = driver

  def __call__(
    self,
    name,
    *,
    template=None,
    variant=None,
    timeout=None,
    pace=None,
    burst=None,
  ):
    if not isinstance(name, str) or not _IDENT_RE.match(name):
      raise FlowCompileError(
        f"ctx.prompt(name) must match {_IDENT_RE.pattern} (dots and @ are "
        f"reserved for span names), got {name!r}"
      )
    if variant is not None and (
      not isinstance(variant, str) or not _IDENT_RE.match(variant)
    ):
      raise FlowCompileError(
        f"ctx.prompt(variant=...) must match {_IDENT_RE.pattern}, got {variant!r}"
      )
    if burst is not None and pace is None:
      raise FlowCompileError(
        "ctx.prompt(burst=...) is an allowance against a pace, and this "
        "observation declares none"
      )
    return self._driver.prompt(
      name,
      template=template,
      variant=variant,
      timeout=timeout,
      pace=pace,
      burst=burst,
    )


def _make_method(verb):
  def method(self, url, *, json=None, headers=None, query=None):
    return self._call(verb.upper(), url, json=json, headers=headers, query=query)

  method.__name__ = verb
  return method


for _verb in _METHODS:
  setattr(Http, _verb, _make_method(_verb))
del _verb


class VarsProxy:
  def __init__(self, driver):
    self._driver = driver

  def __setitem__(self, key, value):
    self._driver.set_var(key, value)

  def __getitem__(self, key):
    return self._driver.get_var(key)


class UserProxy:
  def __init__(self, driver):
    self._driver = driver

  def __getitem__(self, field):
    return self._driver.get_user_field(field)


class EnvProxy:
  def __init__(self, driver):
    self._driver = driver

  def __getitem__(self, name):
    return self._driver.get_env(name)


class Context:
  def __init__(self, driver, has_data_pool):
    self._driver = driver
    self.http = Http(driver)
    self.graphql = GraphQL(driver)
    self.ws = WS(driver)
    self.grpc = GRPC(driver)
    self.prompt = Prompt(driver)
    self.vars = VarsProxy(driver)
    self.env = EnvProxy(driver)
    self._has_data_pool = has_data_pool

  def secret(self, value):
    """Flags a value the step computed -- a token minted mid-flow, a signed
    URL -- as sensitive, so it is scrubbed from this run's artifacts. Returns
    it, so it can wrap an expression in place. Credentials that exist before
    the run starts use the module-level flowbench.secret(...) instead.
    """
    return self._driver.add_secret(value)

  @property
  def user(self):
    if not self._has_data_pool:
      raise FlowCompileError(
        "ctx.user is only available when Flow(..., data=...) binds a data pool"
      )
    return UserProxy(self._driver)
