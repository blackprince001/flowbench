package parser_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
)

func parseAuthFlow(t *testing.T, src string) *ir.Flow {
	t.Helper()
	res, err := parser.ParseFlow([]byte(src), "auth.flow.yaml", nil)
	if err != nil {
		t.Fatalf("parsing:\n%v", err)
	}
	return &res.Scenario.Flows[0]
}

func parseAuthFlowErr(t *testing.T, src string) string {
	t.Helper()
	_, err := parser.ParseFlow([]byte(src), "auth.flow.yaml", nil)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	return err.Error()
}

// TestFlowAuthFlattensOntoSteps covers the declare-once surface: the executor
// only ever sees per-step auth, so the flow-level default has to be resolved
// at parse time — including the explicit opt-out and a step's own override.
func TestFlowAuthFlattensOntoSteps(t *testing.T) {
	flow := parseAuthFlow(t, `
flow: shop
auth:
  scheme: bearer
  token: "{{ env.TOKEN }}"
steps:
  - id: inherits
    call: GET /orders
  - id: opts_out
    call: GET /health
    auth: { scheme: none }
  - id: overrides
    call: GET /admin
    auth:
      scheme: api_key
      name: X-Api-Key
      value: "{{ env.KEY }}"
  - id: pause
    wait: 1s
`)

	inherits, optsOut, overrides, pause := flow.Steps[0], flow.Steps[1], flow.Steps[2], flow.Steps[3]

	if inherits.Auth == nil || inherits.Auth.Scheme != ir.AuthBearer || inherits.Auth.Token != "{{ env.TOKEN }}" {
		t.Errorf("step should have inherited the flow default, got %+v", inherits.Auth)
	}
	if optsOut.Auth != nil {
		t.Errorf(`"scheme: none" should leave the step bare, got %+v`, optsOut.Auth)
	}
	if overrides.Auth == nil || overrides.Auth.Scheme != ir.AuthAPIKey || overrides.Auth.Name != "X-Api-Key" {
		t.Errorf("step should have kept its own auth, got %+v", overrides.Auth)
	}
	// A wait step makes no request, so a flow default has nothing to attach to.
	if pause.Auth != nil {
		t.Errorf("a wait step should not inherit auth, got %+v", pause.Auth)
	}
}

func TestParsesEverySchemesFields(t *testing.T) {
	flow := parseAuthFlow(t, `
flow: schemes
steps:
  - id: oauth
    call: GET /a
    auth:
      scheme: oauth2_client_credentials
      token_url: https://issuer.example/token
      client_id: "{{ env.CLIENT_ID }}"
      client_secret: "{{ env.CLIENT_SECRET }}"
      scopes: [orders:read, orders:write]
  - id: signed
    call: POST /b
    auth:
      scheme: hmac
      secret: "{{ env.HMAC_SECRET }}"
      algorithm: sha512
      encoding: base64
      header: X-Sig
      key_id: k1
      key_id_header: X-Client
      timestamp_header: X-Ts
      sign: "{method}|{path}"
`)

	oauth := flow.Steps[0].Auth
	if oauth.TokenURL != "https://issuer.example/token" || oauth.ClientID != "{{ env.CLIENT_ID }}" {
		t.Errorf("oauth2 fields = %+v", oauth)
	}
	if len(oauth.Scopes) != 2 || oauth.Scopes[1] != "orders:write" {
		t.Errorf("oauth2 scopes = %v", oauth.Scopes)
	}

	signed := flow.Steps[1].Auth
	want := &ir.AuthSpec{
		Scheme: ir.AuthHMAC, Secret: "{{ env.HMAC_SECRET }}", Algorithm: "sha512",
		Encoding: "base64", Header: "X-Sig", KeyID: "k1", KeyIDHeader: "X-Client",
		TimestampHeader: "X-Ts", Sign: "{method}|{path}",
	}
	if !reflect.DeepEqual(signed, want) {
		t.Errorf("hmac fields =\n%+v\nwant\n%+v", signed, want)
	}
}

// TestSignTemplateIsNotAVariableTemplate guards the one place two brace
// vocabularies meet: `sign` uses single-brace placeholders, and the
// variable-graph checker must not read them as malformed `{{ }}` templates.
func TestSignTemplateIsNotAVariableTemplate(t *testing.T) {
	flow := parseAuthFlow(t, `
flow: signed
steps:
  - id: post
    call: POST /a
    auth:
      scheme: hmac
      secret: "{{ env.SECRET }}"
      sign: "{method}\n{path}\n{timestamp}\n{body_sha256}"
`)
	if got := flow.Steps[0].Auth.Sign; !strings.Contains(got, "{body_sha256}") {
		t.Errorf("sign template = %q", got)
	}
}

func TestAuthErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "missing scheme",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth: { token: "{{ env.T }}" }
`,
			want: "auth needs a scheme",
		},
		{
			name: "unknown scheme",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth: { scheme: magic }
`,
			want: `unknown auth scheme "magic"`,
		},
		{
			name: "unknown key",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth: { scheme: bearer, token: t, realm: r }
`,
			want: `unknown auth key "realm"`,
		},
		{
			name: "field from another scheme",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth: { scheme: bearer, token: t, password: p }
`,
			want: `auth field "password" does not apply to the "bearer" scheme`,
		},
		{
			name: "missing required field",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth: { scheme: basic, username: u }
`,
			want: `auth scheme "basic" needs "password"`,
		},
		{
			name: "bad api key location",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth: { scheme: api_key, name: k, value: v, in: body }
`,
			want: `auth in "body" must be`,
		},
		{
			name: "bad hmac algorithm",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth: { scheme: hmac, secret: s, algorithm: md5 }
`,
			want: `hmac algorithm "md5" must be sha256 or sha512`,
		},
		{
			name: "relative token url",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth:
      scheme: oauth2_client_credentials
      token_url: /token
      client_id: i
      client_secret: s
`,
			want: `token_url "/token" must be absolute`,
		},
		{
			name: "auth on a wait step",
			src: `
flow: f
steps:
  - id: s
    wait: 1s
    auth: { scheme: bearer, token: t }
`,
			want: "auth applies to steps that make a request",
		},
		{
			name: "credential with no upstream source",
			src: `
flow: f
steps:
  - id: s
    call: GET /a
    auth: { scheme: bearer, token: "{{ session_token }}" }
`,
			want: "has no upstream source",
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

// TestInheritedCredentialIsCheckedPerStep is the consequence of flattening a
// runtime-extracted token: the login step cannot send what it is about to
// fetch, and the variable graph says so instead of failing at request time.
func TestInheritedCredentialIsCheckedPerStep(t *testing.T) {
	src := `
flow: shop
auth:
  scheme: bearer
  token: "{{ token }}"
steps:
  - id: login
    call: POST /auth/login
    extract: { token: $.data.access_token }
  - id: orders
    call: GET /orders
`
	got := parseAuthFlowErr(t, src)
	if !strings.Contains(got, "has no upstream source") || !strings.Contains(got, `"login"`) {
		t.Errorf("error should point at the login step, got %q", got)
	}

	// Opting the login step out is the fix, and it parses.
	fixed := strings.Replace(src,
		"    call: POST /auth/login\n",
		"    call: POST /auth/login\n    auth: { scheme: none }\n", 1)
	flow := parseAuthFlow(t, fixed)
	if flow.Steps[0].Auth != nil {
		t.Errorf("login should be bare, got %+v", flow.Steps[0].Auth)
	}
	if flow.Steps[1].Auth == nil || flow.Steps[1].Auth.Token != "{{ token }}" {
		t.Errorf("orders should carry the extracted token, got %+v", flow.Steps[1].Auth)
	}
}
