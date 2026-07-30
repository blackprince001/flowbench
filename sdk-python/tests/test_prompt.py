"""Prompt observation (issue #43, ADR 0009).

The LLM call under test is made the way a team's own code makes it: a client
this module builds and FlowBench never sees, posting to an OpenAI-compatible
stub. Nothing here imports a provider SDK -- that is the point of observing
rather than owning, and a test that needed the real SDK would be testing the
SDK.
"""

import asyncio
import contextlib
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import httpx
import pytest

from flowbench import Flow, Profile, expect, secret
from flowbench.context import Context
from flowbench.drivers.live import FlowAbortedError, LiveDriver
from flowbench.errors import FlowCompileError, FlowExecutionError
from flowbench.prompt import (
  hash_prompt,
  normalize_usage,
  parse_pace,
  render,
  reset_pacing,
  wait_for_pace,
)
from flowbench.redaction import PLACEHOLDER, SecretSet
from flowbench.span import finalize
from flowbench.target import TargetConfig

MESSAGES = [
  {"role": "system", "content": "Classify the ticket."},
  {"role": "user", "content": "my card was charged twice"},
]


class _Handler(BaseHTTPRequestHandler):
  def log_message(self, *args):
    pass

  def do_POST(self):
    length = int(self.headers.get("Content-Length", 0))
    self.rfile.read(length)
    status, body = self.server.reply
    payload = json.dumps(body).encode()
    self.send_response(status)
    self.send_header("Content-Length", str(len(payload)))
    self.end_headers()
    self.wfile.write(payload)


def completion(content="refund_request", prompt_tokens=11, completion_tokens=3):
  return {
    "id": "chatcmpl-1",
    "object": "chat.completion",
    "model": "gpt-4o-mini",
    "choices": [
      {
        "index": 0,
        "message": {"role": "assistant", "content": content},
        "finish_reason": "stop",
      }
    ],
    "usage": {
      "prompt_tokens": prompt_tokens,
      "completion_tokens": completion_tokens,
      "total_tokens": prompt_tokens + completion_tokens,
    },
  }


@pytest.fixture(autouse=True)
def fresh_pacing():
  reset_pacing()
  yield
  reset_pacing()


@pytest.fixture
def provider():
  srv = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
  srv.reply = (200, completion())
  thread = threading.Thread(target=srv.serve_forever, daemon=True)
  thread.start()
  yield srv
  srv.shutdown()
  thread.join()


@pytest.fixture
def provider_url(provider):
  return f"http://127.0.0.1:{provider.server_address[1]}"


@pytest.fixture
def cfg(provider_url):
  return TargetConfig(name="test", base_urls=[provider_url])


class Client:
  """Stands in for the provider's SDK: a client built outside any flow, whose
  requests FlowBench only learns about through auto-instrumentation.
  """

  def __init__(self, base_url):
    self._http = httpx.Client(base_url=base_url)

  def create(self, messages, model="gpt-4o-mini"):
    r = self._http.post(
      "/v1/chat/completions", json={"model": model, "messages": messages}
    )
    return r.json()

  def close(self):
    self._http.close()


@pytest.fixture
def client(provider_url):
  c = Client(provider_url)
  yield c
  c.close()


def run_step(cfg, func):
  driver = LiveDriver(cfg, has_data_pool=False)
  driver.begin_step("step", None)
  with contextlib.suppress(FlowAbortedError):
    func(Context(driver, has_data_pool=False))
  driver.end_step()
  driver.close()
  return driver.result()


def find(span, name):
  for child in span.children:
    if child.name == name:
      return child
  raise AssertionError(f"no {name!r} among {[c.name for c in span.children]}")


# -- the acceptance --------------------------------------------------------


def test_every_iteration_records_the_pair_hash_and_token_counts(
  client, provider_url, tmp_path
):
  csv_path = tmp_path / "tickets.csv"
  csv_path.write_text("text\ncharged twice\nwhere is my order\n")

  flow = Flow("triage", data=str(csv_path))

  @flow.step
  def classify(ctx):
    messages = [
      {"role": "system", "content": "Classify the ticket."},
      {"role": "user", "content": str(ctx.user["text"])},
    ]
    with ctx.prompt("classify", template="Classify the ticket.") as p:
      reply = client.create(messages)
      p.record(
        messages,
        reply["choices"][0]["message"]["content"],
        usage=reply["usage"],
      )

  flow.run(
    Profile(mode="integration"), base_url=provider_url, store=str(tmp_path / "store")
  )

  traces = json.loads((tmp_path / "store").glob("*/traces.json").__next__().read_text())
  assert len(traces) == 2
  hashes = set()
  for trace in traces:
    observation = trace["Children"][0]["Children"][0]
    assert observation["Name"] == "classify"
    payload = observation["Payload"]
    assert json.loads(payload["prompt"])[1]["content"] in (
      "charged twice",
      "where is my order",
    )
    assert payload["completion"] == "refund_request"
    assert payload["usage"] == {
      "prompt_tokens": 11,
      "completion_tokens": 3,
      "total_tokens": 14,
    }
    hashes.add(payload["prompt_hash"])

  # One template, one identity: the iterations differ in what was substituted
  # into the prompt, not in the prompt itself.
  assert len(hashes) == 1


def test_provider_429_classifies_as_throttled(client, provider, provider_url, tmp_path):
  provider.reply = (429, {"error": {"message": "rate limit"}})
  flow = Flow("triage")

  @flow.step
  def classify(ctx):
    with ctx.prompt("classify") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply.get("error", {}).get("message", ""))

  flow.run(
    Profile(mode="integration"), base_url=provider_url, store=str(tmp_path / "store")
  )

  meta = json.loads((tmp_path / "store" / "index.json").read_text())[0]
  assert meta["throttle_rate"] == 1.0
  assert meta["error_rate"] == 0.0

  trace = json.loads((tmp_path / "store").glob("*/traces.json").__next__().read_text())[
    0
  ]
  observation = trace["Children"][0]["Children"][0]
  assert observation["Outcome"] == "throttled"
  # And it still captured the pair -- a throttled observation is one whose
  # completion a reader most wants to see.
  assert observation["Payload"]["prompt_hash"]


# -- span shape ------------------------------------------------------------


def test_the_sdks_http_resolves_beneath_the_observation(cfg, client):
  def step(ctx):
    with ctx.prompt("classify") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])

  step_span = run_step(cfg, step).spans[0]
  observation = find(step_span, "classify")
  call = find(observation, "POST /v1/chat/completions")
  leg = find(call, "http_call")
  assert "ttfb" in [c.name for c in leg.children]
  assert observation.duration > 0


def test_a_variant_label_gives_the_observation_its_own_identity(cfg, client):
  def step(ctx):
    with ctx.prompt("classify", variant="concise") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])

  observation = find(run_step(cfg, step).spans[0], "classify@concise")
  finalize(observation, SecretSet(), 2048)
  # Carried in the payload as well as in the name, so a reader groups by it
  # without re-deriving it from the span path.
  assert observation.payload["variant"] == "concise"


def test_an_async_provider_client_resolves_beneath_the_observation(cfg, provider_url):
  """AsyncOpenAI and its equivalents are how a lot of provider code is
  written, so the observation has to parent a call it never sees being made on
  a coroutine either.
  """

  def step(ctx):
    async def ask():
      async with httpx.AsyncClient(base_url=provider_url) as http:
        r = await http.post("/v1/chat/completions", json={"messages": MESSAGES})
        return r.json()

    with ctx.prompt("classify") as p:
      reply = asyncio.run(ask())
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])

  observation = find(run_step(cfg, step).spans[0], "classify")
  call = find(observation, "POST /v1/chat/completions")
  assert "ttfb" in [c.name for c in find(call, "http_call").children]


def test_a_batch_of_concurrent_calls_lands_under_one_observation(cfg, provider_url):
  def step(ctx):
    async def ask_all():
      async with httpx.AsyncClient(base_url=provider_url) as http:
        replies = await asyncio.gather(
          *(
            http.post("/v1/chat/completions", json={"messages": MESSAGES})
            for _ in range(3)
          )
        )
        return [r.json() for r in replies]

    with ctx.prompt("classify") as p:
      replies = asyncio.run(ask_all())
      p.record(MESSAGES, [r["choices"][0]["message"]["content"] for r in replies])

  observation = find(run_step(cfg, step).spans[0], "classify")
  assert [c.name for c in observation.children] == ["POST /v1/chat/completions"] * 3
  assert all(find(c, "http_call") for c in observation.children)


def test_two_observations_in_one_step_are_two_spans(cfg, client):
  def step(ctx):
    for _ in range(2):
      with ctx.prompt("classify") as p:
        reply = client.create(MESSAGES)
        p.record(MESSAGES, reply["choices"][0]["message"]["content"])

  step_span = run_step(cfg, step).spans[0]
  assert [c.name for c in step_span.children] == ["classify", "classify"]


def test_an_observation_earns_the_step_its_span_without_http(cfg):
  """A model that answers without an HTTP request the instrumentation can see
  -- a `requests`-based client, something local -- still did the work.
  """

  def step(ctx):
    with ctx.prompt("classify") as p:
      p.record("classify this", "refund_request")

  assert run_step(cfg, step).outcome == "ok"


# -- identity --------------------------------------------------------------


def test_the_hash_follows_the_template_when_there_is_one():
  a = hash_prompt("Classify: {text}", "Classify: charged twice")
  b = hash_prompt("Classify: {text}", "Classify: where is my order")
  assert a == b
  assert a != hash_prompt("Classify tersely: {text}", "Classify: charged twice")


def test_the_hash_follows_the_content_when_there_is_no_template():
  assert hash_prompt(None, "one") != hash_prompt(None, "two")
  assert hash_prompt(None, "one") == hash_prompt(None, "one")


def test_a_structured_prompt_renders_as_json():
  assert json.loads(render(MESSAGES))[0]["role"] == "system"
  assert render("plain") == "plain"


# -- token counts ----------------------------------------------------------


def test_usage_reads_either_providers_vocabulary():
  openai = {"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14}
  assert normalize_usage(openai) == openai

  class AnthropicUsage:
    input_tokens = 11
    output_tokens = 3

  assert normalize_usage(AnthropicUsage()) == {
    "prompt_tokens": 11,
    "completion_tokens": 3,
    "total_tokens": 14,
  }
  assert normalize_usage(None) is None


def test_usage_that_carries_no_counts_is_refused():
  with pytest.raises(FlowExecutionError, match=r"token counts"):
    normalize_usage(object())


# -- pacing ----------------------------------------------------------------


def test_pace_spaces_calls_after_the_burst_allowance():
  slept, now = [], [0.0]

  def sleep(seconds):
    slept.append(seconds)
    now[0] += seconds

  def clock():
    return now[0]

  # 60/m is one per second, and burst=2 lets the first two go unspaced.
  for _ in range(2):
    assert wait_for_pace("classify", "60/m", 2, sleep=sleep, clock=clock) == 0.0
  assert wait_for_pace(
    "classify", "60/m", 2, sleep=sleep, clock=clock
  ) == pytest.approx(1.0)
  assert slept == [pytest.approx(1.0)]


def test_pace_is_keyed_by_observation_name():
  now = [0.0]
  args = {"sleep": lambda s: now.__setitem__(0, now[0] + s), "clock": lambda: now[0]}
  assert wait_for_pace("classify", "60/m", 1, **args) == 0.0
  # A different observation has its own allowance.
  assert wait_for_pace("summarize", "60/m", 1, **args) == 0.0
  assert wait_for_pace("classify", "60/m", 1, **args) > 0


def test_pace_grammar():
  assert parse_pace("20/m", 1) == (pytest.approx(20 / 60), 1.0)
  assert parse_pace("5/s", 1) == (5.0, 1.0)
  assert parse_pace("100/2h", 1)[0] == pytest.approx(100 / 7200)
  with pytest.raises(FlowCompileError, match=r'must look like "20/m"'):
    parse_pace("20 per minute", 1)
  with pytest.raises(FlowCompileError, match=r"positive integer"):
    parse_pace("20/m", 0)


def test_a_paced_wait_is_its_own_span_beside_the_observation(cfg, client):
  def step(ctx):
    for _ in range(2):
      # 600/m is one per 100ms: the second observation waits for it.
      with ctx.prompt("classify", pace="600/m") as p:
        reply = client.create(MESSAGES)
        p.record(MESSAGES, reply["choices"][0]["message"]["content"])

  started = time.monotonic()
  step_span = run_step(cfg, step).spans[0]
  assert time.monotonic() - started >= 0.05

  assert [c.name for c in step_span.children] == ["classify", "pace", "classify"]
  pace_span = find(step_span, "pace")
  assert pace_span.duration > 0
  # The wait is the flow's own, so it stays out of the observation's latency.
  assert find(step_span, "classify").duration < pace_span.duration + 1


def test_burst_without_a_pace_is_refused(cfg):
  def step(ctx):
    ctx.prompt("classify", burst=3)

  with pytest.raises(FlowCompileError, match=r"allowance against a pace"):
    run_step(cfg, step)


# -- timeouts --------------------------------------------------------------


def test_an_overrun_timeout_fails_the_observation(cfg, client):
  def step(ctx):
    with ctx.prompt("classify", timeout="1ns") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])

  result = run_step(cfg, step)
  assert result.outcome == "failed"
  _, detail = result.failures[0]
  assert "over its 1e-09s timeout" in detail
  # Captured all the same: the completion is what you look at to see why it
  # was slow.
  observation = find(result.spans[0], "classify")
  finalize(observation, SecretSet(), 2048)
  assert observation.payload["completion"] == "refund_request"


def test_a_timeout_within_budget_passes(cfg, client):
  def step(ctx):
    with ctx.prompt("classify", timeout=30) as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])

  assert run_step(cfg, step).outcome == "ok"


# -- failure modes ---------------------------------------------------------


def test_an_observation_that_records_nothing_is_an_error(cfg, client):
  def step(ctx):
    with ctx.prompt("classify"):
      client.create(MESSAGES)

  with pytest.raises(FlowExecutionError, match=r"recorded nothing"):
    run_step(cfg, step)


def test_recording_twice_is_an_error(cfg, client):
  def step(ctx):
    with ctx.prompt("classify") as p:
      p.record(MESSAGES, "one")
      p.record(MESSAGES, "two")

  with pytest.raises(FlowExecutionError, match=r"recorded twice"):
    run_step(cfg, step)


def test_the_providers_own_exception_becomes_a_recorded_failure(cfg):
  def step(ctx):
    with ctx.prompt("classify") as _:
      raise RuntimeError("connection reset by peer")

  result = run_step(cfg, step)
  assert result.outcome == "failed"
  assert "classify: RuntimeError: connection reset by peer" in result.failures[0][1]


def test_an_observation_name_must_be_span_safe(cfg):
  with pytest.raises(FlowCompileError, match=r"reserved for span names"):
    run_step(cfg, lambda ctx: ctx.prompt("classify.v2"))
  with pytest.raises(FlowCompileError, match=r"reserved for span names"):
    run_step(cfg, lambda ctx: ctx.prompt("classify@concise"))
  with pytest.raises(FlowCompileError, match=r"variant"):
    run_step(cfg, lambda ctx: ctx.prompt("classify", variant="v.2"))


# -- assertions on the completion ------------------------------------------


def test_expect_asserts_on_a_recorded_completion(cfg, client):
  def step(ctx):
    with ctx.prompt("classify") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])
    expect(p.completion).to_contain("refund")
    expect(p.completion).to_match(r"^refund_")

  step_span = run_step(cfg, step).spans[0]
  assert "assert_completion" in [c.name for c in step_span.children]


def test_a_failed_completion_assertion_fails_the_step(cfg, client):
  def step(ctx):
    with ctx.prompt("classify") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])
    expect(p.completion).to_contain("cancellation")

  result = run_step(cfg, step)
  assert result.outcome == "failed"
  assert "completion" in result.failures[0][1]


def test_a_recorded_completion_is_an_ordinary_string(cfg, client):
  seen = {}

  def step(ctx):
    with ctx.prompt("classify") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])
    seen["startswith"] = p.completion.startswith("refund")
    seen["in"] = "refund" in p.completion
    seen["json"] = json.loads(p.prompt)[0]["role"]

  run_step(cfg, step)
  assert seen == {"startswith": True, "in": True, "json": "system"}


# -- redaction -------------------------------------------------------------


def test_a_flagged_value_inside_a_prompt_is_redacted(client, provider_url, tmp_path):
  key = "sk-live-8b1d2f0a"
  secret(key)
  flow = Flow("triage")

  @flow.step
  def classify(ctx):
    messages = [{"role": "user", "content": f"my api key is {key}"}]
    with ctx.prompt("classify") as p:
      reply = client.create(messages)
      p.record(messages, reply["choices"][0]["message"]["content"])

  flow.run(
    Profile(mode="integration"), base_url=provider_url, store=str(tmp_path / "store")
  )

  artifacts = "".join(p.read_text() for p in (tmp_path / "store").rglob("*.json"))
  assert key not in artifacts
  assert PLACEHOLDER in artifacts


# -- variants (issue #44) --------------------------------------------------

CONCISE = "Classify in one word."
VERBOSE = "Classify the ticket, explaining your reasoning."


def variant_flow(client, provider):
  """One observation recorded under two labels. What varies is the flow's own
  code -- a different system prompt -- which is the whole of what a variant
  is: the label only gives that version its own identity.
  """
  flow = Flow("triage")

  @flow.step
  def classify(ctx):
    for label, system in (("concise", CONCISE), ("verbose", VERBOSE)):
      messages = [{"role": "system", "content": system}, *MESSAGES[1:]]
      provider.reply = (200, completion(content=f"refund_{label}"))
      with ctx.prompt("classify", template=system, variant=label) as p:
        reply = client.create(messages)
        p.record(messages, reply["choices"][0]["message"]["content"])

  return flow


def test_two_variants_are_two_identities_in_the_run_store(
  client, provider, provider_url, tmp_path
):
  store = tmp_path / "store"
  variant_flow(client, provider).run(
    Profile(mode="integration"), base_url=provider_url, store=str(store)
  )

  trace = json.loads(next(store.glob("*/traces.json")).read_text())[0]
  observations = {c["Name"]: c["Payload"] for c in trace["Children"][0]["Children"]}
  assert set(observations) == {"classify@concise", "classify@verbose"}

  concise, verbose = observations["classify@concise"], observations["classify@verbose"]
  assert concise["completion"] == "refund_concise"
  assert verbose["completion"] == "refund_verbose"
  assert concise["variant"] == "concise"
  assert verbose["variant"] == "verbose"
  # Each variant's own prompt, so each gets its own identity: without this a
  # diff could not tell the two versions apart at all.
  assert concise["prompt_hash"] != verbose["prompt_hash"]
  assert concise["prompt_hash"] == hash_prompt(CONCISE, "")

  # And they fold apart, which is what keeps per-variant metrics per-variant.
  folded = json.loads(next(store.glob("*/folded.json")).read_text())
  step = folded["root"]["children"]["flow:triage"]["children"]["classify"]
  assert {"classify@concise", "classify@verbose"} <= set(step["children"])
  assert step["children"]["classify@concise"]["count"] == 1


def test_a_run_records_the_identities_it_used(client, provider, provider_url, tmp_path):
  store = tmp_path / "store"
  variant_flow(client, provider).run(
    Profile(mode="integration"), base_url=provider_url, store=str(store)
  )

  meta = json.loads((store / "index.json").read_text())[0]
  assert meta["identities"] == [
    "classify",
    "classify.classify@concise",
    "classify.classify@verbose",
  ]


def test_a_renamed_variant_warns_that_folding_will_break(
  client, provider, provider_url, tmp_path, capsys
):
  store = tmp_path / "store"
  profile = Profile(mode="integration")
  variant_flow(client, provider).run(profile, base_url=provider_url, store=str(store))
  capsys.readouterr()

  renamed = Flow("triage")

  @renamed.step
  def classify(ctx):
    for label, system in (("terse", CONCISE), ("verbose", VERBOSE)):
      messages = [{"role": "system", "content": system}, *MESSAGES[1:]]
      with ctx.prompt("classify", template=system, variant=label) as p:
        reply = client.create(messages)
        p.record(messages, reply["choices"][0]["message"]["content"])

  renamed.run(profile, base_url=provider_url, store=str(store))

  err = capsys.readouterr().err
  assert "classify.classify@concise" in err
  assert "cross-run folding for it will break" in err
  # The one that did not move is not reported.
  assert "classify@verbose" not in err


def test_an_unchanged_run_warns_about_nothing(
  client, provider, provider_url, tmp_path, capsys
):
  store = tmp_path / "store"
  profile = Profile(mode="integration")
  for _ in range(2):
    variant_flow(client, provider).run(profile, base_url=provider_url, store=str(store))
  assert "warning" not in capsys.readouterr().err


def test_a_failed_run_is_not_read_as_a_rename(
  client, provider, provider_url, tmp_path, capsys
):
  """A run that stopped early never reached identities it would otherwise
  have recorded, and calling those renames would blame the author for the
  failure they are already looking at.
  """
  store = tmp_path / "store"
  profile = Profile(mode="integration")
  variant_flow(client, provider).run(profile, base_url=provider_url, store=str(store))
  capsys.readouterr()

  broken = Flow("triage")

  @broken.step
  def classify(ctx):
    with ctx.prompt("classify", variant="concise") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])
    expect(p.completion).to_be("something else entirely")

  broken.run(profile, base_url=provider_url, store=str(store))
  assert "warning" not in capsys.readouterr().err


# -- capture policy --------------------------------------------------------


def test_the_pair_survives_the_capture_policy_that_drops_bodies(cfg, client):
  """The carve-out (ADR 0009): a run capturing no bodies still keeps its
  observations, because a diff has nothing left if it does not.
  """

  def step(ctx):
    with ctx.prompt("classify") as p:
      reply = client.create(MESSAGES)
      p.record(MESSAGES, reply["choices"][0]["message"]["content"])

  observation = find(run_step(cfg, step).spans[0], "classify")
  finalize(observation, SecretSet(), -1)
  assert observation.payload["completion"] == "refund_request"
  assert "response" not in observation.payload["prompt"]


def test_an_oversized_pair_is_capped_and_says_so(cfg):
  def step(ctx):
    with ctx.prompt("classify") as p:
      p.record("x" * 50, "y" * 5000)

  observation = find(run_step(cfg, step).spans[0], "classify")
  finalize(observation, SecretSet(), 2048)
  assert len(observation.payload["completion"]) == 2048
  assert observation.payload["prompt"] == "x" * 50
  assert observation.payload["truncated"] is True


# -- the other surface -----------------------------------------------------


def test_compiling_a_flow_that_observes_a_prompt_is_refused():
  flow = Flow("triage")

  @flow.step
  def classify(ctx):
    with ctx.prompt("classify") as p:
      p.record("hello", "world")

  with pytest.raises(FlowCompileError, match=r"only the Python-driven path"):
    flow.compile(Profile(mode="integration"))
