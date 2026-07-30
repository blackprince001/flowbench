"""The package's version, on its own so any module can read it without
importing the package root -- drivers do, to identify themselves to a target,
and the root imports them.

Kept in lockstep with pyproject.toml's version by hand; releases check it.
"""

__version__ = "0.1.1"
