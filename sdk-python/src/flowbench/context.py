"""Driver-agnostic ctx surface: Http/VarsProxy/UserProxy/EnvProxy delegate
every operation to whichever driver a Context was built with -- the
compile-time TraceDriver (drivers/trace.py) or the real-execution LiveDriver
(drivers/live.py). Neither driver's types leak in here.
"""

from .errors import FlowCompileError

_METHODS = ("get", "post", "put", "patch", "delete")


class Http:
  def __init__(self, driver):
    self._driver = driver

  def _call(self, method, url, *, json=None, headers=None, query=None):
    return self._driver.call(method, url, json=json, headers=headers, query=query)


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
