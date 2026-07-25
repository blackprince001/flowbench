package ir_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
)

func scenarioWithAuth(a *ir.AuthSpec) *ir.Scenario {
	return &ir.Scenario{
		Name:    "shop",
		Profile: ir.Profile{Mode: ir.ModeIntegration},
		Flows: []ir.Flow{{Name: "shop", Steps: []ir.Step{{
			ID:   "orders",
			Type: ir.StepCall,
			Call: &ir.CallSpec{Method: "GET", URL: "/orders"},
			Auth: a,
		}}}},
	}
}

func TestAuthSpecValidation(t *testing.T) {
	tests := []struct {
		name string
		spec *ir.AuthSpec
		want string // substring of the expected error; empty means it must pass
	}{
		{name: "bearer", spec: &ir.AuthSpec{Scheme: ir.AuthBearer, Token: "t"}},
		{name: "basic", spec: &ir.AuthSpec{Scheme: ir.AuthBasic, Username: "u", Password: "p"}},
		{name: "api key", spec: &ir.AuthSpec{Scheme: ir.AuthAPIKey, Name: "k", Value: "v", In: ir.InQuery}},
		{name: "cookie", spec: &ir.AuthSpec{Scheme: ir.AuthCookie, Name: "session", Value: "v"}},
		{name: "hmac minimal", spec: &ir.AuthSpec{Scheme: ir.AuthHMAC, Secret: "s"}},
		{
			name: "oauth2",
			spec: &ir.AuthSpec{
				Scheme: ir.AuthOAuth2, TokenURL: "https://issuer.example/token",
				ClientID: "i", ClientSecret: "s", Scopes: []string{"a"},
			},
		},
		{
			// Hand-written IR may carry it; both surfaces drop it at compile
			// time, and the executor treats it as a no-op.
			name: "explicit none",
			spec: &ir.AuthSpec{Scheme: ir.AuthNone},
		},
		{
			name: "templated token url skips the absolute check",
			spec: &ir.AuthSpec{
				Scheme: ir.AuthOAuth2, TokenURL: "{{ env.ISSUER }}/token",
				ClientID: "i", ClientSecret: "s",
			},
		},

		{
			name: "unknown scheme",
			spec: &ir.AuthSpec{Scheme: "kerberos"},
			want: `unknown auth scheme "kerberos"`,
		},
		{
			name: "cross-scheme field",
			spec: &ir.AuthSpec{Scheme: ir.AuthBearer, Token: "t", Secret: "s"},
			want: `auth field "secret" does not apply to the "bearer" scheme`,
		},
		{
			name: "missing required",
			spec: &ir.AuthSpec{Scheme: ir.AuthOAuth2, TokenURL: "https://i.example/t"},
			want: `needs "client_id"`,
		},
		{
			name: "none carrying credentials",
			spec: &ir.AuthSpec{Scheme: ir.AuthNone, Token: "t"},
			want: `auth field "token" does not apply to the "none" scheme`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := scenarioWithAuth(tc.spec).Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("expected %+v to validate, got %v", tc.spec, err)
			case tc.want != "" && err == nil:
				t.Fatalf("expected %+v to be refused for %q", tc.spec, tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestAuthSpecRoundTrips guards the wire shape both surfaces and the run store
// depend on: unset fields stay absent rather than serializing as empty.
func TestAuthSpecRoundTrips(t *testing.T) {
	spec := &ir.AuthSpec{Scheme: ir.AuthBearer, Token: "{{ env.TOKEN }}"}

	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(raw), `{"scheme":"bearer","token":"{{ env.TOKEN }}"}`; got != want {
		t.Errorf("marshaled = %s, want %s", got, want)
	}

	sc, err := ir.DecodeScenario(mustJSON(t, scenarioWithAuth(spec)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := sc.Flows[0].Steps[0].Auth; got == nil || got.Token != spec.Token {
		t.Errorf("auth did not survive the round trip: %+v", got)
	}
}

// TestUnknownAuthFieldIsRefused proves the strict decoder covers auth too, so
// a typo in hand-written or SDK-emitted IR fails loudly.
func TestUnknownAuthFieldIsRefused(t *testing.T) {
	raw := `{"name":"shop","profile":{"mode":"integration"},"flows":[{"name":"shop","steps":[
		{"id":"orders","type":"call","call":{"method":"GET","url":"/orders"},
		 "auth":{"scheme":"bearer","tokenn":"t"}}]}]}`

	if _, err := ir.DecodeScenario([]byte(raw)); err == nil || !strings.Contains(err.Error(), "tokenn") {
		t.Fatalf("expected the misspelled field to be refused, got %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
