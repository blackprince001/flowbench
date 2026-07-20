// Package ir defines the canonical flow IR: the single structure both
// authoring surfaces compile to and the only input the executor accepts.
package ir

import (
	"encoding/json"
	"fmt"
)

type StepType string

const (
	StepCall   StepType = "call"
	StepLogic  StepType = "logic"
	StepWait   StepType = "wait"
	StepPoll   StepType = "poll"
	StepVerify StepType = "verify"
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

type LogicSpec struct {
	Hook string `json:"hook"`
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

type VerifySpec struct {
	Connection string   `json:"connection"`
	Query      string   `json:"query"`
	Args       []string `json:"args,omitempty"`
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
	Name            string   `json:"name"`
	BaseURLs        []string `json:"base_urls"`
	MaxVUs          int      `json:"max_vus,omitempty"`
	MaxRPS          int      `json:"max_rps,omitempty"`
	AgentAddr       string   `json:"agent_addr,omitempty"`
	DisallowedModes []Mode   `json:"disallowed_modes,omitempty"`
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
