class FlowCompileError(Exception):
  """Raised when a flow cannot be compiled to the canonical IR."""


class FlowExecutionError(Exception):
  """Raised when a Python-driven flow fails to execute for real."""
