package parser_test

import (
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
)

// TestParsesGRPCUnaryFixture reads the checked-in fixture, so the shape the
// docs show is the shape the parser accepts.
func TestParsesGRPCUnaryFixture(t *testing.T) {
	res, err := parser.ParseFlowFile("../../tests/flows/grpc_unary.flow.yaml", nil)
	if err != nil {
		t.Fatalf("fixture should parse, got:\n%v", err)
	}
	flow := res.Scenario.Flows[0]
	if len(flow.Steps) != 2 {
		t.Fatalf("want 2 steps, got %d", len(flow.Steps))
	}

	charge, refund := flow.Steps[0], flow.Steps[1]
	if charge.Type != ir.StepGRPC || charge.GRPC == nil {
		t.Fatalf("step 0 = %q with grpc=%v", charge.Type, charge.GRPC)
	}
	if charge.GRPC.Proto != "grpc/billing.proto" {
		t.Errorf("proto = %q", charge.GRPC.Proto)
	}
	if charge.GRPC.Method != "billing.v1.Billing/Charge" {
		t.Errorf("method = %q", charge.GRPC.Method)
	}
	// The method is the path, so the wire form carries the leading slash the
	// author does not have to write.
	if got := charge.GRPC.GRPCPath(); got != "/billing.v1.Billing/Charge" {
		t.Errorf("GRPCPath() = %q", got)
	}
	// No url: the target's base address is the whole address.
	if charge.GRPC.URL != "" {
		t.Errorf("url = %q, want none", charge.GRPC.URL)
	}
	if string(charge.GRPC.Message) != `{"account":"acct_1","amountCents":"1200","currency":"GHS"}` {
		t.Errorf("message = %s", charge.GRPC.Message)
	}
	// A step-level `headers:` block is gRPC metadata; it lands where a call
	// step's headers land, which is what lets auth reach a gRPC call.
	if got := charge.GRPC.Headers["x-request-id"]; got != "req-1" {
		t.Errorf("metadata x-request-id = %q", got)
	}
	if string(refund.GRPC.Message) != `{"chargeId":"{{ charge_id }}"}` {
		t.Errorf("refund message = %s", refund.GRPC.Message)
	}
}

func TestGRPCErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "no proto",
			src: `
flow: f
steps:
  - id: s
    grpc:
      method: a.B/C
`,
			want: "needs a proto",
		},
		{
			name: "no method",
			src: `
flow: f
steps:
  - id: s
    grpc:
      proto: a.proto
`,
			want: "needs a method",
		},
		{
			name: "method is not fully qualified",
			src: `
flow: f
steps:
  - id: s
    grpc:
      proto: a.proto
      method: Charge
`,
			want: "must be fully qualified as package.Service/Method",
		},
		{
			name: "an http url is not a grpc address",
			src: `
flow: f
steps:
  - id: s
    grpc:
      proto: a.proto
      method: a.B/C
      url: http://localhost:50051
`,
			want: "needs a grpc:// or grpcs:// scheme",
		},
		{
			name: "an address with a path",
			src: `
flow: f
steps:
  - id: s
    grpc:
      proto: a.proto
      method: a.B/C
      url: grpc://localhost:50051/a.B/C
`,
			want: "the method is the path",
		},
		{
			name: "message is not a mapping",
			src: `
flow: f
steps:
  - id: s
    grpc:
      proto: a.proto
      method: a.B/C
      message: [1, 2]
`,
			want: "must be a mapping of field to value",
		},
		{
			name: "call-shaped body has nowhere to go",
			src: `
flow: f
steps:
  - id: s
    grpc:
      proto: a.proto
      method: a.B/C
    body: { a: 1 }
`,
			want: "carries its payload in the message it sends",
		},
		{
			name: "unknown grpc key",
			src: `
flow: f
steps:
  - id: s
    grpc:
      proto: a.proto
      method: a.B/C
      timeout: 5s
`,
			want: `unknown grpc key "timeout"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseAuthFlowErr(t, tc.src); !strings.Contains(got, tc.want) {
				t.Errorf("error = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// A gRPC call is a request, so a flow-level credential reaches it — the same
// rule call and graphql steps get, and the one a new adapter is likeliest to
// forget.
func TestGRPCInheritsFlowAuth(t *testing.T) {
	res, err := parser.ParseFlow([]byte(`
flow: f
auth:
  scheme: bearer
  token: "{{ env.BILLING_TOKEN }}"
steps:
  - id: charge
    grpc:
      proto: a.proto
      method: a.B/C
`), "auth.flow.yaml", nil)
	if err != nil {
		t.Fatalf("should parse, got:\n%v", err)
	}
	step := res.Scenario.Flows[0].Steps[0]
	if step.Auth == nil || step.Auth.Scheme != ir.AuthBearer {
		t.Errorf("a grpc step should inherit the flow's auth, got %+v", step.Auth)
	}
}

// A templated address is still checked against the variable graph, so a step
// cannot call a host an earlier step has not produced yet.
func TestGRPCTemplatesAreCheckedAgainstTheVariableGraph(t *testing.T) {
	got := parseAuthFlowErr(t, `
flow: f
steps:
  - id: charge
    grpc:
      proto: a.proto
      method: a.B/C
      message:
        account: "{{ never_extracted }}"
`)
	if !strings.Contains(got, "never_extracted") {
		t.Errorf("error = %q, want it to name the unknown variable", got)
	}
}
