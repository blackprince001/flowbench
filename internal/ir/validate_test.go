package ir_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
)

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct {
		mutate  func(s *ir.Scenario)
		wantErr string
	}{
		"duplicate step id": {
			func(s *ir.Scenario) { s.Flows[0].Steps[2].ID = "login" },
			"duplicate step id",
		},
		"step id with span-reserved characters": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].ID = "login@v2" },
			"reserved for span names",
		},
		"unknown step type": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Type = "prompt" },
			"unknown step type",
		},
		"type does not match the spec that is set": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Type = ir.StepWait },
			`the "wait" spec is not set`,
		},
		"more than one spec set": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps[0].Wait = &ir.WaitSpec{Duration: ir.Duration(time.Second)}
			},
			"exactly one spec",
		},
		"inline call missing method or url": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Call.Method = "" },
			"both a method and a url",
		},
		"call with endpoint and inline parts": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Call.Endpoint = "auth_login" },
			"not both",
		},
		"call body that is not JSON": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Call.Body = json.RawMessage(`{oops`) },
			"not valid JSON",
		},
		"template with no upstream source": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Call.URL = "/auth/{{ order_id }}/login" },
			"no upstream source",
		},
		"template using same-step extraction for injection": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Call.URL = "/auth/login?t={{ token }}" },
			"no upstream source",
		},
		"malformed template placeholder": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps[0].Call.Body = json.RawMessage(`{"email":"{{ user..email }}"}`)
			},
			"malformed template",
		},
		"flow without its data pool bound": {
			func(s *ir.Scenario) { s.Flows[0].Data = "" },
			"no upstream source",
		},
		"flow bound to an undeclared pool": {
			func(s *ir.Scenario) { s.Flows[0].Data = "customers" },
			"not declared by the scenario",
		},
		"duplicate pool names": {
			func(s *ir.Scenario) { s.DataPools = append(s.DataPools, s.DataPools[0]) },
			"duplicate pool name",
		},
		"retry without attempts bound": {
			func(s *ir.Scenario) { s.Flows[0].Steps[1].Retry.MaxAttempts = 0 },
			"at least 1",
		},
		"retry with unknown backoff": {
			func(s *ir.Scenario) { s.Flows[0].Steps[1].Retry.Backoff = "jittered" },
			"unknown backoff",
		},
		"retry with non-http status": {
			func(s *ir.Scenario) { s.Flows[0].Steps[1].Retry.OnStatus = []int{429, 42} },
			"not an HTTP status",
		},
		"retry on a non-call step": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps = append(s.Flows[0].Steps, ir.Step{
					ID:    "cooldown",
					Type:  ir.StepWait,
					Wait:  &ir.WaitSpec{Duration: ir.Duration(time.Second)},
					Retry: &ir.RetryPolicy{OnStatus: []int{503}, Backoff: ir.BackoffFixed, MaxAttempts: 2},
				})
			},
			"call, graphql, and grpc steps only",
		},
		"wait without a positive duration": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps = append(s.Flows[0].Steps, ir.Step{
					ID: "cooldown", Type: ir.StepWait, Wait: &ir.WaitSpec{},
				})
			},
			"must be positive",
		},
		"unbounded poll": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps = append(s.Flows[0].Steps, ir.Step{
					ID:   "await_settlement",
					Type: ir.StepPoll,
					Poll: &ir.PollSpec{
						Call:     ir.CallSpec{Method: "GET", URL: "/orders/{{ order_id }}"},
						Until:    []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: json.RawMessage(`200`)}},
						Interval: ir.Duration(time.Second),
					},
				})
			},
			"must be bounded",
		},
		"poll without until conditions": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps = append(s.Flows[0].Steps, ir.Step{
					ID:   "await_settlement",
					Type: ir.StepPoll,
					Poll: &ir.PollSpec{
						Call:        ir.CallSpec{Method: "GET", URL: "/orders/{{ order_id }}"},
						Interval:    ir.Duration(time.Second),
						MaxAttempts: 10,
					},
				})
			},
			"at least one until condition",
		},
		"extraction var with reserved characters": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Extract[0].Var = "auth.token" },
			"reserved",
		},
		"extraction from body without a path": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Extract[0].Path = "" },
			"needs a path",
		},
		"status extraction with a path": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps[0].Extract = append(s.Flows[0].Steps[0].Extract,
					ir.Extraction{Var: "code", From: ir.ExtractStatus, Path: "$.status"})
			},
			"takes no path",
		},
		"exists assertion carrying a value": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Assert[1].Value = json.RawMessage(`true`) },
			"takes no value",
		},
		"comparison assertion missing its value": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Assert[0].Value = nil },
			"needs a value",
		},
		"header assertion without a key": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps[0].Assert = append(s.Flows[0].Steps[0].Assert,
					ir.Assertion{Source: ir.AssertHeader, Op: ir.OpEq, Value: json.RawMessage(`"application/json"`)})
			},
			"need a key",
		},
		"status assertion with a stray key": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Assert[0].Key = "code" },
			"take no key",
		},
		"var assertion on a var nothing extracts": {
			func(s *ir.Scenario) {
				s.Flows[0].Steps[2].Assert = append(s.Flows[0].Steps[2].Assert,
					ir.Assertion{Source: ir.AssertVar, Key: "receipt_id", Op: ir.OpExists})
			},
			"nothing extracts",
		},
		"unknown assertion op": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Assert[0].Op = "approximately" },
			"unknown assertion op",
		},
		"unknown on_failure": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].OnFailure = "explode" },
			"unknown on_failure",
		},
		"unknown capture mode": {
			func(s *ir.Scenario) { s.Flows[0].Steps[0].Capture = &ir.Capture{Payloads: "sometimes"} },
			"unknown capture mode",
		},
		"unknown profile mode": {
			func(s *ir.Scenario) { s.Profile.Mode = "sprint" },
			"unknown mode",
		},
		"negative profile counts": {
			func(s *ir.Scenario) { s.Profile.VUs = -1 },
			"not be negative",
		},
		"blank threshold": {
			func(s *ir.Scenario) { s.Profile.Thresholds = append(s.Profile.Thresholds, "  ") },
			"is empty",
		},
		"scenario without flows": {
			func(s *ir.Scenario) { s.Flows = nil },
			"at least one flow",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := chainedLoginScenario()
			tc.mutate(s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() error does not mention %q:\n%v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateReportsSourcePositions(t *testing.T) {
	s := chainedLoginScenario()
	s.Flows[0].Pos = &ir.Pos{File: "tests/flows/checkout.flow.yaml", Line: 1}
	s.Flows[0].Steps[2].Pos = &ir.Pos{File: "tests/flows/checkout.flow.yaml", Line: 18}
	s.Flows[0].Steps[2].Call.URL = "/orders/{{ missing_var }}/pay"

	err := s.Validate()
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if !strings.Contains(err.Error(), "tests/flows/checkout.flow.yaml:18") {
		t.Errorf("error should carry the step's file:line, got:\n%v", err)
	}
}

func TestValidateAllowsSameStepVarAssertion(t *testing.T) {
	// a step asserting on a var it extracts in that same step must stay legal
	if err := chainedLoginScenario().Validate(); err != nil {
		t.Fatalf("same-step var assertion should validate, got:\n%v", err)
	}
}

func TestTargetConfigValidate(t *testing.T) {
	valid := ir.TargetConfig{
		Name:            "local",
		BaseURLs:        []string{"http://localhost:8080"},
		MaxVUs:          200,
		MaxRPS:          500,
		DisallowedModes: []ir.Mode{ir.ModeStress, ir.ModeSoak},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid target config rejected:\n%v", err)
	}

	cases := map[string]struct {
		mutate  func(tc *ir.TargetConfig)
		wantErr string
	}{
		"no base urls": {
			func(tc *ir.TargetConfig) { tc.BaseURLs = nil },
			"at least one base URL",
		},
		"relative base url": {
			func(tc *ir.TargetConfig) { tc.BaseURLs = []string{"localhost:8080"} },
			"must be absolute",
		},
		"negative ceiling": {
			func(tc *ir.TargetConfig) { tc.MaxRPS = -1 },
			"not be negative",
		},
		"unknown disallowed mode": {
			func(tc *ir.TargetConfig) { tc.DisallowedModes = []ir.Mode{"sprint"} },
			"unknown mode",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			cfg.BaseURLs = append([]string(nil), valid.BaseURLs...)
			cfg.DisallowedModes = append([]ir.Mode(nil), valid.DisallowedModes...)
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() error does not mention %q:\n%v", tc.wantErr, err)
			}
		})
	}
}
