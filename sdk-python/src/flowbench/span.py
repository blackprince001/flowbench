"""The atomic unit of a live-execution trace tree -- a port of
internal/span/span.go and capture.go, so a Python-produced trace serializes
to the exact JSON shape the run store and results server expect.

Times are held as float seconds internally (Python's natural unit); to_dict
converts to nanosecond integers only at the JSON boundary, matching Go's
time.Duration, which has no custom JSON marshaler and serializes as a plain
int64 of nanoseconds.
"""

OUTCOME_OK = "ok"
OUTCOME_FAILED = "failed"
OUTCOME_THROTTLED = "throttled"
OUTCOME_SKIPPED = "skipped"


def _ns(seconds):
  return round(seconds * 1_000_000_000)


class Span:
  def __init__(self, name, start):
    self.name = name
    self.start = start
    self.duration = 0.0
    self.outcome = OUTCOME_OK
    self.children = []
    self.payload = None

    # Held by reference until finalize() turns them into payload, so a
    # dropped (unsampled) trace costs nothing beyond what call() already
    # spent. Mirrors span.Span's unexported rawReq/rawResp/callMethod/
    # callURL/callStatus/retryAfter/failure fields exactly.
    self._raw_req = None
    self._raw_resp = None
    self._call_method = ""
    self._call_url = ""
    self._call_status = 0
    self._retry_after = ""
    self._failure = ""
    self._observation = None

  def child(self, name, start):
    c = Span(name, start)
    self.children.append(c)
    return c

  def set_raw(self, req, resp):
    self._raw_req, self._raw_resp = req, resp

  def set_call(self, method, url, status, retry_after):
    self._call_method = method
    self._call_url = url
    self._call_status = status
    self._retry_after = retry_after

  def set_failure(self, detail):
    self._failure = detail

  def set_observation(self, prompt, completion, prompt_hash, variant, usage):
    """Records a prompt observation's captured pair and its identity (ADR
    0009). Held by reference like the bodies, but unlike them it is never
    dropped: a diff needs both sides to exist on every iteration.
    """
    self._observation = {
      "prompt": prompt,
      "completion": completion,
      "prompt_hash": prompt_hash,
      "variant": variant,
      "usage": usage,
    }

  def self_time(self):
    self_dur = self.duration
    for c in self.children:
      self_dur -= c.duration
    return max(self_dur, 0.0)

  def to_dict(self):
    d = {
      "Name": self.name,
      "Start": _ns(self.start),
      "Duration": _ns(self.duration),
      "Outcome": self.outcome,
      "Children": [c.to_dict() for c in self.children],
    }
    if self.payload:
      d["Payload"] = self.payload
    return d


def finalize(root, secrets, max_bytes):
  """Turns a kept trace's raw bodies/call-identity/failure into a stored
  Payload dict, redacting first and truncating second -- a port of
  capture.go's Finalize. Call only on traces being kept.
  """
  if root is None:
    return
  if (
    root._call_status != 0
    or root._raw_req is not None
    or root._raw_resp is not None
    or root._failure != ""
    or root._observation is not None
  ):
    payload = {}
    if root._call_method:
      payload["method"] = root._call_method
    if root._call_status:
      payload["status"] = root._call_status
    if root._retry_after:
      payload["retry_after"] = root._retry_after
    if root._raw_req:
      payload["req_bytes"] = len(root._raw_req)
    if root._raw_resp:
      payload["resp_bytes"] = len(root._raw_resp)
    if root._failure:
      payload["failure"] = secrets.redact(root._failure)
    if root._call_url:
      payload["url"] = secrets.redact(root._call_url)
    if max_bytes >= 0:
      request, truncated_req = _cap_body(root._raw_req, secrets, max_bytes)
      response, truncated_resp = _cap_body(root._raw_resp, secrets, max_bytes)
      if request:
        payload["request"] = request
      if response:
        payload["response"] = response
      if truncated_req or truncated_resp:
        payload["truncated"] = True
    if root._observation is not None:
      _add_observation(payload, root._observation, secrets, max_bytes)
    root.payload = payload
    root._raw_req = root._raw_resp = None
    root._failure = ""
    root._observation = None
  for c in root.children:
    finalize(c, secrets, max_bytes)


def _add_observation(payload, observation, secrets, max_bytes):
  """The captured pair, always -- an observation keeps its prompt and
  completion whatever its outcome, because a diff needs both sides (ADR 0009).
  Redaction and the size cap still apply; the cap is the only thing that is
  allowed to shorten them.
  """
  payload["prompt"], truncated_prompt = _cap_text(
    observation["prompt"], secrets, max_bytes
  )
  payload["completion"], truncated_completion = _cap_text(
    observation["completion"], secrets, max_bytes
  )
  payload["prompt_hash"] = observation["prompt_hash"]
  if observation["variant"]:
    payload["variant"] = observation["variant"]
  if observation["usage"]:
    payload["usage"] = observation["usage"]
  if truncated_prompt or truncated_completion:
    payload["truncated"] = True


def _cap_text(text, secrets, max_bytes):
  redacted = secrets.redact(text)
  if max_bytes > 0 and len(redacted.encode("utf-8")) > max_bytes:
    return redacted.encode("utf-8")[:max_bytes].decode("utf-8", errors="replace"), True
  return redacted, False


def _cap_body(data, secrets, max_bytes):
  if not data:
    return "", False
  redacted = secrets.redact_bytes(data)
  if max_bytes > 0 and len(redacted) > max_bytes:
    return redacted[:max_bytes].decode("utf-8", errors="replace"), True
  return redacted.decode("utf-8", errors="replace"), False
