"""SDK-side HTTP auto-instrumentation for the Python-driven path (ADR 0012).

The Go engine owns its HTTP client, so internal/adapters/http.go can hang a
httptrace recorder off every request and emit phase spans. A Python-driven
flow does not: the calls that matter are made by the team's own code and by
whatever SDK it already uses -- the OpenAI client builds its own httpx.Client
and never hears of FlowBench. Without instrumentation those calls are invisible
and Python is an opaque blob in the flame graph.

So the hook goes on the class, not on a client we hand out: httpx.Client.send
is patched once, and every request made while a driver is attached is recorded.
Requests made with no driver attached pass through untouched, which is what
keeps `import flowbench` from instrumenting an unrelated process.

The span shape is the Go adapter's, so the run store cannot tell which producer
wrote a run:

    step_id                     the step span (or, for ctx.http, the call span)
    |- POST /v1/chat/completions  a call span per SDK-made request
       |- http_call               one leg per redirect hop
          |- connect              phase children, as they occur
          |- tls
          |- ttfb
          |- transfer

Phases come from httpcore's `trace` extension, its equivalent of httptrace.
One phase does not survive the translation: httpcore resolves the hostname
inside connect_tcp and traces no DNS event of its own, so a Python-produced
`connect` covers name resolution as well, and there is no `dns` sibling. That
is a real difference from a Go-produced trace and is better left visible than
faked with a second resolution the request never made.

Two limits worth naming. Only the sync httpx.Client is patched, because the
driver that consumes these spans is sync; async clients and `requests` pass
through unrecorded. And the active driver is held in a ContextVar, which a new
thread does not inherit, so a call made on a thread the step spawned is not
recorded -- the safe direction to fail, since the span tree is built without
locks.
"""

import contextvars
import re

import httpx

from .span import OUTCOME_FAILED, OUTCOME_THROTTLED

_LEG = "http_call"

# The driver whose step spans requests attach to, or None outside a run.
_active = contextvars.ContextVar("flowbench_instrumentation", default=None)

_original_send = None


def install():
  """Patches httpx.Client.send once per process. Idempotent, and a no-op at
  request time until a driver attaches, so importing flowbench does not change
  the behavior of a process that never runs a flow.
  """
  global _original_send
  if _original_send is not None:
    return
  _original_send = httpx.Client.send

  def send(client, request, **kwargs):
    instr = _active.get()
    if instr is None:
      return _original_send(client, request, **kwargs)
    return instr.record(client, request, kwargs, _original_send)

  httpx.Client.send = send


class Instrumentation:
  """Records one driver's HTTP calls. One instance per LiveDriver, attached
  for the driver's lifetime; `parent` moves to the current step span as the
  driver walks the flow, and is None between steps so a stray call made
  outside a step is passed through rather than parented to the wrong span.
  """

  def __init__(self, elapsed):
    self._elapsed = elapsed
    self._parent = None
    self._stack = []  # _Call frames in flight, innermost last
    self._bound = None  # span the next request attaches to, set by ctx.http
    self._token = None
    self.recorded = 0  # calls recorded under the current step
    self.throttled = False

  def attach(self):
    install()
    self._token = _active.set(self)

  def detach(self):
    if self._token is not None:
      _active.reset(self._token)
      self._token = None

  def begin_step(self, span):
    self._parent = span
    self._bound = None
    self.recorded = 0
    self.throttled = False

  def end_step(self):
    self._parent = None

  def bind_next(self, span):
    """Points the next request at an existing span instead of a fresh call
    span. ctx.http uses it so its call keeps the Go engine's exact shape --
    the step (or retry attempt) span *is* the call span, with legs directly
    beneath it -- while an SDK-made call gets a call span of its own.
    """
    self._bound = span

  # -- request recording ---------------------------------------------------

  def record(self, client, request, kwargs, send):
    parent = self._stack[-1].span if self._stack else self._parent
    bound, self._bound = self._bound, None
    if parent is None and bound is None:
      return send(client, request, **kwargs)

    if bound is not None:
      call = bound
    else:
      call = parent.child(_call_name(request), self._elapsed())
      self.recorded += 1

    _chain_trace(request, self._trace)
    self._stack.append(_Call(call))
    try:
      response = send(client, request, **kwargs)
    except Exception as e:
      call.outcome = OUTCOME_FAILED
      call.set_failure(f"call failed: {request.method} {request.url}: {e}")
      raise
    finally:
      # Popping mirrors http.go's deferred finish(): a trace event that
      # arrives after send returns -- a streamed body closed later, a
      # speculative dial -- finds no frame and mutates nothing.
      self._stack.pop().close_leg(self._elapsed())
      # Measured from the span's own start, so a bound step span covers the
      # Python that ran before the call too -- otherwise the waterfall would
      # draw the bar earlier than the request it represents.
      call.duration = self._elapsed() - call.start

    call.set_raw(_body(request), _body(response))
    call.set_call(
      request.method,
      str(request.url),
      response.status_code,
      response.headers.get("Retry-After", ""),
    )
    # ctx.http classifies its own outcome (LiveDriver aborts the flow on a
    # throttle); an SDK-made call has no assertion to classify at, so a 429 is
    # marked where it happened and left to bubble up with the step.
    if bound is None and response.status_code == 429:
      call.outcome = OUTCOME_THROTTLED
      self.throttled = True
    return response

  # -- phase recording, a port of http.go's legRecorder --------------------

  def _trace(self, name, info):
    parts = name.split(".", 2)
    handler = _PHASES.get(tuple(parts[1:])) if len(parts) == 3 else None
    if handler is not None and self._stack:
      handler(self._stack[-1], self._elapsed())


class _Call:
  """One request in flight, and the leg it is currently on. Leg state belongs
  to the call rather than to the Instrumentation because a request can start
  while another is still open -- a streamed response the caller has not closed
  yet -- and the inner one's phases must not land on the outer one's leg.
  """

  def __init__(self, span):
    self.span = span
    self.leg = None
    self.marks = {}

  def ensure_leg(self, at):
    if self.leg is None:
      self.leg = self.span.child(_LEG, at)
      self.marks.clear()
    return self.leg

  def mark(self, key, at):
    self.ensure_leg(at)
    self.marks[key] = at

  def phase(self, name, from_key, at):
    leg = self.ensure_leg(at)
    start = self.marks.pop(from_key, None)
    if start is not None:
      leg.child(name, start).duration = at - start

  def close_leg(self, at):
    if self.leg is None:
      return
    self.leg.duration = at - self.leg.start
    self.leg = None
    self.marks.clear()


def _first_byte(call, at):
  """Response headers are in: ttfb closes and transfer opens at the same
  instant, matching http.go's GotFirstResponseByte.
  """
  call.phase("ttfb", "ttfb", at)
  call.mark("transfer", at)


# httpcore names its trace events "<module>.<event>.<stage>"; the module
# differs by protocol (http11/http2) and by proxy, so only the event matters.
_PHASES = {
  ("connect_tcp", "started"): lambda c, at: c.mark("connect", at),
  ("connect_tcp", "complete"): lambda c, at: c.phase("connect", "connect", at),
  ("start_tls", "started"): lambda c, at: c.mark("tls", at),
  ("start_tls", "complete"): lambda c, at: c.phase("tls", "tls", at),
  ("send_request_headers", "started"): lambda c, at: c.ensure_leg(at),
  ("send_request_body", "complete"): lambda c, at: c.mark("ttfb", at),
  ("receive_response_headers", "complete"): _first_byte,
  ("receive_response_body", "complete"): lambda c, at: c.phase(
    "transfer", "transfer", at
  ),
  ("response_closed", "complete"): lambda c, at: c.close_leg(at),
}


def _chain_trace(request, trace):
  """Installs the phase callback without stealing a trace extension the
  caller already set -- theirs still fires, ours runs alongside it.
  """
  existing = request.extensions.get("trace")
  if existing is None:
    request.extensions["trace"] = trace
    return

  def both(name, info):
    trace(name, info)
    return existing(name, info)

  request.extensions["trace"] = both


# A path segment is an identifier when it is all digits, a long hex string, or
# UUID-shaped. Folding is by span name (ADR 0007), so leaving raw ids in would
# give every iteration of a data-pool run its own flame-graph frame.
_IDENTIFIER = re.compile(r"^(?:\d+|[0-9a-fA-F]{8,}|[0-9a-fA-F-]{32,})$")


def _call_name(request):
  path = request.url.path or "/"
  segments = [":id" if _IDENTIFIER.match(s) else s for s in path.split("/")]
  return f"{request.method} {'/'.join(segments) or '/'}"


def _body(message):
  """The request or response body, or empty when it is being streamed --
  reading a stream here would consume it out from under the caller.
  """
  try:
    return message.content or b""
  except (httpx.StreamConsumed, httpx.ResponseNotRead, httpx.RequestNotRead):
    return b""
