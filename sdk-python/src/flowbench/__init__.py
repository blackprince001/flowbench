"""Python authoring surface for flowbench: declarative flows that compile to
the canonical IR (ADR 0002), executed by the Go engine at full VU scale.
"""

from .assertions import expect, frame
from .auth import (
  ApiKey,
  Basic,
  Bearer,
  Cookie,
  Hmac,
  NoAuth,
  OAuth2ClientCredentials,
)
from .errors import FlowCompileError, FlowExecutionError
from .flow import Flow
from .profile import Profile
from .redaction import secret
from .retry import Retry
from .template import env

# Kept in lockstep with pyproject.toml's version by hand; releases check it.
__version__ = "0.1.0"

__all__ = [
  "ApiKey",
  "Basic",
  "Bearer",
  "Cookie",
  "Flow",
  "FlowCompileError",
  "FlowExecutionError",
  "Hmac",
  "NoAuth",
  "OAuth2ClientCredentials",
  "Profile",
  "Retry",
  "env",
  "expect",
  "frame",
  "secret",
]
