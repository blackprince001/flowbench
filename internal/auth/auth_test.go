package auth_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/auth"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/secret"
)

// fixedClock is a signing timestamp a test can predict and advance.
var epoch = time.Unix(1_800_000_000, 0)

func noVars(ref string) (string, error) { return "", fmt.Errorf("no variable %q", ref) }

func apply(t *testing.T, p *auth.Provider, spec *ir.AuthSpec, req *adapters.Request) *secret.Set {
	t.Helper()
	secrets := secret.NewSet()
	if err := p.Apply(context.Background(), spec, req, noVars, secrets); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return secrets
}

func TestApplyIsNoOpWithoutAScheme(t *testing.T) {
	p := auth.NewProvider(auth.Options{})
	for _, spec := range []*ir.AuthSpec{nil, {Scheme: ir.AuthNone}} {
		req := &adapters.Request{Method: "GET", URL: "http://host/x"}
		apply(t, p, spec, req)
		if len(req.Headers) != 0 || len(req.Query) != 0 {
			t.Errorf("spec %v touched the request: headers=%v query=%v", spec, req.Headers, req.Query)
		}
	}
}

// TestHMACSignsTheDefaultCanonicalString pins the default: the signature
// covers method, path, timestamp, and a digest of the body, in that order.
func TestHMACSignsTheDefaultCanonicalString(t *testing.T) {
	p := auth.NewProvider(auth.Options{Now: func() time.Time { return epoch }})
	body := []byte(`{"amount":100}`)
	req := &adapters.Request{Method: "post", URL: "http://host/orders", Body: body}

	apply(t, p, &ir.AuthSpec{Scheme: ir.AuthHMAC, Secret: "shhh"}, req)

	digest := sha256.Sum256(body)
	canonical := strings.Join([]string{
		"POST", "/orders", fmt.Sprint(epoch.Unix()), hex.EncodeToString(digest[:]),
	}, "\n")
	mac := hmac.New(sha256.New, []byte("shhh"))
	mac.Write([]byte(canonical))

	if got, want := req.Headers["X-Signature"], hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Errorf("X-Signature = %q, want %q", got, want)
	}
	// Nothing asked for these, so nothing should have been sent.
	if _, ok := req.Headers["X-Timestamp"]; ok {
		t.Error("a timestamp header was sent without timestamp_header being set")
	}
	if _, ok := req.Headers["X-Key-Id"]; ok {
		t.Error("a key id header was sent without key_id being set")
	}
}

// TestHMACHonorsEveryOption is the escape hatch for services that sign
// something other than the default: a custom canonical string over the full
// placeholder vocabulary, sha512, base64, and renamed headers.
func TestHMACHonorsEveryOption(t *testing.T) {
	p := auth.NewProvider(auth.Options{Now: func() time.Time { return epoch }})
	body := []byte(`{"x":1}`)
	req := &adapters.Request{
		Method: "PUT",
		URL:    "http://host/v1/orders",
		Query:  map[string]string{"b": "2", "a": "1"},
		Body:   body,
	}

	apply(t, p, &ir.AuthSpec{
		Scheme:          ir.AuthHMAC,
		Secret:          "shhh",
		Algorithm:       "sha512",
		Encoding:        "base64",
		Header:          "X-Sig",
		KeyID:           "kid-7",
		KeyIDHeader:     "X-Client",
		TimestampHeader: "X-Ts",
		Sign:            "{method}|{path}|{query}|{body}|{key_id}|{timestamp}",
	}, req)

	timestamp := fmt.Sprint(epoch.Unix())
	canonical := strings.Join([]string{
		"PUT", "/v1/orders", "a=1&b=2", string(body), "kid-7", timestamp,
	}, "|")
	mac := hmac.New(sha512.New, []byte("shhh"))
	mac.Write([]byte(canonical))

	if got, want := req.Headers["X-Sig"], base64.StdEncoding.EncodeToString(mac.Sum(nil)); got != want {
		t.Errorf("X-Sig = %q, want %q (over %q)", got, want, canonical)
	}
	if got := req.Headers["X-Ts"]; got != timestamp {
		t.Errorf("X-Ts = %q, want %q", got, timestamp)
	}
	if got := req.Headers["X-Client"]; got != "kid-7" {
		t.Errorf("X-Client = %q, want %q", got, "kid-7")
	}
}

// TestHMACSignatureIsFreshPerApply is why auth runs per attempt: two signings
// a second apart must differ, so a retry never replays a stale timestamp.
func TestHMACSignatureIsFreshPerApply(t *testing.T) {
	now := epoch
	p := auth.NewProvider(auth.Options{Now: func() time.Time { return now }})
	spec := &ir.AuthSpec{Scheme: ir.AuthHMAC, Secret: "shhh"}

	req := &adapters.Request{Method: "GET", URL: "http://host/x"}
	apply(t, p, spec, req)
	first := req.Headers["X-Signature"]

	now = epoch.Add(time.Second)
	apply(t, p, spec, req)

	if req.Headers["X-Signature"] == first {
		t.Error("a re-signed request carried the previous attempt's signature")
	}
}

func TestCookieRidesAlongsideExistingOnes(t *testing.T) {
	p := auth.NewProvider(auth.Options{})
	req := &adapters.Request{
		Method:  "GET",
		URL:     "http://host/x",
		Headers: map[string]string{"Cookie": "theme=dark"},
	}
	spec := &ir.AuthSpec{Scheme: ir.AuthCookie, Name: "session", Value: "abc"}

	apply(t, p, spec, req)
	if got, want := req.Headers["Cookie"], "theme=dark; session=abc"; got != want {
		t.Errorf("Cookie = %q, want %q", got, want)
	}

	// Auth runs once per attempt, so re-applying must replace the pair rather
	// than send a second copy of it on the retry.
	apply(t, p, spec, req)
	if got, want := req.Headers["Cookie"], "theme=dark; session=abc"; got != want {
		t.Errorf("re-applied Cookie = %q, want %q", got, want)
	}
}

func TestBasicRegistersTheEncodedBlob(t *testing.T) {
	p := auth.NewProvider(auth.Options{})
	req := &adapters.Request{Method: "GET", URL: "http://host/x"}

	secrets := apply(t, p, &ir.AuthSpec{Scheme: ir.AuthBasic, Username: "u", Password: "p"}, req)

	blob := base64.StdEncoding.EncodeToString([]byte("u:p"))
	if got, want := req.Headers["Authorization"], "Basic "+blob; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if !secrets.Contains(blob) {
		t.Error("the basic blob was not registered for redaction; it decodes straight back to the password")
	}
}

// TestBearerRegistersTheToken guards a value that only self-registers when it
// resolves from {{ env.* }} (scope.go's Resolve): a token an earlier step
// extracted and carried forward — the login-then-act pattern auth.md
// documents — took a different path and was never registered before.
func TestBearerRegistersTheToken(t *testing.T) {
	p := auth.NewProvider(auth.Options{})
	req := &adapters.Request{Method: "GET", URL: "http://host/x"}

	secrets := apply(t, p, &ir.AuthSpec{Scheme: ir.AuthBearer, Token: "extracted-token"}, req)

	if got, want := req.Headers["Authorization"], "Bearer extracted-token"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if !secrets.Contains("extracted-token") {
		t.Error("the bearer token was not registered for redaction")
	}
}

// TestAPIKeyRegistersTheValue covers both places the value can ride, header
// or query, same reasoning as TestBearerRegistersTheToken.
func TestAPIKeyRegistersTheValue(t *testing.T) {
	p := auth.NewProvider(auth.Options{})

	headerReq := &adapters.Request{Method: "GET", URL: "http://host/x"}
	secrets := apply(t, p, &ir.AuthSpec{Scheme: ir.AuthAPIKey, Name: "X-Api-Key", Value: "extracted-key"}, headerReq)
	if !secrets.Contains("extracted-key") {
		t.Error("the header-borne api key was not registered for redaction")
	}

	queryReq := &adapters.Request{Method: "GET", URL: "http://host/x"}
	secrets = apply(t, p, &ir.AuthSpec{Scheme: ir.AuthAPIKey, Name: "api_key", Value: "extracted-key", In: ir.InQuery}, queryReq)
	if !secrets.Contains("extracted-key") {
		t.Error("the query-borne api key was not registered for redaction")
	}
}

// TestCookieRegistersTheValue is the same reasoning as
// TestBearerRegistersTheToken, for the cookie scheme.
func TestCookieRegistersTheValue(t *testing.T) {
	p := auth.NewProvider(auth.Options{})
	req := &adapters.Request{Method: "GET", URL: "http://host/x"}

	secrets := apply(t, p, &ir.AuthSpec{Scheme: ir.AuthCookie, Name: "session", Value: "extracted-session"}, req)

	if !secrets.Contains("extracted-session") {
		t.Error("the cookie value was not registered for redaction")
	}
}

// TestOAuth2FallsBackToBodyCredentials covers the half of RFC 6749 that HTTP
// Basic does not: a server that only accepts the credentials as form fields.
func TestOAuth2FallsBackToBodyCredentials(t *testing.T) {
	var grants, basicRejections atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grants.Add(1)
		_ = r.ParseForm()
		if _, _, ok := r.BasicAuth(); ok {
			basicRejections.Add(1)
			http.Error(w, "unsupported_authentication_method", http.StatusUnauthorized)
			return
		}
		if r.PostForm.Get("client_id") != "id" || r.PostForm.Get("client_secret") != "sh" {
			http.Error(w, "invalid_client", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
	}))
	defer srv.Close()

	p := auth.NewProvider(auth.Options{})
	spec := &ir.AuthSpec{
		Scheme: ir.AuthOAuth2, TokenURL: srv.URL, ClientID: "id", ClientSecret: "sh",
	}

	req := &adapters.Request{Method: "GET", URL: "http://host/x"}
	secrets := apply(t, p, spec, req)

	if got, want := req.Headers["Authorization"], "Bearer tok"; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if !secrets.Contains("tok") {
		t.Error("the access token was not registered for redaction")
	}
	if basicRejections.Load() != 1 {
		t.Errorf("expected exactly one Basic probe, saw %d", basicRejections.Load())
	}

	// The failed probe is remembered, so a later fetch goes straight to the
	// convention that worked.
	grants.Store(0)
	basicRejections.Store(0)
	apply(t, p, spec, &adapters.Request{Method: "GET", URL: "http://host/x"})
	if grants.Load() != 0 {
		t.Errorf("a cached token was refetched: %d grants", grants.Load())
	}
}

// TestOAuth2RefreshesBeforeExpiry proves a token is renewed ahead of its
// deadline rather than handed out until it dies mid-request.
func TestOAuth2RefreshesBeforeExpiry(t *testing.T) {
	var grants atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := grants.Add(1)
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, n)
	}))
	defer srv.Close()

	now := epoch
	p := auth.NewProvider(auth.Options{Now: func() time.Time { return now }})
	spec := &ir.AuthSpec{
		Scheme: ir.AuthOAuth2, TokenURL: srv.URL, ClientID: "id", ClientSecret: "sh",
	}

	req := &adapters.Request{Method: "GET", URL: "http://host/x"}
	apply(t, p, spec, req)
	if got := req.Headers["Authorization"]; got != "Bearer tok-1" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer tok-1")
	}

	// Comfortably inside the lifetime: still the cached token.
	now = epoch.Add(time.Hour - 5*time.Minute)
	apply(t, p, spec, req)
	if got := req.Headers["Authorization"]; got != "Bearer tok-1" {
		t.Errorf("a live token was replaced: %q", got)
	}

	// Inside the refresh margin: renewed before it can expire in flight.
	now = epoch.Add(time.Hour - 10*time.Second)
	apply(t, p, spec, req)
	if got := req.Headers["Authorization"]; got != "Bearer tok-2" {
		t.Errorf("an about-to-expire token was not refreshed: %q", got)
	}
}

// TestOAuth2FetchesOnceUnderConcurrency is the 10k-VU case: every VU asking
// for a token at run start must collapse into one grant, not a herd of them.
func TestOAuth2FetchesOnceUnderConcurrency(t *testing.T) {
	var grants atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grants.Add(1)
		time.Sleep(10 * time.Millisecond) // widen the window a herd would race through
		fmt.Fprint(w, `{"access_token":"tok","expires_in":3600}`)
	}))
	defer srv.Close()

	p := auth.NewProvider(auth.Options{})
	spec := &ir.AuthSpec{
		Scheme: ir.AuthOAuth2, TokenURL: srv.URL, ClientID: "id", ClientSecret: "sh",
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := &adapters.Request{Method: "GET", URL: "http://host/x"}
			if err := p.Apply(context.Background(), spec, req, noVars, secret.NewSet()); err != nil {
				t.Errorf("Apply: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := grants.Load(); got != 1 {
		t.Errorf("50 concurrent VUs triggered %d grants, want 1", got)
	}
}

func TestOAuth2SurfacesTokenEndpointFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := auth.NewProvider(auth.Options{})
	err := p.Apply(context.Background(), &ir.AuthSpec{
		Scheme: ir.AuthOAuth2, TokenURL: srv.URL, ClientID: "id", ClientSecret: "sh",
	}, &adapters.Request{Method: "GET", URL: "http://host/x"}, noVars, secret.NewSet())

	if err == nil || !strings.Contains(err.Error(), "answered 500") {
		t.Fatalf("expected the token endpoint's status to surface, got %v", err)
	}
}

func TestResolutionErrorsNameTheField(t *testing.T) {
	p := auth.NewProvider(auth.Options{})
	err := p.Apply(context.Background(),
		&ir.AuthSpec{Scheme: ir.AuthBearer, Token: "{{ missing }}"},
		&adapters.Request{Method: "GET", URL: "http://host/x"}, noVars, secret.NewSet())

	if err == nil || !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("expected the failing field to be named, got %v", err)
	}
}
