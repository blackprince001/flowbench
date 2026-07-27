package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/grpcstub"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/span"
)

const billingProto = "billing.proto"

// serveBilling starts the stub on a loopback port and returns its address.
func serveBilling(t *testing.T, handlers map[string]grpcstub.Handler) string {
	t.Helper()
	srv, err := grpcstub.New("testdata/" + billingProto)
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
	return "grpc://" + ln.Addr().String()
}

func billingMethod(t *testing.T, method string) *adapters.GRPCMethod {
	t.Helper()
	m, err := adapters.NewProtoRegistry("testdata").Method(context.Background(),
		&ir.GRPCSpec{Proto: billingProto, Method: method})
	if err != nil {
		t.Fatalf("resolving %s: %v", method, err)
	}
	return m
}

func grpcResolver(vars map[string]string) adapters.Resolver {
	return func(ref string) (string, error) {
		v, ok := vars[ref]
		if !ok {
			return "", errors.New("no such reference: " + ref)
		}
		return v, nil
	}
}

// invoke runs one call against address and returns what came back.
func invoke(t *testing.T, address string, method *adapters.GRPCMethod, req *adapters.Request) (*adapters.GRPCResponse, *span.Span, error) {
	t.Helper()
	conns := adapters.NewGRPCConns(5 * time.Second)
	t.Cleanup(conns.Close)
	req.URL = address + method.Path
	return conns.Invoke(context.Background(), "charge", &adapters.GRPCCall{Method: method, Request: req}, time.Now())
}

// A unary call is a message out and a message back, and the message back is
// JSON by the time anything downstream sees it — which is what lets extraction
// and assertions be the same code they are for HTTP.
func TestUnaryCallRoundTrip(t *testing.T) {
	address := serveBilling(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(_ context.Context, req []byte) ([]byte, error) {
			var in struct {
				Account     string `json:"account"`
				AmountCents string `json:"amountCents"`
			}
			if err := json.Unmarshal(req, &in); err != nil {
				return nil, err
			}
			return []byte(fmt.Sprintf(
				`{"chargeId":"ch_%s","status":"captured","amountCents":%s}`, in.Account, in.AmountCents)), nil
		},
	})

	req, err := adapters.BuildGRPCRequest(&ir.GRPCSpec{
		Message: json.RawMessage(`{"account":"{{ account }}","amountCents":"1200","currency":"GHS"}`),
	}, grpcResolver(map[string]string{"account": "acct_1"}))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	resp, sp, err := invoke(t, address, billingMethod(t, "billing.v1.Billing/Charge"), req)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Code != codes.OK {
		t.Fatalf("code = %s, want OK", resp.StatusText())
	}
	if got := string(resp.Body); !strings.Contains(got, `"chargeId":"ch_acct_1"`) {
		t.Errorf("body = %s, want the templated account echoed back", got)
	}
	if sp.Outcome != span.OutcomeOK {
		t.Errorf("span outcome = %q, want ok", sp.Outcome)
	}

	// The phase breakdown is the whole reason gRPC gets its own stats handler:
	// a reader comparing a grpc step to a call step should see one vocabulary.
	leg := child(t, sp, "grpc_call")
	for _, phase := range []string{"connect", "ttfb", "transfer"} {
		child(t, leg, phase)
	}
}

// A field holding its zero is still a field. protojson drops those by
// default, which would turn `$.amountCents == 0` into "extract found nothing"
// — an assertion about presence when the author asked about a value.
func TestZeroValuedFieldsSurvive(t *testing.T) {
	address := serveBilling(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(context.Context, []byte) ([]byte, error) {
			return []byte(`{"chargeId":"ch_0"}`), nil // status and amountCents left at their zeros
		},
	})

	req, _ := adapters.BuildGRPCRequest(&ir.GRPCSpec{}, grpcResolver(nil))
	resp, _, err := invoke(t, address, billingMethod(t, "billing.v1.Billing/Charge"), req)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	for _, field := range []string{"status", "amountCents"} {
		if _, ok := out[field]; !ok {
			t.Errorf("%q is missing from %s; a zero is a value, not an absence", field, resp.Body)
		}
	}
}

// A quote in an extracted value must not be able to rewrite the message, the
// same guarantee a call step's body has.
func TestMessageValuesAreJSONEscaped(t *testing.T) {
	req, err := adapters.BuildGRPCRequest(&ir.GRPCSpec{
		Message: json.RawMessage(`{"account":"{{ account }}"}`),
	}, grpcResolver(map[string]string{"account": `a","amountCents":"999`}))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(req.Body, &out); err != nil {
		t.Fatalf("templated message is not valid JSON: %v (%s)", err, req.Body)
	}
	if _, injected := out["amountCents"]; injected {
		t.Errorf("an extracted value smuggled in a second field: %s", req.Body)
	}
}

// A status the server chose to send is a response, not a transport failure:
// the flow's own assertions judge it, exactly as they judge an HTTP 404.
func TestServerStatusIsAResponseNotAnError(t *testing.T) {
	address := serveBilling(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(context.Context, []byte) ([]byte, error) {
			return nil, status.Error(codes.NotFound, "no such account")
		},
	})

	req, _ := adapters.BuildGRPCRequest(&ir.GRPCSpec{}, grpcResolver(nil))
	resp, _, err := invoke(t, address, billingMethod(t, "billing.v1.Billing/Charge"), req)
	if err != nil {
		t.Fatalf("a status the server sent should not be an error: %v", err)
	}
	if resp.Code != codes.NotFound {
		t.Fatalf("code = %s, want NOT_FOUND", resp.StatusText())
	}
	if resp.StatusText() != "NOT_FOUND" {
		t.Errorf("status text = %q, want the canonical NOT_FOUND", resp.StatusText())
	}
	if resp.Message != "no such account" {
		t.Errorf("message = %q, want the server's own", resp.Message)
	}
	if len(resp.Body) != 0 {
		t.Errorf("body = %s, want none — a non-OK call carries no message", resp.Body)
	}
}

// A target that is not there is a failed call, not a target with an opinion.
// Without this the run would report 0% errors against nothing at all.
func TestUnreachableTargetIsAFailedCall(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := "grpc://" + ln.Addr().String()
	ln.Close() // nothing is listening now

	req, _ := adapters.BuildGRPCRequest(&ir.GRPCSpec{}, grpcResolver(nil))
	_, sp, err := invoke(t, address, billingMethod(t, "billing.v1.Billing/Charge"), req)
	if err == nil {
		t.Fatal("calling a dead address should be an error")
	}
	if !strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Errorf("error = %v, want it to name the status", err)
	}
	if sp.Outcome != span.OutcomeFailed {
		t.Errorf("span outcome = %q, want failed", sp.Outcome)
	}
}

// Metadata is HTTP/2 headers, so a step's headers — and every auth scheme that
// writes one — reach a gRPC call unchanged, and come back the same way.
func TestMetadataRidesBothWays(t *testing.T) {
	address := serveBilling(t, map[string]grpcstub.Handler{
		"billing.v1.Billing/Charge": func(ctx context.Context, _ []byte) ([]byte, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer tok_1" {
				return nil, status.Errorf(codes.Unauthenticated, "authorization = %v", got)
			}
			grpc.SetTrailer(ctx, metadata.Pairs("x-served-by", "stub-1"))
			return []byte(`{"chargeId":"ch_1","status":"captured"}`), nil
		},
	})

	req, _ := adapters.BuildGRPCRequest(&ir.GRPCSpec{
		Headers: map[string]string{"Authorization": "Bearer {{ token }}"},
	}, grpcResolver(map[string]string{"token": "tok_1"}))

	resp, _, err := invoke(t, address, billingMethod(t, "billing.v1.Billing/Charge"), req)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Code != codes.OK {
		t.Fatalf("code = %s (%s), want OK", resp.StatusText(), resp.Message)
	}
	if got := resp.Headers.Get("x-served-by"); got != "stub-1" {
		t.Errorf("trailer x-served-by = %q, want stub-1 — headers and trailers are one lookup", got)
	}
}

// A streaming method is refused by name, before a run starts, rather than
// failing somewhere less legible once it is under way.
func TestStreamingMethodIsRefused(t *testing.T) {
	_, err := adapters.NewProtoRegistry("testdata").Method(context.Background(),
		&ir.GRPCSpec{Proto: billingProto, Method: "billing.v1.Billing/Watch"})
	if err == nil {
		t.Fatal("a streaming method should be refused")
	}
	if !strings.Contains(err.Error(), "streaming") || !strings.Contains(err.Error(), "unary") {
		t.Errorf("error = %v, want it to say the method streams and that v1 is unary-only", err)
	}
}

// A typo in a method or service name is the likeliest failure, so the error
// puts the alternatives on screen instead of only saying no.
func TestUnknownMethodListsWhatExists(t *testing.T) {
	reg := adapters.NewProtoRegistry("testdata")

	_, err := reg.Method(context.Background(), &ir.GRPCSpec{Proto: billingProto, Method: "billing.v1.Billing/Charg"})
	if err == nil || !strings.Contains(err.Error(), "Refund") {
		t.Errorf("error = %v, want it to list the methods that do exist", err)
	}

	_, err = reg.Method(context.Background(), &ir.GRPCSpec{Proto: billingProto, Method: "billing.v2.Billing/Charge"})
	if err == nil || !strings.Contains(err.Error(), "billing.v1.Billing") {
		t.Errorf("error = %v, want it to list the services that do exist", err)
	}
}

// The bundled compiler's reach is a property of a pinned dependency, so it is
// pinned by a test rather than asserted in the ADR: proto2, proto3 and
// edition 2023 all compile.
func TestEverySupportedProtoSyntaxCompiles(t *testing.T) {
	reg := adapters.NewProtoRegistry("testdata")
	for _, tc := range []struct{ proto, method string }{
		{"legacy.proto", "legacy.v1.Echo/Ping"},     // proto2
		{billingProto, "billing.v1.Billing/Charge"}, // proto3
		{"modern.proto", "modern.v1.Echo/Ping"},     // edition 2023
	} {
		if _, err := reg.Method(context.Background(), &ir.GRPCSpec{Proto: tc.proto, Method: tc.method}); err != nil {
			t.Errorf("%s: %v", tc.proto, err)
		}
	}
}

func TestCompilingAMissingProtoSaysWhich(t *testing.T) {
	_, err := adapters.NewProtoRegistry("testdata").Method(context.Background(),
		&ir.GRPCSpec{Proto: "nope.proto", Method: "a.B/C"})
	if err == nil || !strings.Contains(err.Error(), "nope.proto") {
		t.Errorf("error = %v, want it to name the file", err)
	}
}

func TestGRPCCodeNamesAreCanonical(t *testing.T) {
	for code, want := range map[codes.Code]string{
		codes.OK:                "OK",
		codes.ResourceExhausted: "RESOURCE_EXHAUSTED",
		codes.NotFound:          "NOT_FOUND",
		codes.Unavailable:       "UNAVAILABLE",
		codes.Code(99):          "CODE_99",
	} {
		if got := adapters.GRPCCodeName(code); got != want {
			t.Errorf("GRPCCodeName(%d) = %q, want %q", code, got, want)
		}
	}
}

func child(t *testing.T, parent *span.Span, name string) *span.Span {
	t.Helper()
	for _, c := range parent.Children {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("span %q has no %q child (has %v)", parent.Name, name, childNames(parent))
	return nil
}

func childNames(sp *span.Span) []string {
	var names []string
	for _, c := range sp.Children {
		names = append(names, c.Name)
	}
	return names
}
