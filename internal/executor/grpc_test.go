package executor_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/grpcstub"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
)

// serveGRPC serves the same schema the conformance fixture calls and returns
// the base URL a target would carry — written as http://, because a target
// lists a host once whatever protocol reaches it.
func serveGRPC(t *testing.T, handlers map[string]grpcstub.Handler) string {
	t.Helper()
	srv, err := grpcstub.New("../../tests/flows/grpc/billing.proto")
	if err != nil {
		t.Fatalf("building stub: %v", err)
	}
	for method, h := range handlers {
		srv.Handle(method, h)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(srv.Stop)
	return "http://" + ln.Addr().String()
}

func chargeFlow(throttle *ir.ThrottleSpec) ir.Flow {
	return ir.Flow{Name: "billing", Steps: []ir.Step{{
		ID: "charge", Type: ir.StepGRPC,
		GRPC: &ir.GRPCSpec{
			Proto:   "billing.proto",
			Method:  "billing.v1.Billing/Charge",
			Message: []byte(`{"account":"acct_1","amountCents":"1200","currency":"GHS"}`),
		},
		Assert:   []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(0)}},
		Throttle: throttle,
	}}}
}

func protos(t *testing.T) *adapters.ProtoRegistry {
	t.Helper()
	return adapters.NewProtoRegistry("../../tests/flows/grpc")
}

// TestResourceExhaustedIsThrottledNotFailed is the issue #28 acceptance: a
// service shedding load with RESOURCE_EXHAUSTED feeds throttle_rate in a load
// run and leaves error_rate alone, and the same responses fail an integration
// run. It is the same contract HTTP 429 has (ADR 0006), reached through a
// status vocabulary that shares no numbers with HTTP's.
func TestResourceExhaustedIsThrottledNotFailed(t *testing.T) {
	base := serveGRPC(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(context.Context, []byte) ([]byte, error) {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		},
	})

	load, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, 10, 300*time.Millisecond),
		Flows:    []ir.Flow{chargeFlow(nil)},
		BaseURL:  base,
		Protos:   protos(t),
	})
	if err != nil {
		t.Fatalf("load Run: %v", err)
	}
	if load.ThrottleRate() == 0 {
		t.Fatal("load: expected a nonzero throttle_rate")
	}
	if load.ErrorRate() != 0 {
		t.Fatalf("load: RESOURCE_EXHAUSTED must not count as an error, got error_rate %.2f", load.ErrorRate())
	}
	if load.Failed() != 0 || load.Aborted {
		t.Fatalf("load: failed=%d aborted=%v, want a clean throttled run", load.Failed(), load.Aborted)
	}
	if load.Throttled() != len(load.Samples) {
		t.Fatalf("load: %d of %d flow-runs throttled, want all", load.Throttled(), len(load.Samples))
	}

	integ, err := executor.Run(context.Background(), executor.Options{
		Schedule: &planner.Schedule{Mode: ir.ModeIntegration, Arrival: planner.Closed, Stop: planner.StopOnce, PeakVUs: 3},
		Flows:    []ir.Flow{chargeFlow(nil)},
		BaseURL:  base,
		Protos:   protos(t),
	})
	if err != nil {
		t.Fatalf("integration Run: %v", err)
	}
	if integ.ErrorRate() == 0 {
		t.Fatal("integration: the same throttles must fail the run")
	}
	if integ.ThrottleRate() == 0 {
		t.Fatal("integration: throttle_rate is still tracked everywhere")
	}
}

// A unary flow passes against the stub — the other half of the acceptance —
// and the response message is JSON by the time assertions and extraction see
// it, so they are the same code they are for HTTP.
func TestUnaryFlowPassesAndExtracts(t *testing.T) {
	base := serveGRPC(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(context.Context, []byte) ([]byte, error) {
			return []byte(`{"chargeId":"ch_77","status":"captured","amountCents":"1200"}`), nil
		},
		"billing.v1.Billing/Refund": func(_ context.Context, req []byte) ([]byte, error) {
			if string(req) != `{"chargeId":"ch_77"}` {
				return nil, status.Errorf(codes.InvalidArgument, "got %s", req)
			}
			return []byte(`{"status":"refunded"}`), nil
		},
	})

	flow := ir.Flow{Name: "billing", Steps: []ir.Step{
		{
			ID: "charge", Type: ir.StepGRPC,
			GRPC: &ir.GRPCSpec{
				Proto:   "billing.proto",
				Method:  "billing.v1.Billing/Charge",
				Message: []byte(`{"account":"acct_1","amountCents":"1200","currency":"GHS"}`),
			},
			Extract: []ir.Extraction{{Var: "charge_id", From: ir.ExtractBody, Path: "$.chargeId"}},
			Assert: []ir.Assertion{
				{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(0)},
				{Source: ir.AssertBody, Key: "$.status", Op: ir.OpEq, Value: []byte(`"captured"`)},
			},
		},
		{
			// The value extracted from the first message rides back out in the
			// second, JSON-escaped by the same templating a call body uses.
			ID: "refund", Type: ir.StepGRPC,
			GRPC: &ir.GRPCSpec{
				Proto:   "billing.proto",
				Method:  "billing.v1.Billing/Refund",
				Message: []byte(`{"chargeId":"{{ charge_id }}"}`),
			},
			Assert: []ir.Assertion{{Source: ir.AssertBody, Key: "$.status", Op: ir.OpEq, Value: []byte(`"refunded"`)}},
		},
	}}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: &planner.Schedule{Mode: ir.ModeIntegration, Arrival: planner.Closed, Stop: planner.StopOnce, PeakVUs: 1},
		Flows:    []ir.Flow{flow},
		BaseURL:  base,
		Protos:   protos(t),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ErrorRate() != 0 {
		t.Fatalf("error_rate = %.2f, want a clean run", res.ErrorRate())
	}
}

// A status the server chose to send is data, exactly as an HTTP 500 is: it
// does not fail the step on its own, and a flow that wants it to say so says
// so with an assertion.
func TestServerStatusIsDataUntilAssertedOn(t *testing.T) {
	base := serveGRPC(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(context.Context, []byte) ([]byte, error) {
			return nil, status.Error(codes.NotFound, "no such account")
		},
	})

	// Asserting the code the server sent: the step passes.
	expected := ir.Flow{Name: "billing", Steps: []ir.Step{{
		ID: "charge", Type: ir.StepGRPC,
		GRPC:   &ir.GRPCSpec{Proto: "billing.proto", Method: "billing.v1.Billing/Charge"},
		Assert: []ir.Assertion{{Source: ir.AssertStatus, Op: ir.OpEq, Value: val(int(codes.NotFound))}},
	}}}
	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: &planner.Schedule{Mode: ir.ModeIntegration, Arrival: planner.Closed, Stop: planner.StopOnce, PeakVUs: 1},
		Flows:    []ir.Flow{expected},
		BaseURL:  base,
		Protos:   protos(t),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ErrorRate() != 0 {
		t.Fatalf("error_rate = %.2f, want the expected NOT_FOUND to pass", res.ErrorRate())
	}

	// Asserting OK: the same response fails, and it is the assertion that says so.
	res, err = executor.Run(context.Background(), executor.Options{
		Schedule: &planner.Schedule{Mode: ir.ModeIntegration, Arrival: planner.Closed, Stop: planner.StopOnce, PeakVUs: 1},
		Flows:    []ir.Flow{chargeFlow(nil)},
		BaseURL:  base,
		Protos:   protos(t),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ErrorRate() == 0 {
		t.Fatal("asserting OK against a NOT_FOUND should fail")
	}
}

// An author whose gateway sheds load with a code of its own maps it, the way
// a step maps a 503 on the HTTP side — and the mapping is read in gRPC's
// numbering, where 14 is UNAVAILABLE and nothing is 503.
func TestAuthorMappedCodeIsThrottled(t *testing.T) {
	base := serveGRPC(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(context.Context, []byte) ([]byte, error) {
			return nil, status.Error(codes.Unavailable, "shedding")
		},
	})

	// Unmapped, UNAVAILABLE is a call that never reached the service: a failure.
	plain, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, 5, 200*time.Millisecond),
		Flows:    []ir.Flow{chargeFlow(nil)},
		BaseURL:  base,
		Protos:   protos(t),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if plain.ErrorRate() == 0 || plain.ThrottleRate() != 0 {
		t.Fatalf("unmapped UNAVAILABLE: want error, throttle 0 — got error_rate %.2f throttle_rate %.2f",
			plain.ErrorRate(), plain.ThrottleRate())
	}

	// Mapped, it is a throttle — which outranks the transport's own verdict,
	// because a throttle is a throttle whatever else is true of it (ADR 0006).
	mapped, err := executor.Run(context.Background(), executor.Options{
		Schedule: holdSchedule(ir.ModeLoad, 5, 200*time.Millisecond),
		Flows:    []ir.Flow{chargeFlow(&ir.ThrottleSpec{Statuses: []int{int(codes.Unavailable)}})},
		BaseURL:  base,
		Protos:   protos(t),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mapped.ThrottleRate() == 0 || mapped.ErrorRate() != 0 {
		t.Fatalf("mapped UNAVAILABLE: want throttled data — got error_rate %.2f throttle_rate %.2f",
			mapped.ErrorRate(), mapped.ThrottleRate())
	}
}

// A retry policy reads the gRPC code, so on_status: [14] retries UNAVAILABLE
// the way on_status: [503] retries a bad gateway.
func TestRetryReadsTheGRPCCode(t *testing.T) {
	var calls atomic.Int64
	base := serveGRPC(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(context.Context, []byte) ([]byte, error) {
			if calls.Add(1) == 1 {
				return nil, status.Error(codes.Unavailable, "warming up")
			}
			return []byte(`{"chargeId":"ch_1","status":"captured"}`), nil
		},
	})

	flow := chargeFlow(nil)
	flow.Steps[0].Retry = &ir.RetryPolicy{
		OnStatus:    []int{int(codes.Unavailable)},
		Backoff:     ir.BackoffFixed,
		MaxAttempts: 3,
		BaseDelay:   ir.Duration(10 * time.Millisecond),
	}

	res, err := executor.Run(context.Background(), executor.Options{
		Schedule: &planner.Schedule{Mode: ir.ModeIntegration, Arrival: planner.Closed, Stop: planner.StopOnce, PeakVUs: 1},
		Flows:    []ir.Flow{flow},
		BaseURL:  base,
		Protos:   protos(t),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ErrorRate() != 0 {
		t.Fatalf("error_rate = %.2f, want the retry to have covered the first failure", res.ErrorRate())
	}
	if calls.Load() < 2 {
		t.Fatalf("the stub saw %d call(s), want the step retried", calls.Load())
	}
}
