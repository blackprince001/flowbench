"""SDK-side HTTP auto-instrumentation for the Python-driven path (ADR 0012).

The Go engine owns its HTTP client, so internal/adapters/http.go can hang a
httptrace recorder off every request and emit phase spans. A Python-driven
flow does not: the calls that matter are made by the team's own code and by
whatever SDK it already uses -- the OpenAI client builds its own httpx.Client
and never hears of FlowBench. Without instrumentation those calls are invisible
and Python is an opaque blob in the flame graph.

So the hook goes on the class, not on a client we hand out: httpx.Client.send
and httpx.AsyncClient.send are patched once, and every request made while a
driver is attached is recorded. Requests made with no driver attached pass
through untouched, which is what keeps `import flowbench` from instrumenting an
unrelated process.

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

Async is recorded, and concurrency is why the bookkeeping is shaped the way it
is. An `AsyncOpenAI` client, or a step that gathers a batch of calls, has
several requests in flight at once, so neither the parent of a request nor the
owner of an arriving phase event can be "the most recent one": both live in a
ContextVar (`_current_call`), and each request's trace callback is bound to its
own frame. A task copies the context when it is created, so sibling coroutines
cannot see each other's frames, while a request genuinely made inside another
still nests. What concurrency does change is the reading: overlapping siblings
can sum to more than the parent's own duration, so a flame graph over a batched
step shows where the time went, not a timeline you can lay a ruler against.

Two limits worth naming. `requests` and other non-httpx clients pass through
unrecorded -- the hook is httpx's. And the active driver is held in a
ContextVar, which a new *thread* does not inherit (unlike a task), so a call
made on a thread the step spawned is not recorded -- the safe direction to
fail, since the span tree is built without locks.
"""

import contextvars
import re

import httpx

from .span import OUTCOME_FAILED, OUTCOME_THROTTLED

_LEG = "http_call"

# The driver whose step spans requests attach to, or None outside a run.
_active = contextvars.ContextVar("flowbench_instrumentation", default=None)

# The request currently in flight *in this context*, so a request made while
# another is open nests beneath it. A ContextVar rather than a stack because
# under asyncio several requests are in flight at once and a stack would hand
# each one whichever request started last: a task copies the context when it is
# created, so concurrent requests cannot see each other's frames, while a
# strictly nested sync call still finds its parent.
_current_call = contextvars.ContextVar("flowbench_current_call", default=None)

_original_send = None
_original_async_send = None


def install():
  """Patches httpx.Client.send and httpx.AsyncClient.send once per process.
  Idempotent, and a no-op at request time until a driver attaches, so importing
  flowbench does not change the behavior of a process that never runs a flow.
  """
  global _original_send, _original_async_send
  if _original_send is not None:
    return
  _original_send = httpx.Client.send
  _original_async_send = httpx.AsyncClient.send

  def send(client, request, **kwargs):
    instr = _active.get()
    if instr is None:
      return _original_send(client, request, **kwargs)
    return instr.record(client, request, kwargs, _original_send)

  async def asend(client, request, **kwargs):
    instr = _active.get()
    if instr is None:
      return await _original_async_send(client, request, **kwargs)
    return await instr.record_async(client, request, kwargs, _original_async_send)

  httpx.Client.send = send
  httpx.AsyncClient.send = asend


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

  def enter_scope(self, span):
    """Parents recorded calls to `span` instead of the step, and returns the
    previous parent for exit_scope. A prompt observation uses it so the
    requests the provider's SDK makes hang beneath the observation rather than
    beside it.
    """
    previous, self._parent = self._parent, span
    return previous

  def exit_scope(self, previous):
    self._parent = previous

  def bind_next(self, span):
    """Points the next request at an existing span instead of a fresh call
    span. ctx.http uses it so its call keeps the Go engine's exact shape --
    the step (or retry attempt) span *is* the call span, with legs directly
    beneath it -- while an SDK-made call gets a call span of its own.
    """
    self._bound = span

  # -- request recording ---------------------------------------------------
  #
  # Split into open/close halves because a sync send returns a response and an
  # async one returns an awaitable: everything either path does around the call
  # is identical, and the two record methods differ only in the `await`.

  def record(self, client, request, kwargs, send):
    frame = self._open(request, is_async=False)
    if frame is None:
      return send(client, request, **kwargs)
    try:
      response = send(client, request, **kwargs)
    except Exception as e:
      self._failed(frame, request, e)
      raise
    finally:
      self._close(frame)
    self._captured(frame, request, response)
    return response

  async def record_async(self, client, request, kwargs, send):
    frame = self._open(request, is_async=True)
    if frame is None:
      return await send(client, request, **kwargs)
    try:
      response = await send(client, request, **kwargs)
    except Exception as e:
      self._failed(frame, request, e)
      raise
    finally:
      self._close(frame)
    self._captured(frame, request, response)
    return response

  def _open(self, request, is_async):
    """Starts recording one request, or returns None for one that belongs to
    nobody -- a call made outside a step, which passes through untouched.
    """
    in_flight = _current_call.get()
    parent = in_flight.span if in_flight is not None else self._parent
    bound, self._bound = self._bound, None
    if parent is None and bound is None:
      return None

    if bound is not None:
      call = bound
    else:
      call = parent.child(_call_name(request), self._elapsed())
      self.recorded += 1

    frame = _Call(call, self._elapsed, bound=bound is not None)
    frame.token = _current_call.set(frame)
    # Bound to this request's own frame rather than to the Instrumentation, so
    # a phase event can only ever reach the request it came from. Under asyncio
    # that is the whole ballgame: two responses arrive interleaved and each
    # one's headers must close its own ttfb.
    _chain_trace(request, frame.atrace if is_async else frame.trace, is_async)
    return frame

  def _close(self, frame):
    # Mirrors http.go's deferred finish(): after this a trace event that
    # arrives late -- a streamed body closed later, a speculative dial --
    # finds a finished frame and mutates nothing.
    _current_call.reset(frame.token)
    frame.close_leg(self._elapsed())
    frame.done = True
    # Measured from the span's own start, so a bound step span covers the
    # Python that ran before the call too -- otherwise the waterfall would
    # draw the bar earlier than the request it represents.
    frame.span.duration = self._elapsed() - frame.span.start

  def _failed(self, frame, request, error):
    frame.span.outcome = OUTCOME_FAILED
    frame.span.set_failure(f"call failed: {request.method} {request.url}: {error}")

  def _captured(self, frame, request, response):
    call = frame.span
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
    if not frame.bound and response.status_code == 429:
      call.outcome = OUTCOME_THROTTLED
      self.throttled = True


class _Call:
  """One request in flight, and the leg it is currently on. Leg state belongs
  to the call rather than to the Instrumentation because a request can start
  while another is still open -- a streamed response the caller has not closed
  yet, or simply another coroutine's request -- and the phases of one must not
  land on the other's leg.
  """

  def __init__(self, span, elapsed, bound=False):
    self.span = span
    self.bound = bound  # the span was handed to us (ctx.http), not created
    self.leg = None
    self.marks = {}
    self.done = False
    self.token = None
    self._elapsed = elapsed

  def trace(self, name, info):
    """httpcore's trace hook, for this request alone -- a port of http.go's
    legRecorder.
    """
    if self.done:
      return
    parts = name.split(".", 2)
    handler = _PHASES.get(tuple(parts[1:])) if len(parts) == 3 else None
    if handler is not None:
      handler(self, self._elapsed())

  async def atrace(self, name, info):
    """The same hook for the async transport, which requires a coroutine and
    refuses a plain function.
    """
    self.trace(name, info)

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


def _chain_trace(request, trace, is_async):
  """Installs the phase callback without stealing a trace extension the
  caller already set -- theirs still fires, ours runs alongside it. The async
  transport requires a coroutine and refuses a plain function, so the chained
  pair has to match the interface the request will travel on.
  """
  existing = request.extensions.get("trace")
  if existing is None:
    request.extensions["trace"] = trace
    return

  if is_async:

    async def both(name, info):
      await trace(name, info)
      return await existing(name, info)

  else:

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
