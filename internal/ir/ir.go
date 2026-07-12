// Package ir defines the canonical flow representation: the single structure
// both authoring surfaces (YAML DSL, Python SDK) compile to and the only
// input the executor accepts (ADR 0002).
//
// Contract rules for evolving these types:
//
//   - Evolution is additive. New fields are optional (omitempty); existing
//     fields never change type or disappear while any surface emits them.
//   - Enums are open string types, so new step kinds (ws in M3, a possible
//     prompt kind per ADR 0009) are new constants, not breaking changes.
//     Validation pins each release to the kinds it can execute.
//   - The canonical encoding is JSON, decoded strictly (unknown fields are
//     errors) so drift between the surfaces and the engine surfaces loudly.
//   - Surface-syntax strings (Profile.Ramp, Profile.ArrivalCap,
//     Profile.Thresholds) are carried verbatim in v0, matching the PRD's
//     conceptual model; the planner owns their semantics (M2) and structured
//     forms will be added additively.
package ir

import "encoding/json"

// StepType discriminates Step's union. Exactly one spec field matching the
// type must be set.
type StepType string

const (
	StepCall   StepType = "call"
	StepLogic  StepType = "logic"
	StepWait   StepType = "wait"
	StepPoll   StepType = "poll"
	StepVerify StepType = "verify"
)

// Mode is the execution mode a profile selects — the four test categories,
// with system as the multi-flow composition of integration (ADR 0003).
type Mode string

const (
	ModeIntegration Mode = "integration"
	ModeSystem      Mode = "system"
	ModeLoad        Mode = "load"
	ModeStress      Mode = "stress"
	ModeSoak        Mode = "soak"
)

// FailureAction says what an assertion failure does to the run. The zero
// value defers to the mode-aware default (loud in integration/system, data
// in load/stress/soak — PRD section 12).
type FailureAction string

const (
	FailureAbortFlow FailureAction = "abort_flow"
	FailureAbortRun  FailureAction = "abort_run"
	FailureRecord    FailureAction = "record"
)

// CaptureMode overrides the run-level capture policy for one step. The zero
// value defers to the policy default (all failures plus a sample). "always"
// is the carve-out prompt observations rely on (ADR 0009).
type CaptureMode string

const (
	CaptureAlways CaptureMode = "always"
	CaptureNever  CaptureMode = "never"
)

// Capture is a step's capture-policy override.
type Capture struct {
	Payloads CaptureMode `json:"payloads,omitempty"`
}

// Scenario is the runnable unit: one or more flows bound to a profile, a
// target config (by name, resolved at run time via --target), and data pools.
type Scenario struct {
	Name      string     `json:"name"`
	Flows     []Flow     `json:"flows"`
	Profile   Profile    `json:"profile"`
	Target    string     `json:"target,omitempty"`
	DataPools []DataPool `json:"data_pools,omitempty"`
}

// Flow is an ordered sequence of steps; the unit of authorship.
type Flow struct {
	Name string `json:"name"`
	// Data names the scenario data pool whose rows this flow draws; the row
	// is exposed to templates under the pool's name as the variable root
	// (a pool named "user" backs "{{ user.email }}").
	Data  string `json:"data,omitempty"`
	Steps []Step `json:"steps"`
}

// Step is the atomic unit of a flow: a discriminated union over the v0 step
// kinds, plus the extraction, assertion, retry, failure, and capture
// behaviour shared by all kinds.
type Step struct {
	// ID is the step's structural identity: folding spans across iterations
	// and runs depends on it staying stable (PRD 10.7).
	ID   string   `json:"id"`
	Type StepType `json:"type"`

	Call   *CallSpec   `json:"call,omitempty"`
	Logic  *LogicSpec  `json:"logic,omitempty"`
	Wait   *WaitSpec   `json:"wait,omitempty"`
	Poll   *PollSpec   `json:"poll,omitempty"`
	Verify *VerifySpec `json:"verify,omitempty"`

	Extract   []Extraction  `json:"extract,omitempty"`
	Assert    []Assertion   `json:"assert,omitempty"`
	Retry     *RetryPolicy  `json:"retry,omitempty"`
	OnFailure FailureAction `json:"on_failure,omitempty"`
	Capture   *Capture      `json:"capture,omitempty"`
}

// CallSpec is a protocol call, either inline (method + URL) or a reference
// into the endpoint catalog — exactly one of the two.
type CallSpec struct {
	Endpoint string            `json:"endpoint,omitempty"`
	Method   string            `json:"method,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Query    map[string]string `json:"query,omitempty"`
	// Body is the request body as JSON, with template placeholders allowed
	// inside string values.
	Body json.RawMessage `json:"body,omitempty"`
}

// LogicSpec routes a step to a registered Python hook via the bridged worker
// pool (ADR 0008).
type LogicSpec struct {
	Hook string `json:"hook"`
}

// WaitSpec pauses the iteration.
type WaitSpec struct {
	Duration Duration `json:"duration"`
}

// PollSpec repeats a call until every condition in Until holds. It must be
// bounded by a timeout, a max attempt count, or both, so a persistently
// unavailable target cannot hang a run (PRD section 14).
type PollSpec struct {
	Call        CallSpec    `json:"call"`
	Until       []Assertion `json:"until"`
	Interval    Duration    `json:"interval"`
	Timeout     Duration    `json:"timeout,omitempty"`
	MaxAttempts int         `json:"max_attempts,omitempty"`
}

// VerifySpec checks state directly in a database when no endpoint exposes
// it: a parameterized read-only query against a connection declared in the
// target config (PRD 10.4). The step's assertions judge the result.
type VerifySpec struct {
	Connection string `json:"connection"`
	Query      string `json:"query"`
	// Args are the query's parameter values; template placeholders allowed.
	Args []string `json:"args,omitempty"`
}

// ExtractionSource says which part of the response an extraction reads.
type ExtractionSource string

const (
	// ExtractBody is the default when From is empty.
	ExtractBody   ExtractionSource = "body"
	ExtractHeader ExtractionSource = "header"
	ExtractStatus ExtractionSource = "status"
)

// Extraction captures a value from a step's response into a flow variable,
// available to templates in later steps and to var assertions from this step
// onward.
type Extraction struct {
	Var  string           `json:"var"`
	From ExtractionSource `json:"from,omitempty"`
	// Path is the JSONPath into the body, or the header name; unused when
	// extracting the status.
	Path string `json:"path,omitempty"`
}

// AssertionSource says what an assertion inspects.
type AssertionSource string

const (
	AssertStatus  AssertionSource = "status"
	AssertLatency AssertionSource = "latency"
	AssertHeader  AssertionSource = "header"
	AssertBody    AssertionSource = "body"
	AssertVar     AssertionSource = "var"
)

// AssertionOp is the comparison an assertion applies.
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

// Assertion is one per-step condition. Key carries the header name, body
// JSONPath, or variable name for the sources that need one; Value is the
// expected operand as JSON, absent for exists/not_exists.
type Assertion struct {
	Source AssertionSource `json:"source"`
	Key    string          `json:"key,omitempty"`
	Op     AssertionOp     `json:"op"`
	Value  json.RawMessage `json:"value,omitempty"`
}

// BackoffStrategy is how retry delays grow between attempts.
type BackoffStrategy string

const (
	BackoffFixed       BackoffStrategy = "fixed"
	BackoffExponential BackoffStrategy = "exponential"
	// BackoffHonorRetryAfter respects the target's Retry-After header,
	// falling back to exponential when the header is absent.
	BackoffHonorRetryAfter BackoffStrategy = "honor_retry_after"
)

// RetryPolicy makes a call step back off and retry on rate-limited or
// transiently unavailable responses (PRD 10.1). Each attempt emits its own
// span so time spent backing off stays visible.
type RetryPolicy struct {
	OnStatus    []int           `json:"on_status"`
	Backoff     BackoffStrategy `json:"backoff"`
	MaxAttempts int             `json:"max_attempts"`
	BaseDelay   Duration        `json:"base_delay,omitempty"`
}

// Profile is the execution contract that turns one flow into any of the four
// test categories (ADR 0003). Ramp (e.g. "0 -> 500 over 5m"), ArrivalCap
// (e.g. "300/s"), and Thresholds (e.g. "p95(latency) < 800ms") are surface
// syntax carried verbatim in v0; the planner parses them (M2).
type Profile struct {
	Mode       Mode     `json:"mode"`
	VUs        int      `json:"vus,omitempty"`
	Ramp       string   `json:"ramp,omitempty"`
	Hold       Duration `json:"hold,omitempty"`
	Iterations int      `json:"iterations,omitempty"`
	ArrivalCap string   `json:"arrival_cap,omitempty"`
	Thresholds []string `json:"thresholds,omitempty"`
}

// TargetConfig is a named target: base URLs (the host allow-list), ceilings,
// and optionally an agent address. Never credentials (ADR 0005).
type TargetConfig struct {
	Name     string   `json:"name"`
	BaseURLs []string `json:"base_urls"`
	// MaxVUs and MaxRPS are planner-enforced ceilings; zero means no ceiling.
	MaxVUs          int    `json:"max_vus,omitempty"`
	MaxRPS          int    `json:"max_rps,omitempty"`
	AgentAddr       string `json:"agent_addr,omitempty"`
	DisallowedModes []Mode `json:"disallowed_modes,omitempty"`
}

// PoolFormat is a data pool's fixture format, inferred from the source file
// extension when empty.
type PoolFormat string

const (
	PoolCSV  PoolFormat = "csv"
	PoolJSON PoolFormat = "json"
)

// PoolDistribution is how concurrent VUs draw rows.
type PoolDistribution string

const (
	// DistributeUniquePerVU is the default: each VU gets a distinct row.
	DistributeUniquePerVU PoolDistribution = "unique-per-vu"
	DistributeRoundRobin  PoolDistribution = "round-robin"
	DistributeRandom      PoolDistribution = "random"
)

// PoolExhaustion is what happens when a pool runs out of rows.
type PoolExhaustion string

const (
	// ExhaustFail is the default: exhaustion is a pre-run or run error.
	ExhaustFail  PoolExhaustion = "fail"
	ExhaustCycle PoolExhaustion = "cycle"
	ExhaustStop  PoolExhaustion = "stop"
)

// DataPool is a fixture source with a distribution policy; also the
// sanctioned seeding mechanism (PRD 10.3).
type DataPool struct {
	Name         string           `json:"name"`
	Source       string           `json:"source"`
	Format       PoolFormat       `json:"format,omitempty"`
	Distribution PoolDistribution `json:"distribution,omitempty"`
	OnExhausted  PoolExhaustion   `json:"on_exhausted,omitempty"`
}
