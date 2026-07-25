package executor_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/auth"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
	"github.com/blackprince001/flowbench/internal/secret"
	"github.com/blackprince001/flowbench/internal/target"
)

// Credentials the fixture flows under tests/flows/auth resolve from the
// environment. Every stub below answers 200 only when the credential arrived
// intact, so a scheme that quietly sends nothing fails rather than passes.
const (
	testBearer       = "hdr.payload.sig-0123456789"
	testUser         = "checkout-service"
	testPassword     = "p@ss:word-with-a-colon"
	testAPIKey       = "ak_live_9f8e7d6c5b4a"
	testSession      = "sess-abc123def456"
	testClientID     = "client-abc"
	testClientSecret = "sh-secret-987"
	testAccessToken  = "at_issued_by_the_stub_0001"
	testHMACSecret   = "hs_top_secret_key"
	testHMACKeyID    = "key-2026-07"
)

// TestAuthSchemeFixtures is the issue #30 acceptance: one fixture flow per
// scheme, each run end to end against a stub that verifies the credential.
func TestAuthSchemeFixtures(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		handler http.Handler
		// env is the process environment the fixture's `{{ env.* }}`
		// references resolve against, given the stub's base URL.
		env func(baseURL string) map[string]string
	}{
		{
			name:    "bearer",
			fixture: "bearer.flow.yaml",
			handler: expectHeader(t, "/whoami", "Authorization", "Bearer "+testBearer),
			env: func(string) map[string]string {
				return map[string]string{"FLOWBENCH_TEST_BEARER": testBearer}
			},
		},
		{
			name:    "basic",
			fixture: "basic.flow.yaml",
			handler: expectHeader(t, "/whoami", "Authorization",
				"Basic "+base64.StdEncoding.EncodeToString([]byte(testUser+":"+testPassword))),
			env: func(string) map[string]string {
				return map[string]string{
					"FLOWBENCH_TEST_USER":     testUser,
					"FLOWBENCH_TEST_PASSWORD": testPassword,
				}
			},
		},
		{
			name:    "api_key",
			fixture: "api_key.flow.yaml",
			handler: apiKeyStub(t),
			env: func(string) map[string]string {
				return map[string]string{"FLOWBENCH_TEST_API_KEY": testAPIKey}
			},
		},
		{
			name:    "cookie",
			fixture: "cookie.flow.yaml",
			handler: cookieStub(t),
			env: func(string) map[string]string {
				return map[string]string{"FLOWBENCH_TEST_SESSION": testSession}
			},
		},
		{
			name:    "oauth2_client_credentials",
			fixture: "oauth2.flow.yaml",
			handler: oauth2Stub(t, nil),
			env: func(baseURL string) map[string]string {
				return map[string]string{
					"FLOWBENCH_TEST_TOKEN_URL":     baseURL + "/token",
					"FLOWBENCH_TEST_CLIENT_ID":     testClientID,
					"FLOWBENCH_TEST_CLIENT_SECRET": testClientSecret,
				}
			},
		},
		{
			name:    "hmac",
			fixture: "hmac.flow.yaml",
			handler: hmacStub(t),
			env: func(string) map[string]string {
				return map[string]string{
					"FLOWBENCH_TEST_HMAC_SECRET": testHMACSecret,
					"FLOWBENCH_TEST_HMAC_KEY_ID": testHMACKeyID,
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			for k, v := range tc.env(srv.URL) {
				t.Setenv(k, v)
			}

			it, _ := runAuthFixture(t, tc.fixture, srv.URL)
			if len(it.Failures) > 0 {
				t.Fatalf("fixture %s did not authenticate: %v", tc.fixture, it.Failures)
			}
		})
	}
}

// TestFlowLevelAuthDefault covers the declare-once surface: steps inherit the
// flow's auth, one overrides it, and `scheme: none` opts one out — the stub
// refusing any step that carries the wrong credential.
func TestFlowLevelAuthDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/whoami", requireHeader(t, "Authorization", "Bearer "+testBearer))
	mux.HandleFunc("/whoami-key", requireHeader(t, "X-Api-Key", testAPIKey))
	mux.HandleFunc("/public", func(w http.ResponseWriter, r *http.Request) {
		// The opted-out step must arrive bare: an inherited default leaking
		// onto it is the bug this endpoint exists to catch.
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("opted-out step still sent Authorization: %q", got)
			http.Error(w, "unexpected credential", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("FLOWBENCH_TEST_BEARER", testBearer)
	t.Setenv("FLOWBENCH_TEST_API_KEY", testAPIKey)

	it, _ := runAuthFixture(t, "flow_default.flow.yaml", srv.URL)
	if len(it.Failures) > 0 {
		t.Fatalf("flow-level auth fixture failed: %v", it.Failures)
	}
}

// TestDerivedCredentialsAreRedacted is the redaction half of the acceptance.
// The env-sourced values are registered by the scope as it resolves them; what
// this proves is that the material auth *derives* from them — the basic blob
// and an OAuth2 access token, both of which hand an attacker the account — is
// registered too, and so cannot reach a stored artifact.
func TestDerivedCredentialsAreRedacted(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		blob := base64.StdEncoding.EncodeToString([]byte(testUser + ":" + testPassword))
		t.Setenv("FLOWBENCH_TEST_USER", testUser)
		t.Setenv("FLOWBENCH_TEST_PASSWORD", testPassword)

		srv := httptest.NewServer(http.HandlerFunc(echoAuthorization))
		defer srv.Close()

		it, scope := runAuthFixture(t, "basic.flow.yaml", srv.URL, forcedMiss()...)
		assertRedacted(t, it, scope, blob, testPassword)
	})

	t.Run("oauth2", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/token", tokenEndpoint(t, nil))
		mux.HandleFunc("/whoami", echoAuthorization)
		srv := httptest.NewServer(mux)
		defer srv.Close()

		t.Setenv("FLOWBENCH_TEST_TOKEN_URL", srv.URL+"/token")
		t.Setenv("FLOWBENCH_TEST_CLIENT_ID", testClientID)
		t.Setenv("FLOWBENCH_TEST_CLIENT_SECRET", testClientSecret)

		it, scope := runAuthFixture(t, "oauth2.flow.yaml", srv.URL, forcedMiss()...)
		assertRedacted(t, it, scope, testAccessToken, testClientSecret)
	})
}

// forcedMiss is an assertion that cannot pass, added to a fixture's first step
// so the echoed credential lands in a recorded failure detail — the artifact
// redaction has to scrub.
func forcedMiss() []ir.Assertion {
	return []ir.Assertion{{
		Source: ir.AssertBody, Key: "$.seen", Op: ir.OpEq, Value: val("no-match"),
	}}
}

func assertRedacted(t *testing.T, it *executor.Iteration, scope *executor.Scope, derived string, alsoSecret ...string) {
	t.Helper()

	if !scope.Secrets().Contains(derived) {
		t.Errorf("derived credential was never registered as a secret")
	}
	if len(it.Failures) == 0 {
		t.Fatal("expected the forced assertion miss to be recorded")
	}
	for _, f := range it.Failures {
		for _, leaked := range append([]string{derived}, alsoSecret...) {
			if strings.Contains(f.Detail, leaked) {
				t.Errorf("failure detail leaked a credential: %s", f.Detail)
			}
		}
		if !strings.Contains(f.Detail, secret.Placeholder) {
			t.Errorf("failure detail carries the echoed credential unredacted: %s", f.Detail)
		}
	}
}

// runAuthFixture parses a fixture under tests/flows/auth and runs it at one VU
// against baseURL, through the same target gate a real run uses. Extra
// assertions are appended to the first step, for the redaction cases that need
// a failure to inspect.
func runAuthFixture(t *testing.T, fixture, baseURL string, extra ...ir.Assertion) (*executor.Iteration, *executor.Scope) {
	t.Helper()

	path := "../../tests/flows/auth/" + fixture
	res, err := parser.ParseFlowFile(path, nil)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	tgt, err := target.New(&ir.TargetConfig{Name: "stub", BaseURLs: []string{baseURL}})
	if err != nil {
		t.Fatalf("building target: %v", err)
	}
	if err := tgt.Check(res.Scenario); err != nil {
		t.Fatalf("target gate refused %s: %v", path, err)
	}

	flow := res.Scenario.Flows[0]
	if len(extra) > 0 {
		flow.Steps[0].Assert = append(flow.Steps[0].Assert, extra...)
		flow.Steps[0].OnFailure = ir.FailureRecord
	}

	scope := executor.NewScope(flow.Data, nil)
	runner := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: tgt.BaseURL(),
		Mode:    res.Scenario.Profile.Mode,
		Allow:   tgt.Allows,
		Auth:    auth.NewProvider(auth.Options{Allow: tgt.Allows}),
	}
	it, err := runner.RunFlow(context.Background(), flow, scope)
	if err != nil {
		t.Fatalf("running %s: %v", path, err)
	}
	return it, scope
}

// echoAuthorization reflects the credential back into the body, the shape a
// leaky endpoint takes and the reason redaction exists.
func echoAuthorization(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `{"seen":%q}`, r.Header.Get("Authorization"))
}

func requireHeader(t *testing.T, name, want string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(name); got != want {
			t.Errorf("%s: %s = %q, want %q", r.URL.Path, name, got, want)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}
}

func expectHeader(t *testing.T, path, name, want string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(path, requireHeader(t, name, want))
	return mux
}

func apiKeyStub(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/whoami", requireHeader(t, "X-Api-Key", testAPIKey))
	mux.HandleFunc("/whoami-query", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api_key"); got != testAPIKey {
			t.Errorf("api_key query param = %q, want %q", got, testAPIKey)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	return mux
}

func cookieStub(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil || c.Value != testSession {
			t.Errorf("session cookie = %v (err %v), want %q", c, err, testSession)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	return mux
}

// tokenEndpoint is a client-credentials token endpoint. hits, when non-nil,
// counts grants so a test can prove the run fetched once and cached.
func tokenEndpoint(t *testing.T, hits *atomic.Int64) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("token request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if got := r.PostForm.Get("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", got)
		}
		id, secretVal, ok := r.BasicAuth()
		if !ok {
			id, secretVal = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		if id != testClientID || secretVal != testClientSecret {
			http.Error(w, "invalid_client", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, testAccessToken)
	}
}

func oauth2Stub(t *testing.T, hits *atomic.Int64) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", tokenEndpoint(t, hits))
	mux.HandleFunc("/whoami", requireHeader(t, "Authorization", "Bearer "+testAccessToken))
	return mux
}

// hmacStub recomputes the default canonical string from what it received and
// compares — so the test pins the signature to the wire, not to the signer's
// own idea of it.
func hmacStub(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading signed body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("X-Key-Id"); got != testHMACKeyID {
			t.Errorf("X-Key-Id = %q, want %q", got, testHMACKeyID)
		}
		timestamp := r.Header.Get("X-Timestamp")
		if timestamp == "" {
			t.Error("signed request carried no X-Timestamp")
		}

		digest := sha256.Sum256(body)
		canonical := strings.Join([]string{
			r.Method, r.URL.EscapedPath(), timestamp, hex.EncodeToString(digest[:]),
		}, "\n")
		mac := hmac.New(sha256.New, []byte(testHMACSecret))
		mac.Write([]byte(canonical))
		want := hex.EncodeToString(mac.Sum(nil))

		if got := r.Header.Get("X-Signature"); !hmac.Equal([]byte(got), []byte(want)) {
			t.Errorf("X-Signature = %q, want %q (over %q)", got, want, canonical)
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	return mux
}

// TestOAuth2TokenFetchedOncePerRun proves the shared cache: many iterations
// against the same token endpoint issue one grant, not one each. Without it a
// 10k-VU run would open with 10k grants and be rate-limited by its own auth.
func TestOAuth2TokenFetchedOncePerRun(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(oauth2Stub(t, &hits))
	defer srv.Close()

	t.Setenv("FLOWBENCH_TEST_TOKEN_URL", srv.URL+"/token")
	t.Setenv("FLOWBENCH_TEST_CLIENT_ID", testClientID)
	t.Setenv("FLOWBENCH_TEST_CLIENT_SECRET", testClientSecret)

	res, err := parser.ParseFlowFile("../../tests/flows/auth/oauth2.flow.yaml", nil)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	tgt, err := target.New(&ir.TargetConfig{Name: "stub", BaseURLs: []string{srv.URL}})
	if err != nil {
		t.Fatalf("building target: %v", err)
	}

	// One provider, as a run has — shared across the iterations below.
	provider := auth.NewProvider(auth.Options{Allow: tgt.Allows})
	flow := res.Scenario.Flows[0]
	for i := 0; i < 5; i++ {
		runner := &executor.Runner{
			Session: adapters.NewSession(adapters.SessionOptions{}),
			BaseURL: tgt.BaseURL(),
			Mode:    ir.ModeIntegration,
			Allow:   tgt.Allows,
			Auth:    provider,
		}
		it, err := runner.RunFlow(context.Background(), flow, executor.NewScope("", nil))
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if len(it.Failures) > 0 {
			t.Fatalf("iteration %d failed: %v", i, it.Failures)
		}
	}

	if got := hits.Load(); got != 1 {
		t.Errorf("token endpoint saw %d grants across 5 iterations, want 1", got)
	}
}

// TestOAuth2TokenURLMustBeAllowed closes the exfiltration path: without the
// gate, naming any host as token_url would ship env-sourced client credentials
// to it, straight past the allow-list that governs every other request.
func TestOAuth2TokenURLMustBeAllowed(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("credentials reached a host outside the allow-list")
		fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600}`, testAccessToken)
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	t.Setenv("FLOWBENCH_TEST_TOKEN_URL", elsewhere.URL+"/token")
	t.Setenv("FLOWBENCH_TEST_CLIENT_ID", testClientID)
	t.Setenv("FLOWBENCH_TEST_CLIENT_SECRET", testClientSecret)

	res, err := parser.ParseFlowFile("../../tests/flows/auth/oauth2.flow.yaml", nil)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	tgt, err := target.New(&ir.TargetConfig{Name: "stub", BaseURLs: []string{srv.URL}})
	if err != nil {
		t.Fatalf("building target: %v", err)
	}

	runner := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: tgt.BaseURL(),
		Mode:    ir.ModeIntegration,
		Allow:   tgt.Allows,
		Auth:    auth.NewProvider(auth.Options{Allow: tgt.Allows}),
	}
	it, err := runner.RunFlow(context.Background(), res.Scenario.Flows[0], executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("RunFlow: %v", err)
	}
	// A refused token endpoint is a recorded step failure, the same shape a
	// refused call takes — the run reports it rather than crashing out.
	if len(it.Failures) != 1 || !strings.Contains(it.Failures[0].Detail, "not an allowed target") {
		t.Fatalf("expected the token endpoint to be refused, got %v", it.Failures)
	}
}

// TestStaticTokenURLIsGatedPreRun proves the same host rule applies before the
// run starts when the token URL is a literal rather than templated.
func TestStaticTokenURLIsGatedPreRun(t *testing.T) {
	sc := &ir.Scenario{
		Name:    "exfiltrate",
		Profile: ir.Profile{Mode: ir.ModeIntegration},
		Flows: []ir.Flow{{Name: "exfiltrate", Steps: []ir.Step{{
			ID:   "call",
			Type: ir.StepCall,
			Call: &ir.CallSpec{Method: "GET", URL: "/whoami"},
			Auth: &ir.AuthSpec{
				Scheme:       ir.AuthOAuth2,
				TokenURL:     "https://attacker.example/token",
				ClientID:     "id",
				ClientSecret: "secret",
			},
		}}}},
	}
	tgt, err := target.New(&ir.TargetConfig{Name: "local", BaseURLs: []string{"http://localhost:8080"}})
	if err != nil {
		t.Fatalf("building target: %v", err)
	}
	err = tgt.Check(sc)
	if err == nil || !strings.Contains(err.Error(), "attacker.example") {
		t.Fatalf("expected the pre-run gate to name the token host, got %v", err)
	}
}
