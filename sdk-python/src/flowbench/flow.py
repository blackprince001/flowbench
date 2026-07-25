import json
import os

from .context import Context, StepBuilder
from .errors import FlowCompileError
from .ir import build_scenario
from .profile import Profile


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
      builder = StepBuilder(step_id, available_vars)
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
      if effective is not None:
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

  def run(self, profile):
    compiled = self.compile(profile)
    if os.environ.get("FLOWBENCH_COMPILE_ONLY"):
      print(json.dumps(compiled))
      return
    raise NotImplementedError(
      "flow.run() executes at Python concurrency and writes to the run "
      "store (ADR 0012); not implemented until issue #25. Use "
      "flow.compile(profile) to get the IR."
    )
