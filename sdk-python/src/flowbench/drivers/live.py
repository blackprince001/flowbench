"""The real-execution driver: backs Flow.run()'s direct-execution path
(issue #25, ADR 0012's Python-driven producer). Unlike TraceDriver, this
makes real httpx calls, evaluates real assertions, and accumulates a real
span tree -- one LiveDriver instance lives for a whole flow iteration (every
step shares it), not one per step.

Mirrors internal/executor/iteration.go's runStep/runCall/record/
recordThrottle closely enough to produce structurally equivalent run
artifacts, simplified where the current Python DSL exposes no per-step
on_failure/throttle/capture overrides (#22 never built that surface): every
failure aborts the flow (integration/system's default), 429 alone is a
throttle, and every call is captured.
"""

import os
import time

from .. import target as target_mod
from ..errors import FlowExecutionError
from ..eval import compare
from ..jsonpath import query_json
from ..retry_exec import backoff_delay, retryable
from ..secret import SecretSet
from ..span import OUTCOME_FAILED, OUTCOME_OK, OUTCOME_THROTTLED, Span

try:
  import httpx
except ImportError as e:  # pragma: no cover
  raise FlowExecutionError(
    "the httpx package is required for live execution; install sdk-python's "
    "dependencies (uv sync --project sdk-python)"
  ) from e


class FlowAbortedError(Exception):
  """Internal control-flow signal: a failure aborts the flow. This is the
  only on_failure behavior the Python DSL exposes today (matches Go's
  integration/system mode default) -- caught by Flow.run()'s step loop.
  """


class LiveValue:
  """A real value read from a live response, an extracted var, a data-pool
  row, or the environment. kind/key identify it for assertion naming and
  error messages, mirroring trace mode's Subject(builder, source, key).
  """

  def __init__(self, value, kind, driver, key=None):
    self.value = value
    self.kind = kind
    self.key = key
    self._driver = driver

  def __str__(self):
    return _stringify(self.value)

  def __repr__(self):
    return f"LiveValue({self.value!r})"


class _PendingLiveExtraction:
  """The result of a live response's json_path(...): the value is already
  resolved, but span/failure recording waits for ctx.vars[key] = ... to
  supply the destination var name (the child span's name, matching Go's
  sp.Child(ex.Var, ...)).
  """

  def __init__(self, value, found, path):
    self.value = value
    self.found = found
    self.path = path


class LiveResponse:
  def __init__(self, resp, driver):
    self._resp = resp
    self._driver = driver

  @property
  def status(self):
    return LiveValue(self._resp.status_code, kind="status", driver=self._driver)

  def header(self, name):
    return LiveValue(
      self._resp.headers.get(name), kind="header", driver=self._driver, key=name
    )

  def json_path(self, path):
    value, found = query_json(self._resp.content, path)
    return _PendingLiveExtraction(value, found, path)


class LiveAssertionBuilder:
  def __init__(self, subject):
    self._subject = subject

  def _check(self, op, expected):
    driver = self._subject._driver
    name = _assert_name(self._subject.kind, self._subject.key)
    child = driver._current_span.child(name, driver._elapsed())
    actual = self._subject.value

    if op == "exists":
      passed = actual is not None
    elif op == "not_exists":
      passed = actual is None
    else:
      passed = compare(op, actual, expected)

    if not passed:
      child.outcome = OUTCOME_FAILED
      driver._record_failure(child, _detail(self._subject, op, actual, expected))

  def to_be(self, value):
    if value is None:
      return self._check("not_exists", None)
    return self._check("eq", value)

  def not_to_be(self, value):
    if value is None:
      return self._check("exists", None)
    return self._check("ne", value)

  def to_be_less_than(self, value):
    return self._check("lt", value)

  def to_be_less_than_or_equal(self, value):
    return self._check("lte", value)

  def to_be_greater_than(self, value):
    return self._check("gt", value)

  def to_be_greater_than_or_equal(self, value):
    return self._check("gte", value)

  def to_contain(self, value):
    return self._check("contains", value)

  def to_match(self, pattern):
    return self._check("matches", pattern)

  def to_exist(self):
    return self._check("exists", None)

  def to_not_exist(self):
    return self._check("not_exists", None)


class IterationResult:
  """One flow-run's outcome: the completed span tree, the worst outcome
  across its steps, recorded failures, and whether any step throttled --
  mirrors internal/executor.Iteration, minus the Aborted field (#25's
  Python DSL exposes no abort_run; every failure aborts the flow).
  """

  def __init__(self, spans, outcome, failures, throttled):
    self.spans = spans
    self.outcome = outcome
    self.failures = failures  # list of (step_id, detail)
    self.throttled = throttled


class LiveDriver:
  def __init__(self, target_cfg, has_data_pool, secrets=None, timeout=30.0):
    self._target = target_cfg
    self._base_url = target_cfg.base_url
    self._client = httpx.Client(timeout=timeout)
    self._secrets = secrets if secrets is not None else SecretSet()
    self._vars = {}
    self._row = None
    self._has_data_pool = has_data_pool
    self._anchor = time.monotonic()
    self._current_span = None
    self._current_step_id = None
    self._current_retry = None

    self.spans = []
    self.failures = []  # list of (step_id, detail)
    self.outcome = OUTCOME_OK
    self.throttled = False

  def close(self):
    self._client.close()

  def set_row(self, row):
    self._row = row

  def result(self):
    return IterationResult(self.spans, self.outcome, self.failures, self.throttled)

  def _elapsed(self):
    return time.monotonic() - self._anchor

  # -- per-step lifecycle, driven by Flow.run() ---------------------------

  def begin_step(self, step_id, retry):
    self._current_step_id = step_id
    self._current_retry = retry.to_ir() if retry is not None else None
    self._current_span = Span(step_id, self._elapsed())

  def end_step(self):
    step = self._current_span
    self.spans.append(step)
    self.outcome = _worst(self.outcome, step.outcome)
    self._current_span = None
    self._current_step_id = None
    self._current_retry = None

  # -- driver protocol: Http/VarsProxy/UserProxy/EnvProxy delegate here ---

  def call(self, method, url, *, json=None, headers=None, query=None):
    resolved_url = self._resolve_url(_stringify(_unwrap(url)))
    body = _unwrap(json) if json is not None else None
    live_headers = (
      {k: _stringify(_unwrap(v)) for k, v in headers.items()} if headers else None
    )
    live_query = (
      {k: _stringify(_unwrap(v)) for k, v in query.items()} if query else None
    )

    if not target_mod.allows(self._target, resolved_url):
      detail = f"host allow-list: {resolved_url} is not an allowed target"
      self._record_failure(self._current_span, detail)

    step = self._current_span
    policy = self._current_retry
    if policy is None:
      resp = self._do_request(
        method, resolved_url, live_headers, live_query, body, step
      )
    else:
      resp = self._do_request_with_retry(
        method, resolved_url, live_headers, live_query, body, step, policy
      )

    if resp.status_code == 429:
      self._record_throttle(resp.status_code)

    return LiveResponse(resp, self)

  def set_var(self, key, value):
    if not isinstance(value, _PendingLiveExtraction):
      raise FlowExecutionError(
        f"ctx.vars[{key!r}] = ... must be assigned a response.json_path(...) "
        f"extraction, got {type(value).__name__}"
      )
    child = self._current_span.child(key, self._elapsed())
    if not value.found:
      child.outcome = OUTCOME_FAILED
      self._record_failure(child, f"extract {key!r} found nothing")
    self._vars[key] = value.value

  def get_var(self, key):
    if key not in self._vars:
      raise FlowExecutionError(
        f"ctx.vars[{key!r}] read before it was extracted by an earlier step"
      )
    return LiveValue(self._vars[key], kind="var", driver=self, key=key)

  def get_user_field(self, field):
    if self._row is None:
      raise FlowExecutionError(
        f"ctx.user[{field!r}] read but no data-pool row is bound"
      )
    if field not in self._row:
      raise FlowExecutionError(f"ctx.user[{field!r}] is not a column in the data pool")
    return LiveValue(self._row[field], kind="user", driver=self, key=field)

  def get_env(self, name):
    value = os.environ.get(name, "")
    self._secrets.add(value)
    return LiveValue(value, kind="env", driver=self, key=name)

  # -- HTTP execution -------------------------------------------------------

  def _resolve_url(self, url):
    if not self._base_url or "://" in url:
      return url
    return self._base_url.rstrip("/") + "/" + url.lstrip("/")

  def _send(self, method, url, headers, query, body, span_obj):
    resp = self._client.request(method, url, headers=headers, params=query, json=body)
    span_obj.duration = resp.elapsed.total_seconds()
    span_obj.set_raw(resp.request.content or b"", resp.content or b"")
    span_obj.set_call(
      method,
      str(resp.request.url),
      resp.status_code,
      resp.headers.get("Retry-After", ""),
    )
    return resp

  def _do_request(self, method, url, headers, query, body, step):
    try:
      return self._send(method, url, headers, query, body, step)
    except httpx.HTTPError as e:
      step.outcome = OUTCOME_FAILED
      self._record_failure(step, f"call failed: {e}")

  def _do_request_with_retry(self, method, url, headers, query, body, step, policy):
    max_attempts = policy.get("max_attempts") or 1
    attempt = 1
    resp = None
    transport_error = None
    while True:
      attempt_span = step.child(f"attempt {attempt}", self._elapsed())
      try:
        resp = self._send(method, url, headers, query, body, attempt_span)
      except httpx.HTTPError as e:
        attempt_span.outcome = OUTCOME_FAILED
        transport_error = e
        break
      if attempt >= max_attempts or not retryable(policy, resp.status_code):
        break
      delay = backoff_delay(policy, attempt, resp.headers)
      if delay > 0:
        backoff_span = step.child("backoff", self._elapsed())
        time.sleep(delay)
        backoff_span.duration = self._elapsed() - backoff_span.start
      attempt += 1

    step.duration = self._elapsed() - step.start
    if transport_error is not None:
      step.outcome = OUTCOME_FAILED
      self._record_failure(step, f"call failed: {transport_error}")
    return resp

  # -- failure/throttle recording -----------------------------------------

  def _record_failure(self, at_span, detail):
    detail = self._secrets.redact(detail)
    self._current_span.outcome = OUTCOME_FAILED
    at_span.set_failure(detail)
    self.failures.append((self._current_step_id, detail))
    raise FlowAbortedError()

  def _record_throttle(self, status):
    self.throttled = True
    self._current_span.outcome = OUTCOME_THROTTLED
    detail = self._secrets.redact(f"throttled: HTTP {status}")
    self._current_span.set_failure(detail)
    self.failures.append((self._current_step_id, detail))
    raise FlowAbortedError()


def _unwrap(value):
  """Recursively replaces LiveValue with its real value, preserving native
  JSON types (int/float/bool/list/dict) -- unlike TemplateRef, embedding a
  LiveValue directly in a json= body keeps it typed rather than stringifying.
  """
  if isinstance(value, LiveValue):
    return value.value
  if isinstance(value, dict):
    return {k: _unwrap(v) for k, v in value.items()}
  if isinstance(value, list):
    return [_unwrap(v) for v in value]
  return value


def _stringify(value):
  if isinstance(value, bool):
    return "true" if value else "false"
  return str(value)


def _assert_name(kind, key):
  if kind == "header":
    return "assert_header_" + _sanitize(key)
  if kind == "var":
    return "assert_var_" + _sanitize(key)
  return "assert_" + kind  # "assert_status"


def _sanitize(s):
  return "".join(c if c.isalnum() or c == "_" else "_" for c in s)


def _detail(subject, op, actual, expected):
  subj = subject.kind if subject.key is None else f"{subject.kind} {subject.key}"
  if op == "exists":
    return f"{subj} does not exist"
  if op == "not_exists":
    return f"{subj} exists"
  return f"{subj}: {actual!r} {op} {expected!r}"


def _worst(a, b):
  if a == OUTCOME_FAILED or b == OUTCOME_FAILED:
    return OUTCOME_FAILED
  if a == OUTCOME_THROTTLED or b == OUTCOME_THROTTLED:
    return OUTCOME_THROTTLED
  return a
