import json
import os

from .context import Context
from .drivers.trace import TraceDriver
from .errors import FlowCompileError
from .ir import build_scenario
from .profile import Profile


class Flow:
  def __init__(self, name, data=None):
    self.name = name
    self.data = data
    self._steps = []  # list of (func, retry_or_None), in registration order

  def step(self, func=None, *, retry=None):
    def register(f):
      self._steps.append((f, retry))
      return f

    if func is not None:
      return register(func)
    return register

  def compile(self, profile=None):
    if profile is None:
      profile = Profile(mode="integration")

    available_vars = set()
    steps = []
    for func, retry in self._steps:
      step_id = func.__name__
      builder = TraceDriver(step_id, available_vars)
      ctx = Context(builder, has_data_pool=self.data is not None)
      func(ctx)

      if builder.call_spec is None:
        raise FlowCompileError(
          f"step {step_id!r} never made a ctx.http call; "
          "every @flow.step function must make exactly one"
        )

      step = {"id": step_id, "type": "call", "call": builder.call_spec}
      if builder.extract:
        step["extract"] = builder.extract
      if builder.assert_:
        step["assert"] = builder.assert_
      if retry is not None:
        step["retry"] = retry.to_ir()
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
