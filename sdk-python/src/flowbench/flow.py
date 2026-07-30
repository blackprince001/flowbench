import csv
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

from .context import Context
from .drivers.live import FlowAbortedError, LiveDriver
from .drivers.trace import TraceDriver
from .errors import FlowCompileError, FlowExecutionError
from .ir import build_scenario
from .profile import Profile
from .redaction import SecretSet, flagged
from .span import OUTCOME_OK, Span
from .store import Sample, prior_identities, write_run
from .target import TargetConfig, resolve_target_via_binary

_LIVE_MODES = ("integration", "system")


class Flow:
  def __init__(self, name, data=None, auth=None):
    self.name = name
    self.data = data
    self.auth = auth  # default every step inherits unless it declares its own
    self._steps = []  # list of (func, retry_or_None, auth_or_None), in order

  def step(self, func=None, *, retry=None, auth=None):
    def register(f):
      self._steps.append((f, retry, auth))
      return f

    if func is not None:
      return register(func)
    return register

  def compile(self, profile=None):
    if profile is None:
      profile = Profile(mode="integration")

    available_vars = set()
    steps = []
    for func, retry, auth in self._steps:
      step_id = func.__name__
      builder = TraceDriver(step_id, available_vars)
      ctx = Context(builder, has_data_pool=self.data is not None)
      func(ctx)

      if builder.spec is None:
        raise FlowCompileError(
          f"step {step_id!r} never made a ctx.http call; "
          "every @flow.step function must make exactly one"
        )

      step = {"id": step_id, "type": builder.kind, builder.kind: builder.spec}
      if builder.extract:
        step["extract"] = builder.extract
      if builder.assert_:
        step["assert"] = builder.assert_
      if retry is not None:
        step["retry"] = retry.to_ir()
      # Flatten the flow-level default onto the step and drop explicit
      # opt-outs, exactly as the YAML parser does (internal/parser's
      # flattenAuth), so both surfaces hand the executor the same IR.
      effective = self.auth if auth is None else auth
      if effective is not None and _makes_request(builder.kind, builder.spec):
        spec = effective.to_ir()
        if spec["scheme"] != "none":
          step["auth"] = spec
      steps.append(step)

    return build_scenario(
      name=self.name,
      data_source=self.data,
      steps=steps,
      profile=profile.to_ir(),
    )

  def run(
    self, profile, *, target="local", targets_dir="targets", base_url=None, store="runs"
  ):
    """Dumps compiled IR (FLOWBENCH_COMPILE_ONLY, the flowbench run
    <file>.py contract) or executes for real -- integration/system only
    (ADR 0012: Python-driven runs are honestly lower-ceiling, not a
    VU-scale scheduler). mode=load/stress/soak needs the Go engine instead.

    Deliberately does NOT compile() first when executing live: compile()
    traces every step function once with symbolic values, so any
    side-effecting code in a step (a counter, a captured list, a log line)
    would run an extra time contaminated with TemplateRef text. Live
    execution has its own equivalent checks (one call per step, enforced by
    LiveDriver; var availability, enforced by get_var) so nothing is lost.
    """
    if os.environ.get("FLOWBENCH_COMPILE_ONLY"):
      print(json.dumps(self.compile(profile)))
      return

    if profile.mode not in _LIVE_MODES:
      raise FlowExecutionError(
        f"flow.run() only executes {_LIVE_MODES!r} directly; "
        f"mode={profile.mode!r} needs the Go engine -- run "
        f"`flowbench run <file>.py` instead"
      )

    cfg = self._resolve_target(target, targets_dir, base_url)
    if profile.mode in cfg.disallowed_modes:
      raise FlowExecutionError(f"target {cfg.name!r} disallows {profile.mode!r} mode")

    rows = self._load_rows()
    main_dir = self._main_dir()
    commit, dirty = _git_info(main_dir, self._main_file())
    initiator = os.environ.get("USER") or os.environ.get("USERNAME") or "unknown"

    started_at = datetime.now(timezone.utc)
    run_start = time.monotonic()
    secrets = SecretSet(flagged())
    roots, samples = [], []
    failed = 0
    # A step's identity is declared -- it is the function's name -- so it is
    # known before anything runs. An observation's is not: it exists because a
    # step's body opened one, under whatever variant label the author's code
    # chose, so the run itself is what discovers it.
    identities = {func.__name__ for func, _, _ in self._steps}

    for i, row in enumerate(rows):
      iter_start = time.monotonic()
      result = self._run_iteration(cfg, row, secrets)
      identities |= result.identities
      dispatch = iter_start - run_start
      service = time.monotonic() - iter_start

      root = Span("flow:" + self.name, dispatch)
      root.duration = service
      root.outcome = result.outcome
      root.children = result.spans
      roots.append(root)
      samples.append(
        Sample(
          flow=self.name,
          actual=dispatch,
          service=service,
          outcome=result.outcome,
          throttled=result.throttled,
        )
      )

      if result.outcome != OUTCOME_OK or result.failures:
        failed += 1
        print(f"  {self.name} [{i + 1}/{len(rows)}]  FAIL")
        for step_id, detail in result.failures:
          print(f"      {step_id}: {detail}")
      else:
        print(f"  {self.name} [{i + 1}/{len(rows)}]  ok")

    iterations = len(samples)
    print(f"{iterations} iteration(s): {iterations - failed} passed, {failed} failed")

    scenario = Path(self._main_file() or f"{self.name}.py").name
    self._warn_on_lost_identities(store, scenario, identities, clean=failed == 0)

    info = {
      "scenario": scenario,
      "identities": identities,
      "mode": profile.mode,
      "initiator": initiator,
      "target": cfg.name,
      "commit": commit,
      "dirty": dirty,
      "started_at": started_at,
      "duration": time.monotonic() - run_start,
    }
    run_dir = write_run(store, info, roots, samples, secrets)
    print(f"run saved to {run_dir}")

  def _warn_on_lost_identities(self, store, scenario, current, clean):
    """Warns when a name earlier runs of this scenario recorded is gone.

    Folding is by structural name (ADR 0007), so renaming a step -- or a
    variant label, which is the same thing one level down: `classify@concise`
    is an identity, not a note on one -- silently splits a flame graph into a
    before and an after. The Go parser warns on the YAML side by comparing
    declared step ids; an observation is declared nowhere, so the comparison
    has to be against what a previous run actually recorded.

    Only a clean run is grounds for the warning. A run that failed part way
    stopped short of identities it would otherwise have reached, and reporting
    those as renames would be blaming the author for the failure they are
    already looking at.
    """
    if not clean:
      return
    prior = prior_identities(store, scenario)
    if prior is None:
      return
    for name in sorted(prior - current):
      print(
        f"flowbench: warning: {scenario} no longer records {name!r}; prior "
        "runs reference that name, so cross-run folding for it will break -- "
        "if this is a rename, flame data continuity is lost",
        file=sys.stderr,
      )

  def _run_iteration(self, cfg, row, secrets):
    driver = LiveDriver(cfg, has_data_pool=self.data is not None, secrets=secrets)
    driver.set_row(row)
    try:
      for func, retry, auth in self._steps:
        # Auth schemes and GraphQL steps compile fine (both surfaces produce
        # the same IR either way) but LiveDriver doesn't apply/execute them
        # yet -- fail loud here rather than silently sending an
        # unauthenticated request or crashing deep inside ctx.graphql().
        effective_auth = self.auth if auth is None else auth
        if effective_auth is not None and effective_auth.to_ir()["scheme"] != "none":
          raise FlowExecutionError(
            f"step {func.__name__!r} declares auth, which live execution does "
            "not yet apply -- run `flowbench run <file>.py` instead (the Go "
            "engine supports every auth scheme)"
          )

        driver.begin_step(func.__name__, retry)
        ctx = Context(driver, has_data_pool=self.data is not None)
        try:
          func(ctx)
        except FlowAbortedError:
          driver.end_step()
          break
        else:
          driver.end_step()
    finally:
      driver.close()
    return driver.result()

  def _resolve_target(self, target, targets_dir, base_url):
    if base_url is not None:
      print(
        "flowbench: base_url set directly -- no host allow-list enforced",
        file=sys.stderr,
      )
      return TargetConfig(name="(explicit base_url)", base_urls=[base_url])
    return resolve_target_via_binary(target, targets_dir)

  def _load_rows(self):
    if self.data is None:
      return [None]
    path = self._main_dir() / self.data
    with path.open(newline="") as f:
      return list(csv.DictReader(f))

  def _main_file(self):
    main_mod = sys.modules.get("__main__")
    return getattr(main_mod, "__file__", None)

  def _main_dir(self):
    main_file = self._main_file()
    if main_file:
      return Path(main_file).resolve().parent
    return Path.cwd()


def _makes_request(kind, spec):
  """Mirrors ir.Step.MakesRequest (internal/ir/validate.go): whether the step
  puts a request on the wire, and so whether a flow-level auth default has
  anything to attach itself to.

  A ws step counts only when it opens the session — credentials ride on the
  handshake, and a step working on a session someone else opened has no
  request left to decorate.
  """
  if kind == "ws":
    return "url" in spec
  return kind in ("call", "graphql", "grpc", "poll")


def _git_info(directory, file):
  """Mirrors cmd/flowbench/attribution.go's gitInfo: the HEAD commit of the
  flow file's repository and whether the flow file has uncommitted changes.
  Both are empty/false outside a git repo.
  """
  try:
    commit_proc = subprocess.run(
      ["git", "-C", str(directory), "rev-parse", "HEAD"],
      capture_output=True,
      text=True,
      check=False,
    )
  except FileNotFoundError:
    return "", False
  if commit_proc.returncode != 0:
    return "", False
  commit = commit_proc.stdout.strip()

  args = ["git", "-C", str(directory), "status", "--porcelain"]
  if file:
    args += ["--", file]
  status_proc = subprocess.run(args, capture_output=True, text=True, check=False)
  dirty = status_proc.returncode == 0 and status_proc.stdout.strip() != ""
  return commit, dirty
