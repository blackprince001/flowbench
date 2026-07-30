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
from ..errors import FlowCompileError, FlowExecutionError
from ..eval import compare
from ..instrument import Instrumentation
from ..jsonpath import query_json
from ..prompt import (
  hash_prompt,
  normalize_usage,
  parse_timeout,
  render,
  wait_for_pace,
)
from ..redaction import SecretSet
from ..retry_exec import backoff_delay, retryable
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


class RecordedText(str):
  """A prompt or completion a step recorded -- the real string, so the flow's
  own code can parse, slice and chain it as it would any other, and an
  assertion subject, so `expect(p.completion).to_contain(...)` needs no
  machinery of its own (PRD 10.9).
  """

  def __new__(cls, text, kind, driver):
    self = super().__new__(cls, text)
    self.kind = kind
    self.key = None
    self.value = text
    self._driver = driver
    return self


class PendingLiveExtraction:
  """The result of a live response's json_path(...): the value is already
  resolved, but span/failure recording waits for ctx.vars[key] = ... to
  supply the destination var name (the child span's name, matching Go's
  sp.Child(ex.Var, ...)) -- or for expect(...) to assert on it in place.
  """

  def __init__(self, value, found, path, driver):
    self.value = value
    self.found = found
    self.path = path
    self._driver = driver


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
    return PendingLiveExtraction(value, found, path, self._driver)


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

  def __init__(self, spans, outcome, failures, throttled, identities):
    self.spans = spans
    self.outcome = outcome
    self.failures = failures  # list of (step_id, detail)
    self.throttled = throttled
    # Span paths of the observations this iteration recorded. Unlike a step,
    # an observation is not declared anywhere -- it exists because a step's
    # body opened one -- so the only way to know its identity is to run it.
    self.identities = identities


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
    self._call_made = False
    self._observed = 0
    self._instr = Instrumentation(self._elapsed)
    self._instr.attach()

    self.spans = []
    self.failures = []  # list of (step_id, detail)
    self.outcome = OUTCOME_OK
    self.throttled = False
    self.identities = set()

  def close(self):
    self._instr.detach()
    self._client.close()

  def add_secret(self, value):
    """ctx.secret(...): flags a value the flow computed at run time. Import-
    scope credentials use flowbench.secret(...) instead, which this run's
    SecretSet was already seeded from.
    """
    self._secrets.add(value if isinstance(value, str) else str(value))
    return value

  def set_row(self, row):
    self._row = row

  def result(self):
    return IterationResult(
      self.spans, self.outcome, self.failures, self.throttled, self.identities
    )

  def note_observation(self, span_name):
    """Remembers that this step opened an observation under this identity, as
    the span path folding will key it by.
    """
    self.identities.add(f"{self._current_step_id}.{span_name}")

  def _elapsed(self):
    return time.monotonic() - self._anchor

  # -- per-step lifecycle, driven by Flow.run() ---------------------------

  def begin_step(self, step_id, retry):
    self._current_step_id = step_id
    self._current_retry = retry.to_ir() if retry is not None else None
    self._current_span = Span(step_id, self._elapsed())
    self._call_made = False
    self._observed = 0
    self._instr.begin_step(self._current_span)

  def end_step(self):
    step = self._current_span
    step_id = self._current_step_id
    self._instr.end_step()
    # A step earns its span by putting a request on the wire. ctx.http is one
    # way; a call the step's own code makes -- its SDK, its helper, its LLM
    # client -- is another, and auto-instrumentation sees those too, so a step
    # that never touches ctx.http is no longer empty by definition. A recorded
    # observation counts as well: it is an exchange with a provider whatever
    # carried it, and not every client is one instrumentation can see -- a
    # `requests`-based SDK, or a model reached over something that is not HTTP.
    # A step that already failed is exempt: it has said what went wrong, and
    # the authoring rule laid over the top of that would bury the finding --
    # "made no HTTP call" is not what happened when the provider's client threw
    # before it got one out.
    if (
      step.outcome != OUTCOME_FAILED
      and not self._call_made
      and not self._instr.recorded
      and not self._observed
    ):
      raise FlowExecutionError(
        f"step {step_id!r} made no HTTP call; every @flow.step function must "
        "make one, through ctx.http or through the flow's own client"
      )
    if self._instr.throttled:
      self.throttled = True
      step.outcome = _worst(step.outcome, OUTCOME_THROTTLED)
    self.spans.append(step)
    self.outcome = _worst(self.outcome, step.outcome)
    self._current_span = None
    self._current_step_id = None
    self._current_retry = None

  # -- driver protocol: Http/VarsProxy/UserProxy/EnvProxy delegate here ---

  def call(self, method, url, *, json=None, headers=None, query=None):
    if self._call_made:
      raise FlowExecutionError(
        f"step {self._current_step_id!r} makes more than one ctx.http call; "
        "each @flow.step function must make exactly one call "
        "(split it into two steps)"
      )
    self._call_made = True

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
    raise FlowExecutionError(
      "ctx.graphql() is not yet supported by live execution -- run "
      "`flowbench run <file>.py` instead (the Go engine supports GraphQL steps)"
    )

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
    raise FlowExecutionError(
      "ctx.ws() is not yet supported by live execution -- run "
      "`flowbench run <file>.py` instead (the Go engine supports ws steps)"
    )

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
    raise FlowExecutionError(
      "ctx.grpc() is not yet supported by live execution -- run "
      "`flowbench run <file>.py` instead (the Go engine supports grpc steps)"
    )

  def prompt(
    self, name, *, template=None, variant=None, timeout=None, pace=None, burst=None
  ):
    if self._current_span is None:
      raise FlowExecutionError(
        "ctx.prompt(...) is only available inside a @flow.step function"
      )
    return LivePromptObservation(
      self,
      name=name,
      template=template,
      variant=variant,
      timeout=timeout,
      pace=pace,
      burst=burst,
    )

  def set_var(self, key, value):
    if not isinstance(value, PendingLiveExtraction):
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
    # Binding makes span_obj the call span, so ctx.http's phase spans hang
    # directly off the step (or retry attempt) exactly as the Go adapter's do,
    # instead of gaining a call span the engine path has no counterpart for.
    # Duration, bodies, and call identity are recorded by the instrumentation.
    self._instr.bind_next(span_obj)
    return self._client.request(method, url, headers=headers, params=query, json=body)

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


class LivePromptObservation:
  """One observed LLM exchange: `with ctx.prompt("classify") as p:` … around
  the call the flow's own code makes (ADR 0009, PRD 10.9).

  FlowBench does not make the call, template the prompt, or set a model
  parameter. What the wrapper does is four things the call site is the only
  place to do them:

    - emits the observation's span (`classify`, or `classify@concise` under a
      variant label) with the SDK's own HTTP parented beneath it, so the
      provider round-trip is never opaque in the flame graph;
    - holds the recorded pair for capture, on every iteration whatever the
      outcome, because a diff needs both sides;
    - hashes the prompt's identity -- the author's `template=` when there is
      one, else the recorded content;
    - paces repetition against a declared ceiling before the call goes out.

  A `timeout=` is measured rather than enforced: the call belongs to the
  author's SDK and FlowBench has no handle to cancel it, so an overrun is
  reported after the call returns. Pass the same bound to the SDK's own
  timeout argument to actually cut it short.

  An async client inside the block is recorded like any other, including a
  batch gathered concurrently -- they all land under the observation. What is
  not supported is two *observations* open at once: the recording parent is
  driver state rather than per-task, and pacing sleeps the thread, so a batch
  of concurrent ctx.prompt blocks would cross their spans and serialize on the
  guard. Gather the calls inside one observation, or take the observations one
  at a time.
  """

  def __init__(self, driver, *, name, template, variant, timeout, pace, burst):
    self.name = name
    self.variant = variant
    self.span_name = f"{name}@{variant}" if variant else name
    self.prompt = None
    self.completion = None
    self.prompt_hash = None
    self.usage = None
    self._driver = driver
    self._template = template
    self._timeout = None if timeout is None else parse_timeout(timeout)
    self._pace = pace
    self._burst = burst
    self._span = None
    self._scope = None
    self._throttled_before = False

  def __enter__(self):
    driver = self._driver
    if self._pace is not None:
      waited = wait_for_pace(self.name, self._pace, self._burst)
      if waited > 0:
        # A sibling of the observation, like a retry's backoff span: the wait
        # is the flow's own, and folding it into the prompt span would inflate
        # every latency figure the observation reports.
        paced = driver._current_span.child("pace", driver._elapsed() - waited)
        paced.duration = waited
    self._span = driver._current_span.child(self.span_name, driver._elapsed())
    driver.note_observation(self.span_name)
    self._scope = driver._instr.enter_scope(self._span)
    self._throttled_before = driver._instr.throttled
    return self

  def __exit__(self, exc_type, exc, tb):
    driver = self._driver
    driver._instr.exit_scope(self._scope)
    self._span.duration = driver._elapsed() - self._span.start

    # A provider 429 reached the call span through instrumentation; the
    # observation carries it up. Marked, not aborted -- there is no assertion
    # to classify at, and aborting would be cutting third-party code off
    # mid-flight (ADR 0006, and the same rule #24 settled for SDK calls).
    if driver._instr.throttled and not self._throttled_before:
      self._span.outcome = OUTCOME_THROTTLED

    if exc is not None:
      # Two kinds of exception pass through untouched: a failure the driver
      # already recorded (a failed assertion, a refused host), which is on its
      # way out carrying its own detail, and FlowBench's own contract errors,
      # which are the flow being written wrong and must stay loud rather than
      # becoming data. What is left is the provider's call raising, and that is
      # this observation failing.
      if isinstance(exc, (FlowAbortedError, FlowExecutionError, FlowCompileError)):
        return False
      self._span.outcome = OUTCOME_FAILED
      driver._record_failure(
        self._span, f"{self.span_name}: {type(exc).__name__}: {exc}"
      )

    if self.prompt is None:
      raise FlowExecutionError(
        f"observation {self.span_name!r} recorded nothing; call "
        "p.record(prompt, completion) inside the with-block -- an observation "
        "nobody records is invisible to the diff view"
      )
    if self._timeout is not None and self._span.duration > self._timeout:
      self._span.outcome = OUTCOME_FAILED
      driver._record_failure(
        self._span,
        f"{self.span_name}: took {self._span.duration:.3f}s, over its "
        f"{self._timeout:g}s timeout",
      )
    return False

  def record(self, prompt, completion, usage=None):
    """Hands the exchange over: what was sent, what came back, and the token
    counts if the provider reported them (a mapping, or the usage object the
    SDK returned -- prompt/completion or input/output naming, either way).
    """
    if self.prompt is not None:
      raise FlowExecutionError(
        f"observation {self.span_name!r} recorded twice; one wrapped call is "
        "one observation, so a second call belongs in its own ctx.prompt(...) "
        "block"
      )
    self.prompt = RecordedText(render(prompt), "prompt", self._driver)
    self.completion = RecordedText(render(completion), "completion", self._driver)
    self.prompt_hash = hash_prompt(self._template, str(self.prompt))
    self.usage = normalize_usage(usage)
    self._span.set_observation(
      str(self.prompt),
      str(self.completion),
      self.prompt_hash,
      self.variant,
      self.usage,
    )
    self._driver._observed += 1


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
  if kind in ("header", "var", "body"):
    return f"assert_{kind}_" + _sanitize(key)
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
