package parser_test

import (
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
)

func TestParsesPRDSampleFlow(t *testing.T) {
	res, err := parser.ParseFlowFile("../../tests/flows/authenticated_checkout.flow.yaml", nil)
	if err != nil {
		t.Fatalf("PRD sample flow should parse, got:\n%v", err)
	}
	sc := res.Scenario

	if sc.Name != "authenticated_checkout" {
		t.Errorf("scenario name = %q, want authenticated_checkout", sc.Name)
	}
	if len(sc.Flows) != 1 || len(sc.Flows[0].Steps) != 3 {
		t.Fatalf("want 1 flow with 3 steps, got %+v", sc.Flows)
	}
	flow := sc.Flows[0]

	if flow.Data != "user" || len(sc.DataPools) != 1 || sc.DataPools[0].Source != "fixtures/users.csv" {
		t.Errorf("data shorthand should bind pool %q from users.csv, got flow.Data=%q pools=%+v",
			"user", flow.Data, sc.DataPools)
	}

	login, createOrder, pay := flow.Steps[0], flow.Steps[1], flow.Steps[2]
	for i, want := range []string{"login", "create_order", "pay"} {
		if flow.Steps[i].ID != want || flow.Steps[i].Type != ir.StepCall {
			t.Errorf("step %d = %q (%s), want call step %q", i, flow.Steps[i].ID, flow.Steps[i].Type, want)
		}
		if flow.Steps[i].Pos == nil || flow.Steps[i].Pos.Line == 0 {
			t.Errorf("step %d should carry a source position", i)
		}
	}

	if login.Call.Method != "POST" || login.Call.URL != "/auth/login" {
		t.Errorf("login call = %s %s", login.Call.Method, login.Call.URL)
	}
	if len(login.Extract) != 1 || login.Extract[0].Var != "token" || login.Extract[0].Path != "$.data.access_token" {
		t.Errorf("login extract = %+v", login.Extract)
	}
	if len(login.Assert) != 2 {
		t.Errorf("login should have 2 assertions, got %+v", login.Assert)
	}

	if createOrder.Retry == nil ||
		createOrder.Retry.Backoff != ir.BackoffHonorRetryAfter ||
		createOrder.Retry.MaxAttempts != 5 ||
		len(createOrder.Retry.OnStatus) != 2 {
		t.Errorf("create_order retry = %+v", createOrder.Retry)
	}
	if got := createOrder.Call.Headers["Authorization"]; got != "Bearer {{ token }}" {
		t.Errorf("create_order Authorization header = %q", got)
	}
	if string(createOrder.Call.Body) != `{"items":"{{ user.cart }}"}` {
		t.Errorf("create_order body = %s", createOrder.Call.Body)
	}

	if pay.Call.URL != "/orders/{{ order_id }}/pay" {
		t.Errorf("pay url = %q", pay.Call.URL)
	}

	p := sc.Profile
	if p.Mode != ir.ModeStress ||
		p.Ramp != "0 -> 500 over 5m" ||
		p.Hold != ir.Duration(10*time.Minute) ||
		p.ArrivalCap != "300/s" ||
		len(p.Thresholds) != 2 {
		t.Errorf("profile = %+v", p)
	}
}

func TestUndefinedVariableFailsWithFileLine(t *testing.T) {
	src := []byte(`flow: broken
steps:
  - id: pay
    call: POST /orders/{{ order_id }}/pay
`)
	_, err := parser.ParseFlow(src, "test.flow.yaml", nil)
	if err == nil {
		t.Fatal("undefined variable should fail the parse")
	}
	msg := err.Error()
	if !strings.Contains(msg, "test.flow.yaml:3") {
		t.Errorf("error should point at the step's file:line (test.flow.yaml:3), got:\n%v", msg)
	}
	if !strings.Contains(msg, "order_id") {
		t.Errorf("error should name the unresolved variable, got:\n%v", msg)
	}
}

func TestUnknownKeysFailWithPosition(t *testing.T) {
	src := []byte(`flow: typo
steps:
  - id: ping
    call: GET /health
    assertt: [ status == 200 ]
`)
	_, err := parser.ParseFlow(src, "typo.flow.yaml", nil)
	if err == nil {
		t.Fatal("unknown step key should fail the parse")
	}
	if !strings.Contains(err.Error(), `unknown step key "assertt"`) ||
		!strings.Contains(err.Error(), "typo.flow.yaml:5") {
		t.Errorf("error should name the key and its file:line, got:\n%v", err)
	}
}

func TestCallShorthandRejectsMalformedValues(t *testing.T) {
	for name, call := range map[string]string{
		"no url":           "POST",
		"lowercase method": "post /health",
	} {
		t.Run(name, func(t *testing.T) {
			src := []byte("flow: bad\nsteps:\n  - id: s\n    call: " + call + "\n")
			_, err := parser.ParseFlow(src, "bad.flow.yaml", nil)
			if err == nil || !strings.Contains(err.Error(), "call must look like") {
				t.Errorf("call %q should fail the shorthand, got: %v", call, err)
			}
		})
	}
}

func TestWaitAndPollSteps(t *testing.T) {
	src := []byte(`flow: settle
steps:
  - id: create
    call: POST /jobs
    extract: { job_id: $.data.id }
  - id: cooldown
    wait: 2s
  - id: await_done
    poll:
      call: GET /jobs/{{ job_id }}
      until: [ $.data.state == "done" ]
      interval: 500ms
      max_attempts: 20
`)
	res, err := parser.ParseFlow(src, "settle.flow.yaml", nil)
	if err != nil {
		t.Fatalf("wait/poll flow should parse, got:\n%v", err)
	}
	steps := res.Scenario.Flows[0].Steps
	if steps[1].Type != ir.StepWait || steps[1].Wait.Duration != ir.Duration(2*time.Second) {
		t.Errorf("cooldown = %+v", steps[1])
	}
	poll := steps[2].Poll
	if steps[2].Type != ir.StepPoll || poll == nil {
		t.Fatalf("await_done should be a poll step, got %+v", steps[2])
	}
	if poll.Call.URL != "/jobs/{{ job_id }}" || poll.Interval != ir.Duration(500*time.Millisecond) ||
		poll.MaxAttempts != 20 || len(poll.Until) != 1 {
		t.Errorf("poll = %+v", poll)
	}
}

func TestProfileVUCountForm(t *testing.T) {
	src := []byte(`flow: quick
steps:
  - id: ping
    call: GET /health
profile:
  mode: load
  vus: 50
  hold: 5m
`)
	res, err := parser.ParseFlow(src, "quick.flow.yaml", nil)
	if err != nil {
		t.Fatalf("integer vus form should parse, got:\n%v", err)
	}
	p := res.Scenario.Profile
	if p.Mode != ir.ModeLoad || p.VUs != 50 || p.Hold != ir.Duration(5*time.Minute) {
		t.Errorf("profile = %+v", p)
	}
}

func TestMissingProfileDefaultsToIntegration(t *testing.T) {
	src := []byte("flow: quick\nsteps:\n  - id: ping\n    call: GET /health\n")
	res, err := parser.ParseFlow(src, "quick.flow.yaml", nil)
	if err != nil {
		t.Fatalf("profile-less flow should parse, got:\n%v", err)
	}
	if res.Scenario.Profile.Mode != ir.ModeIntegration {
		t.Errorf("default mode = %q, want integration", res.Scenario.Profile.Mode)
	}
}

func TestHoldSetTwiceFails(t *testing.T) {
	src := []byte(`flow: dup
steps:
  - id: ping
    call: GET /health
profile:
  mode: load
  vus: { ramp: "0 -> 10 over 1m", hold: 5m }
  hold: 10m
`)
	_, err := parser.ParseFlow(src, "dup.flow.yaml", nil)
	if err == nil || !strings.Contains(err.Error(), "hold is set more than once") {
		t.Errorf("duplicate hold should fail, got: %v", err)
	}
}

func TestStepRenameWarningHook(t *testing.T) {
	src := []byte(`flow: authenticated_checkout
steps:
  - id: login
    call: POST /auth/login
  - id: pay_v2
    call: POST /pay
`)
	res, err := parser.ParseFlow(src, "checkout.flow.yaml", &parser.Options{
		PriorStepIDs: map[string][]string{
			"authenticated_checkout": {"login", "pay"},
		},
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("want exactly one rename warning, got %+v", res.Warnings)
	}
	w := res.Warnings[0].String()
	if !strings.Contains(w, `"pay"`) || !strings.Contains(w, "checkout.flow.yaml") {
		t.Errorf("warning should name the missing step and the file, got: %s", w)
	}
}

func TestRejectsMultipleDocuments(t *testing.T) {
	src := []byte("flow: a\nsteps:\n  - id: s\n    call: GET /x\n---\nflow: b\n")
	if _, err := parser.ParseFlow(src, "multi.flow.yaml", nil); err == nil {
		t.Error("multi-document files should be rejected")
	}
}
