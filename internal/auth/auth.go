// Package auth applies a step's declared credentials to the outgoing request.
//
// Credentials themselves are never a FlowBench concept (ADR 0005): a spec
// carries `{{ env.* }}` references or values an earlier step extracted, and
// this package resolves them through the iteration's scope at request time.
// Resolution registers every env-sourced value as a secret, and the derived
// material this package produces that is reversible back to a credential —
// the basic-auth blob, an OAuth2 access token — is registered too, so none of
// it can reach an artifact.
//
// A Provider is shared by every VU in a run: one OAuth2 token fetch serves
// 10,000 virtual users rather than each fetching its own.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/secret"
)

// tokenRefreshMargin renews an OAuth2 token this long before it expires, so a
// request in flight never carries one that dies mid-connection.
const tokenRefreshMargin = 30 * time.Second

// defaultTokenTimeout bounds a token-endpoint fetch. It is deliberately
// shorter than a call timeout: the token endpoint gates every VU's first
// request, so a hanging one stalls the whole run.
const defaultTokenTimeout = 15 * time.Second

// maxTokenBody caps what is read from a token endpoint, so a target that
// answers with a firehose cannot exhaust memory.
const maxTokenBody = 1 << 20

// Resolver expands a `{{ ref }}` to its value — the iteration scope's
// Resolve, which registers env-sourced values as secrets as it goes.
type Resolver func(ref string) (string, error)

type Options struct {
	// Allow gates the OAuth2 token endpoint against the target's host
	// allow-list. Without it a flow could ship env-sourced client
	// credentials to any host it names (issue #8). Nil allows every host.
	Allow func(string) (bool, error)

	// Timeout bounds a token fetch; zero uses defaultTokenTimeout.
	Timeout time.Duration

	// Now is the clock the HMAC timestamp reads; nil uses time.Now.
	Now func() time.Time
}

// Provider applies auth specs and owns the run's shared OAuth2 token cache.
type Provider struct {
	client *http.Client
	allow  func(string) (bool, error)
	now    func() time.Time

	mu     sync.Mutex
	tokens map[string]*tokenEntry
}

func NewProvider(opts Options) *Provider {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTokenTimeout
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Provider{
		client: &http.Client{Timeout: timeout},
		allow:  opts.Allow,
		now:    now,
		tokens: map[string]*tokenEntry{},
	}
}

// Apply resolves spec against the iteration's scope and mutates req in place.
// It is called once per attempt rather than once per step, so a signature or
// timestamp is fresh on every retry instead of replaying a stale one.
func (p *Provider) Apply(ctx context.Context, spec *ir.AuthSpec, req *adapters.Request, resolve Resolver, secrets *secret.Set) error {
	if spec == nil || spec.Scheme == ir.AuthNone {
		return nil
	}
	r, err := expand(spec, resolve)
	if err != nil {
		return err
	}

	switch r.Scheme {
	case ir.AuthBearer:
		req.SetHeader("Authorization", "Bearer "+r.Token)
	case ir.AuthBasic:
		blob := base64.StdEncoding.EncodeToString([]byte(r.Username + ":" + r.Password))
		// The blob decodes straight back to the credential, so it is as
		// sensitive as the password itself.
		secrets.Add(blob)
		req.SetHeader("Authorization", "Basic "+blob)
	case ir.AuthAPIKey:
		if r.In == ir.InQuery {
			req.SetQuery(r.Name, r.Value)
		} else {
			req.SetHeader(r.Name, r.Value)
		}
	case ir.AuthCookie:
		req.AddCookie(r.Name, r.Value)
	case ir.AuthOAuth2:
		token, err := p.token(ctx, r)
		if err != nil {
			return err
		}
		secrets.Add(token)
		req.SetHeader("Authorization", "Bearer "+token)
	case ir.AuthHMAC:
		return p.sign(r, req)
	default:
		return fmt.Errorf("auth: unknown scheme %q", r.Scheme)
	}
	return nil
}

// expand resolves every templated field into a copy, leaving the IR the
// executor holds untouched — VUs share it. Sign is excluded: its
// `{placeholder}` vocabulary belongs to the signer, not the templater.
func expand(a *ir.AuthSpec, resolve Resolver) (*ir.AuthSpec, error) {
	out := *a
	for _, f := range []struct {
		name string
		dst  *string
	}{
		{"token", &out.Token},
		{"username", &out.Username},
		{"password", &out.Password},
		{"name", &out.Name},
		{"value", &out.Value},
		{"token_url", &out.TokenURL},
		{"client_id", &out.ClientID},
		{"client_secret", &out.ClientSecret},
		{"secret", &out.Secret},
		{"algorithm", &out.Algorithm},
		{"encoding", &out.Encoding},
		{"header", &out.Header},
		{"key_id", &out.KeyID},
		{"key_id_header", &out.KeyIDHeader},
		{"timestamp_header", &out.TimestampHeader},
	} {
		v, err := ir.ExpandTemplates(*f.dst, resolve)
		if err != nil {
			return nil, fmt.Errorf("auth %s: %w", f.name, err)
		}
		*f.dst = v
	}
	if len(a.Scopes) > 0 {
		scopes := make([]string, len(a.Scopes))
		for i, s := range a.Scopes {
			v, err := ir.ExpandTemplates(s, resolve)
			if err != nil {
				return nil, fmt.Errorf("auth scope %d: %w", i, err)
			}
			scopes[i] = v
		}
		out.Scopes = scopes
	}
	return &out, nil
}

// sign computes an HMAC over the canonical string and attaches it, plus the
// timestamp and key id when the spec asks for them.
func (p *Provider) sign(a *ir.AuthSpec, req *adapters.Request) error {
	newHash, err := hashFor(a.Algorithm)
	if err != nil {
		return err
	}
	u, err := req.FinalURL()
	if err != nil {
		return fmt.Errorf("auth hmac: %w", err)
	}

	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	timestamp := strconv.FormatInt(p.now().Unix(), 10)
	digest := sha256.Sum256(req.Body)

	canonical := strings.NewReplacer(
		"{method}", strings.ToUpper(req.Method),
		"{path}", path,
		"{query}", u.RawQuery,
		"{body}", string(req.Body),
		"{body_sha256}", hex.EncodeToString(digest[:]),
		"{timestamp}", timestamp,
		"{key_id}", a.KeyID,
	).Replace(cmpOr(a.Sign, ir.DefaultHMACSigningTemplate))

	mac := hmac.New(newHash, []byte(a.Secret))
	mac.Write([]byte(canonical))
	sum := mac.Sum(nil)

	signature := hex.EncodeToString(sum)
	if a.Encoding == "base64" {
		signature = base64.StdEncoding.EncodeToString(sum)
	}

	// The signature is per-request and not reversible to the secret, so it is
	// deliberately not registered as one: a run at 10k VUs would otherwise
	// grow the redaction set without bound and pay for it on every scrub.
	req.SetHeader(cmpOr(a.Header, ir.DefaultHMACHeader), signature)
	if a.TimestampHeader != "" {
		req.SetHeader(a.TimestampHeader, timestamp)
	}
	if a.KeyID != "" {
		req.SetHeader(cmpOr(a.KeyIDHeader, ir.DefaultHMACKeyIDHeader), a.KeyID)
	}
	return nil
}

func hashFor(algorithm string) (func() hash.Hash, error) {
	switch algorithm {
	case "", ir.DefaultHMACAlgorithm:
		return sha256.New, nil
	case "sha512":
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("auth hmac: unknown algorithm %q", algorithm)
	}
}

// tokenEntry is one cached client-credentials token. Its own mutex means a
// fetch for one token endpoint never blocks a request authenticating against
// another, while the herd of VUs waiting on the same endpoint at run start
// collapses into a single fetch.
type tokenEntry struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	// bodyAuth records that this endpoint rejected HTTP Basic and wants the
	// credentials in the form body, so later refreshes skip the probe.
	bodyAuth bool
}

// token returns a cached access token, fetching one when the cache is empty
// or the held token is within the refresh margin of expiring.
func (p *Provider) token(ctx context.Context, a *ir.AuthSpec) (string, error) {
	key := a.TokenURL + "\x00" + a.ClientID + "\x00" + strings.Join(a.Scopes, " ")

	p.mu.Lock()
	entry, ok := p.tokens[key]
	if !ok {
		entry = &tokenEntry{}
		p.tokens[key] = entry
	}
	p.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.token != "" && p.now().Add(tokenRefreshMargin).Before(entry.expiresAt) {
		return entry.token, nil
	}
	if p.allow != nil {
		ok, err := p.allow(a.TokenURL)
		if err != nil {
			return "", fmt.Errorf("auth oauth2: host allow-list check failed for %s: %w", a.TokenURL, err)
		}
		if !ok {
			return "", fmt.Errorf("auth oauth2: token_url %s is not an allowed target; add it to the target's base_urls", a.TokenURL)
		}
	}

	token, ttl, err := p.fetchToken(ctx, a, entry)
	if err != nil {
		return "", err
	}
	entry.token = token
	entry.expiresAt = p.now().Add(ttl)
	return token, nil
}

// fetchToken runs the client-credentials grant. RFC 6749 lets a server take
// the client credentials either as HTTP Basic or as form fields and does not
// let a client send both, so this probes Basic first (the spec's preference)
// and falls back to form fields on the rejection that indicates the other
// convention — the same auto-detection golang.org/x/oauth2 settled on.
func (p *Provider) fetchToken(ctx context.Context, a *ir.AuthSpec, entry *tokenEntry) (string, time.Duration, error) {
	if !entry.bodyAuth {
		token, ttl, retry, err := p.requestToken(ctx, a, false)
		if err == nil {
			return token, ttl, nil
		}
		if !retry {
			return "", 0, err
		}
	}
	token, ttl, _, err := p.requestToken(ctx, a, true)
	if err != nil {
		return "", 0, err
	}
	entry.bodyAuth = true
	return token, ttl, nil
}

// requestToken posts one grant. retry reports whether the failure was the
// server rejecting this credential-placement convention rather than a real
// problem, so only that case is worth trying the other way.
func (p *Provider) requestToken(ctx context.Context, a *ir.AuthSpec, inBody bool) (token string, ttl time.Duration, retry bool, err error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if len(a.Scopes) > 0 {
		form.Set("scope", strings.Join(a.Scopes, " "))
	}
	if inBody {
		form.Set("client_id", a.ClientID)
		form.Set("client_secret", a.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, false, fmt.Errorf("auth oauth2: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if !inBody {
		req.SetBasicAuth(url.QueryEscape(a.ClientID), url.QueryEscape(a.ClientSecret))
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return "", 0, false, fmt.Errorf("auth oauth2: token request to %s: %w", a.TokenURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return "", 0, false, fmt.Errorf("auth oauth2: reading token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// 400 and 401 are how a server that wants form fields rejects Basic.
		retry = !inBody && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized)
		return "", 0, retry, fmt.Errorf("auth oauth2: token endpoint %s answered %d", a.TokenURL, resp.StatusCode)
	}

	var payload struct {
		AccessToken string      `json:"access_token"`
		ExpiresIn   json.Number `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", 0, false, fmt.Errorf("auth oauth2: token response from %s is not JSON: %w", a.TokenURL, err)
	}
	if payload.AccessToken == "" {
		return "", 0, false, fmt.Errorf("auth oauth2: token response from %s carries no access_token", a.TokenURL)
	}

	// A server that states no lifetime gets one cache-miss per request rather
	// than a token cached forever.
	ttl = tokenRefreshMargin
	if secs, convErr := payload.ExpiresIn.Int64(); convErr == nil && secs > 0 {
		ttl = time.Duration(secs) * time.Second
	}
	return payload.AccessToken, ttl, false, nil
}

func cmpOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
