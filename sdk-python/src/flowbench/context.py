"""Driver-agnostic ctx surface: Http/VarsProxy/UserProxy/EnvProxy delegate
every operation to whichever driver a Context was built with -- the
compile-time TraceDriver (drivers/trace.py) or the real-execution LiveDriver
(drivers/live.py). Neither driver's types leak in here.
"""

from .errors import FlowCompileError

_METHODS = ("get", "post", "put", "patch", "delete")

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
    self.vars = VarsProxy(driver)
    self.env = EnvProxy(driver)
    self._has_data_pool = has_data_pool

  @property
  def user(self):
    if not self._has_data_pool:
      raise FlowCompileError(
        "ctx.user is only available when Flow(..., data=...) binds a data pool"
      )
    return UserProxy(self._driver)
