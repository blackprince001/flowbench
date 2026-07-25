"""Auth schemes, declared on a Flow or a single step.

Each class compiles to the `auth` block of `internal/ir/ir.go`'s Step,
including its `omitempty` rules -- an optional field left unset is omitted
rather than written as its default, so a Python flow and the equivalent YAML
produce the same IR.

Credentials are never literals here any more than they are in YAML: pass
``env("API_TOKEN")`` for a value the process environment holds (ADR 0005),
or a value an earlier step extracted. The engine resolves it at request time
and registers it for redaction.
"""

from dataclasses import dataclass

from .errors import FlowCompileError

_ALGORITHMS = ("sha256", "sha512")
_ENCODINGS = ("hex", "base64")
_LOCATIONS = ("header", "query")


@dataclass
class Bearer:
  """``Authorization: Bearer <token>``, static or extracted at runtime."""

  token: str

  def to_ir(self):
    return {"scheme": "bearer", "token": _required("Bearer.token", self.token)}


@dataclass
class Basic:
  """HTTP basic auth. The engine base64-encodes and redacts the pair."""

  username: str
  password: str

  def to_ir(self):
    return {
      "scheme": "basic",
      "username": _required("Basic.username", self.username),
      "password": _required("Basic.password", self.password),
    }


@dataclass
class ApiKey:
  """An API key in a header (the default) or a query parameter."""

  name: str
  value: str
  location: str = "header"

  def to_ir(self):
    _one_of("ApiKey.location", self.location, _LOCATIONS)
    ir = {
      "scheme": "api_key",
      "name": _required("ApiKey.name", self.name),
      "value": _required("ApiKey.value", self.value),
    }
    # Omitted at the default, matching the YAML surface's optional `in:`.
    if self.location != "header":
      ir["in"] = self.location
    return ir


@dataclass
class Cookie:
  """A session cookie, sent alongside whatever the VU's jar already holds."""

  name: str
  value: str

  def to_ir(self):
    return {
      "scheme": "cookie",
      "name": _required("Cookie.name", self.name),
      "value": _required("Cookie.value", self.value),
    }


@dataclass
class OAuth2ClientCredentials:
  """The client-credentials grant. The token endpoint is fetched once per run
  and shared across VUs, and must sit inside the target's allow-list.

  Authorization-code is out of v1 scope: it needs browser interaction.
  """

  token_url: str
  client_id: str
  client_secret: str
  scopes: list | None = None

  def to_ir(self):
    ir = {
      "scheme": "oauth2_client_credentials",
      "token_url": _required("OAuth2ClientCredentials.token_url", self.token_url),
      "client_id": _required("OAuth2ClientCredentials.client_id", self.client_id),
      "client_secret": _required(
        "OAuth2ClientCredentials.client_secret", self.client_secret
      ),
    }
    if self.scopes:
      ir["scopes"] = [str(s) for s in self.scopes]
    return ir


@dataclass
class Hmac:
  """HMAC request signing.

  ``sign`` is the canonical string, over the placeholders ``{method}``,
  ``{path}``, ``{query}``, ``{body}``, ``{body_sha256}``, ``{timestamp}``,
  and ``{key_id}``; unset, it is
  ``"{method}\\n{path}\\n{timestamp}\\n{body_sha256}"``.
  """

  secret: str
  algorithm: str | None = None
  encoding: str | None = None
  header: str | None = None
  key_id: str | None = None
  key_id_header: str | None = None
  timestamp_header: str | None = None
  sign: str | None = None

  def to_ir(self):
    if self.algorithm is not None:
      _one_of("Hmac.algorithm", self.algorithm, _ALGORITHMS)
    if self.encoding is not None:
      _one_of("Hmac.encoding", self.encoding, _ENCODINGS)

    ir = {"scheme": "hmac", "secret": _required("Hmac.secret", self.secret)}
    for key, value in (
      ("algorithm", self.algorithm),
      ("encoding", self.encoding),
      ("header", self.header),
      ("key_id", self.key_id),
      ("key_id_header", self.key_id_header),
      ("timestamp_header", self.timestamp_header),
      ("sign", self.sign),
    ):
      if value:
        ir[key] = str(value)
    return ir


@dataclass
class NoAuth:
  """Opts one step out of the flow's auth default."""

  def to_ir(self):
    return {"scheme": "none"}


def _required(what, value):
  text = "" if value is None else str(value)
  if not text:
    raise FlowCompileError(f"{what} is required")
  return text


def _one_of(what, value, allowed):
  if value not in allowed:
    raise FlowCompileError(f"{what} must be one of {list(allowed)!r}, got {value!r}")
