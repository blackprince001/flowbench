// Package ir defines the canonical flow IR: the single structure both
// authoring surfaces compile to and the only input the executor accepts.
package ir

import (
	"encoding/json"
	"fmt"
	"strings"
)

type StepType string

const (
	StepCall    StepType = "call"
	StepGraphQL StepType = "graphql"
	StepWS      StepType = "ws"
	StepGRPC    StepType = "grpc"
	StepWait    StepType = "wait"
	StepPoll    StepType = "poll"
)

type Mode string

const (
	ModeIntegration Mode = "integration"
	ModeSystem      Mode = "system"
	ModeLoad        Mode = "load"
	ModeStress      Mode = "stress"
	ModeSoak        Mode = "soak"
)

type FailureAction string

const (
	FailureAbortFlow FailureAction = "abort_flow"
	FailureAbortRun  FailureAction = "abort_run"
	FailureRecord    FailureAction = "record"
)

type CaptureMode string

const (
	CaptureAlways CaptureMode = "always"
	CaptureNever  CaptureMode = "never"
)

type Capture struct {
	Payloads CaptureMode `json:"payloads,omitempty"`
}

type Pos struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Col  int    `json:"col,omitempty"`
}

func (p *Pos) String() string {
	if p == nil || p.File == "" {
		return "<unknown>"
	}
	if p.Line <= 0 {
		return p.File
	}
	if p.Col > 0 {
		return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
	}
	return fmt.Sprintf("%s:%d", p.File, p.Line)
}

type Scenario struct {
	Name      string     `json:"name"`
	Flows     []Flow     `json:"flows"`
	Profile   Profile    `json:"profile"`
	Target    string     `json:"target,omitempty"`
	DataPools []DataPool `json:"data_pools,omitempty"`
}

type Flow struct {
	Name  string `json:"name"`
	Data  string `json:"data,omitempty"`
	Steps []Step `json:"steps"`
	Pos   *Pos   `json:"pos,omitempty"`
}

type Step struct {
	ID   string   `json:"id"`
	Type StepType `json:"type"`

	Call    *CallSpec    `json:"call,omitempty"`
	GraphQL *GraphQLSpec `json:"graphql,omitempty"`
	WS      *WSSpec      `json:"ws,omitempty"`
	GRPC    *GRPCSpec    `json:"grpc,omitempty"`
	Wait    *WaitSpec    `json:"wait,omitempty"`
	Poll    *PollSpec    `json:"poll,omitempty"`

	Extract   []Extraction  `json:"extract,omitempty"`
	Assert    []Assertion   `json:"assert,omitempty"`
	Retry     *RetryPolicy  `json:"retry,omitempty"`
	Throttle  *ThrottleSpec `json:"throttle,omitempty"`
	Auth      *AuthSpec     `json:"auth,omitempty"`
	OnFailure FailureAction `json:"on_failure,omitempty"`
	Capture   *Capture      `json:"capture,omitempty"`
	Pos       *Pos          `json:"pos,omitempty"`
}

type CallSpec struct {
	Endpoint string            `json:"endpoint,omitempty"`
	Method   string            `json:"method,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Query    map[string]string `json:"query,omitempty"`
	Body     json.RawMessage   `json:"body,omitempty"`
}

// GraphQLErrorPolicy decides what a non-empty `errors` array means for the
// step's outcome. GraphQL answers `200 OK` for a failed operation, so unlike
// every other adapter the transport status cannot classify it — the body has
// to. Defaulting to fail is deliberate: the alternative is a flow that forgets
// an assertion reporting a broken query as a pass.
type GraphQLErrorPolicy string

const (
	// GraphQLErrorsFail is the default: any error fails the step.
	GraphQLErrorsFail GraphQLErrorPolicy = "fail"
	// GraphQLErrorsAllowPartial fails only when the operation returned no
	// data — the federated-graph case, where one subgraph erroring while the
	// rest resolve is a normal, useful response.
	GraphQLErrorsAllowPartial GraphQLErrorPolicy = "allow_partial"
	// GraphQLErrorsIgnore leaves errors to the flow's own assertions.
	GraphQLErrorsIgnore GraphQLErrorPolicy = "ignore"
)

// GraphQLSpec is one GraphQL operation. It is HTTP underneath — a POST of
// {query, variables, operationName} — so it shares the HTTP adapter's session,
// per-phase spans, retry policy, throttle classification, and auth: only the
// request body shape and the error semantics differ.
type GraphQLSpec struct {
	Endpoint string `json:"endpoint,omitempty"`
	URL      string `json:"url,omitempty"`

	// Query is the operation document. Variables carry the values, so this
	// stays a constant string rather than something templated per iteration.
	Query     string          `json:"query"`
	Variables json.RawMessage `json:"variables,omitempty"`

	// Operation names which operation to run when the document holds several.
	Operation string             `json:"operation_name,omitempty"`
	Headers   map[string]string  `json:"headers,omitempty"`
	OnErrors  GraphQLErrorPolicy `json:"on_errors,omitempty"`
}

// WSSpec is one step's worth of work on a WebSocket session.
//
// It is the first step type that is not call-shaped. A session outlives the
// step that opened it — it is closed when the iteration ends, not when the
// step returns — so a step either opens one (URL set), works on one already
// open (URL empty), or does both. Session names it in every case: on an
// opening step the name later steps reference, on the others which session to
// use. Empty is a name like any other: the flow's single unnamed session.
//
// v0 speaks JSON text frames. That is what makes the rest of the engine work
// on a frame unchanged — Match, Extract and Assert are the same JSONPath
// evaluator the HTTP body path uses.
type WSSpec struct {
	Endpoint string `json:"endpoint,omitempty"`
	URL      string `json:"url,omitempty"`
	Session  string `json:"session,omitempty"`

	// Headers and Subprotocols ride on the handshake, so they say something
	// only on a step that opens a session.
	Headers      map[string]string `json:"headers,omitempty"`
	Subprotocols []string          `json:"subprotocols,omitempty"`

	// Send is the frame to send, in the shape a call step's body takes.
	Send json.RawMessage `json:"send,omitempty"`

	Receive *WSReceive `json:"receive,omitempty"`
}

// Opens reports whether the step opens the session rather than joining one an
// earlier step opened.
func (w *WSSpec) Opens() bool { return w.URL != "" || w.Endpoint != "" }

// describeSession names the session for a human. The unnamed session is the
// common case, and quoting "" at a reader helps nobody.
func (w *WSSpec) describeSession() string {
	if w.Session == "" {
		return "the flow's ws session"
	}
	return fmt.Sprintf("ws session %q", w.Session)
}

// WSReceive is the frame the step waits for.
//
// Match is a filter, not an assertion: a duplex connection carries frames this
// step never asked for — heartbeats, other subscriptions' traffic — and
// skipping them is not the same as failing on them. The step's own Assert
// judges the frame Match selected. An empty Match takes the next frame,
// whatever it is.
type WSReceive struct {
	Match   []Assertion `json:"match,omitempty"`
	Timeout Duration    `json:"timeout,omitempty"`
}

// GRPCSpec is one unary gRPC call.
//
// It is the first adapter that does not ride on the HTTP one. gRPC brings its
// own HTTP/2 client, so the session, the cookie jar and the httptrace hooks do
// not carry over. Everything above the wire does: templating, the host
// allow-list, retry, throttle classification, extraction and assertions all
// apply unchanged, because the response message is converted to JSON and the
// status code is an ordinary status.
//
// Streaming is deliberately absent — the step and span model is call-shaped,
// and whether streaming belongs in v1 at all is the question issue #29 asks.
type GRPCSpec struct {
	// Proto is the file describing the method, resolved relative to the flow
	// file. It is compiled to descriptors once per run, not once per call.
	Proto string `json:"proto"`

	// ImportPaths are extra roots for the proto's own imports, resolved the
	// same way. The proto's own directory is always searched.
	ImportPaths []string `json:"import_paths,omitempty"`

	// Method is the fully-qualified method — `package.Service/Method`. It is
	// also, literally, the HTTP/2 `:path` gRPC puts on the wire.
	Method string `json:"method"`

	Endpoint string `json:"endpoint,omitempty"`

	// URL is the address as `grpc://host:port` (or `grpcs://` for TLS). A gRPC
	// step has no path of its own — the method is the path — so the target's
	// base URL is usually the whole address, and this stays empty.
	URL string `json:"url,omitempty"`

	// Message is the request, in the shape a call step's body takes. It is
	// converted into the method's input type, so a field the schema does not
	// declare is an error rather than something the server quietly ignores.
	Message json.RawMessage `json:"message,omitempty"`

	// Headers are gRPC metadata. They are named for what they are on the wire
	// — HTTP/2 headers — so the step-level `headers:` block, and every auth
	// scheme that writes one, reaches a gRPC call unchanged.
	Headers map[string]string `json:"headers,omitempty"`
}

// GRPCPath renders the method as the leading-slash path form the wire uses.
func (g *GRPCSpec) GRPCPath() string {
	if strings.HasPrefix(g.Method, "/") {
		return g.Method
	}
	return "/" + g.Method
}

type WaitSpec struct {
	Duration Duration `json:"duration"`
}

type PollSpec struct {
	Call        CallSpec    `json:"call"`
	Until       []Assertion `json:"until"`
	Interval    Duration    `json:"interval"`
	Timeout     Duration    `json:"timeout,omitempty"`
	MaxAttempts int         `json:"max_attempts,omitempty"`
}

type ExtractionSource string

const (
	ExtractBody   ExtractionSource = "body"
	ExtractHeader ExtractionSource = "header"
	ExtractStatus ExtractionSource = "status"
)

type Extraction struct {
	Var  string           `json:"var"`
	From ExtractionSource `json:"from,omitempty"`
	Path string           `json:"path,omitempty"`
}

type AssertionSource string

const (
	AssertStatus  AssertionSource = "status"
	AssertLatency AssertionSource = "latency"
	AssertHeader  AssertionSource = "header"
	AssertBody    AssertionSource = "body"
	AssertVar     AssertionSource = "var"
)

type AssertionOp string

const (
	OpEq        AssertionOp = "eq"
	OpNe        AssertionOp = "ne"
	OpLt        AssertionOp = "lt"
	OpLte       AssertionOp = "lte"
	OpGt        AssertionOp = "gt"
	OpGte       AssertionOp = "gte"
	OpContains  AssertionOp = "contains"
	OpMatches   AssertionOp = "matches"
	OpExists    AssertionOp = "exists"
	OpNotExists AssertionOp = "not_exists"
)

type Assertion struct {
	Source AssertionSource `json:"source"`
	Key    string          `json:"key,omitempty"`
	Op     AssertionOp     `json:"op"`
	Value  json.RawMessage `json:"value,omitempty"`
}

type BackoffStrategy string

const (
	BackoffFixed           BackoffStrategy = "fixed"
	BackoffExponential     BackoffStrategy = "exponential"
	BackoffHonorRetryAfter BackoffStrategy = "honor_retry_after"
)

type RetryPolicy struct {
	OnStatus    []int           `json:"on_status"`
	Backoff     BackoffStrategy `json:"backoff"`
	MaxAttempts int             `json:"max_attempts"`
	BaseDelay   Duration        `json:"base_delay,omitempty"`
}

// ThrottleSpec tunes throttle classification for a call step. Statuses lists
// author-mapped codes treated as throttled on top of the always-on HTTP 429.
// AsError overrides the mode default for whether a throttle counts as a
// failure (integration/system: yes; load/stress/soak: no) — nil keeps it.
type ThrottleSpec struct {
	Statuses []int `json:"statuses,omitempty"`
	AsError  *bool `json:"as_error,omitempty"`
}

type AuthScheme string

const (
	// AuthNone is an explicit opt-out. Both authoring surfaces drop it while
	// flattening a flow-level default onto steps, so it only reaches the
	// executor in hand-written IR, where it is a no-op.
	AuthNone   AuthScheme = "none"
	AuthBearer AuthScheme = "bearer"
	AuthBasic  AuthScheme = "basic"
	AuthAPIKey AuthScheme = "api_key"
	AuthCookie AuthScheme = "cookie"
	AuthOAuth2 AuthScheme = "oauth2_client_credentials"
	AuthHMAC   AuthScheme = "hmac"
)

// CredentialIn names where an api_key rides on the request.
type CredentialIn string

const (
	InHeader CredentialIn = "header"
	InQuery  CredentialIn = "query"
)

// HMAC defaults. The canonical string is what most request-signing schemes
// agree on — method, path, a timestamp, a body digest — and Sign overrides it
// for services that sign something else.
const (
	DefaultHMACAlgorithm       = "sha256"
	DefaultHMACEncoding        = "hex"
	DefaultHMACHeader          = "X-Signature"
	DefaultHMACKeyIDHeader     = "X-Key-Id"
	DefaultHMACSigningTemplate = "{method}\n{path}\n{timestamp}\n{body_sha256}"
)

// AuthSpec declares how a step authenticates. It carries credential
// *references* — `{{ env.* }}` templates resolved at request time — never
// literal credentials, so every file holding one stays safe to commit
// (ADR 0005). Which fields apply depends on Scheme; validate rejects fields
// belonging to another scheme rather than silently ignoring them.
//
// OAuth2 authorization-code is deliberately absent: it needs browser
// interaction and is explicitly out of v1 scope (PRD section 8).
type AuthSpec struct {
	Scheme AuthScheme `json:"scheme"`

	// bearer: the token, static or extracted by an earlier step.
	Token string `json:"token,omitempty"`

	// basic
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// api_key (In selects header or query) and cookie (always a cookie).
	Name  string       `json:"name,omitempty"`
	Value string       `json:"value,omitempty"`
	In    CredentialIn `json:"in,omitempty"`

	// oauth2_client_credentials. The token endpoint is fetched once per run
	// and cached across VUs, and must sit inside the target's allow-list.
	TokenURL     string   `json:"token_url,omitempty"`
	ClientID     string   `json:"client_id,omitempty"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`

	// hmac request signing.
	Secret          string `json:"secret,omitempty"`
	Algorithm       string `json:"algorithm,omitempty"`        // sha256 (default) | sha512
	Encoding        string `json:"encoding,omitempty"`         // hex (default) | base64
	Header          string `json:"header,omitempty"`           // carries the signature
	KeyID           string `json:"key_id,omitempty"`           // optional key identifier
	KeyIDHeader     string `json:"key_id_header,omitempty"`    // carries KeyID when set
	TimestampHeader string `json:"timestamp_header,omitempty"` // carries the signing timestamp
	// Sign is the canonical string, over the placeholders {method}, {path},
	// {query}, {body}, {body_sha256}, {timestamp}, and {key_id}. It is not
	// a credential and is never `{{ }}`-templated.
	Sign string `json:"sign,omitempty"`
}

type Profile struct {
	Mode       Mode     `json:"mode"`
	VUs        int      `json:"vus,omitempty"`
	Ramp       string   `json:"ramp,omitempty"`
	Hold       Duration `json:"hold,omitempty"`
	Iterations int      `json:"iterations,omitempty"`
	ArrivalCap string   `json:"arrival_cap,omitempty"`
	Thresholds []string `json:"thresholds,omitempty"`
}

type TargetConfig struct {
	Name     string   `json:"name"`
	BaseURLs []string `json:"base_urls"`
	MaxVUs   int      `json:"max_vus,omitempty"`
	MaxRPS   int      `json:"max_rps,omitempty"`

	// RequestTimeout bounds a single call. It belongs to the target rather than
	// the scenario because it is a property of what is being called: a target
	// that never answers inside 2s is failing, whatever the flow asks of it.
	// Zero uses the adapter's default.
	RequestTimeout Duration `json:"request_timeout,omitempty"`

	AgentAddr       string `json:"agent_addr,omitempty"`
	DisallowedModes []Mode `json:"disallowed_modes,omitempty"`
}

type PoolFormat string

const (
	PoolCSV  PoolFormat = "csv"
	PoolJSON PoolFormat = "json"
)

type PoolDistribution string

const (
	DistributeUniquePerVU PoolDistribution = "unique-per-vu"
	DistributeRoundRobin  PoolDistribution = "round-robin"
	DistributeRandom      PoolDistribution = "random"
)

type PoolExhaustion string

const (
	ExhaustFail  PoolExhaustion = "fail"
	ExhaustCycle PoolExhaustion = "cycle"
	ExhaustStop  PoolExhaustion = "stop"
)

type DataPool struct {
	Name         string           `json:"name"`
	Source       string           `json:"source"`
	Format       PoolFormat       `json:"format,omitempty"`
	Distribution PoolDistribution `json:"distribution,omitempty"`
	OnExhausted  PoolExhaustion   `json:"on_exhausted,omitempty"`
}
