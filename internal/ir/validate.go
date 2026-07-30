package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

var templateRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)*)\s*\}\}`)

var openBraceRe = regexp.MustCompile(`\{\{`)

const envRoot = "env"

func (s *Scenario) Validate() error {
	var errs []error
	if !identRe.MatchString(s.Name) {
		errs = append(errs, errf(`scenario`, "name %q must match %s", s.Name, identRe))
	}
	if s.Target != "" && !identRe.MatchString(s.Target) {
		errs = append(errs, errf(`scenario`, "target %q must match %s", s.Target, identRe))
	}

	pools := map[string]bool{}
	for i, p := range s.DataPools {
		path := fmt.Sprintf("data pool %d (%q)", i, p.Name)
		if pools[p.Name] {
			errs = append(errs, errf(path, "duplicate pool name"))
		}
		pools[p.Name] = true
		errs = append(errs, p.validate(path)...)
	}

	if len(s.Flows) == 0 {
		errs = append(errs, errf("scenario", "must contain at least one flow"))
	}
	flowNames := map[string]bool{}
	for i, f := range s.Flows {
		path := posPrefix(f.Pos) + fmt.Sprintf("flow %d (%q)", i, f.Name)
		if flowNames[f.Name] {
			errs = append(errs, errf(path, "duplicate flow name"))
		}
		flowNames[f.Name] = true
		errs = append(errs, f.validate(path, pools)...)
	}

	errs = append(errs, s.Profile.validate("profile")...)
	return errors.Join(errs...)
}

func (t *TargetConfig) Validate() error {
	var errs []error
	if !identRe.MatchString(t.Name) {
		errs = append(errs, errf("target", "name %q must match %s", t.Name, identRe))
	}
	if len(t.BaseURLs) == 0 {
		errs = append(errs, errf("target", "must declare at least one base URL (the host allow-list)"))
	}
	for _, raw := range t.BaseURLs {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, errf("target", "base URL %q must be absolute (scheme and host)", raw))
		}
	}
	if t.MaxVUs < 0 || t.MaxRPS < 0 {
		errs = append(errs, errf("target", "ceilings must not be negative"))
	}
	for _, m := range t.DisallowedModes {
		if !validMode(m) {
			errs = append(errs, errf("target", "unknown mode %q in disallowed_modes", m))
		}
	}
	return errors.Join(errs...)
}

func (p *DataPool) validate(path string) []error {
	var errs []error
	if !identRe.MatchString(p.Name) {
		errs = append(errs, errf(path, "name %q must match %s", p.Name, identRe))
	}
	if p.Source == "" {
		errs = append(errs, errf(path, "source file is required"))
	}
	switch p.Format {
	case "", PoolCSV, PoolJSON:
	default:
		errs = append(errs, errf(path, "unknown format %q", p.Format))
	}
	switch p.Distribution {
	case "", DistributeUniquePerVU, DistributeRoundRobin, DistributeRandom:
	default:
		errs = append(errs, errf(path, "unknown distribution %q", p.Distribution))
	}
	switch p.OnExhausted {
	case "", ExhaustFail, ExhaustCycle, ExhaustStop:
	default:
		errs = append(errs, errf(path, "unknown exhaustion policy %q", p.OnExhausted))
	}
	return errs
}

func (f *Flow) validate(path string, pools map[string]bool) []error {
	var errs []error
	if !identRe.MatchString(f.Name) {
		errs = append(errs, errf(path, "name %q must match %s", f.Name, identRe))
	}
	if f.Data != "" && !pools[f.Data] {
		errs = append(errs, errf(path, "data pool %q is not declared by the scenario", f.Data))
	}
	if len(f.Steps) == 0 {
		errs = append(errs, errf(path, "must contain at least one step"))
	}

	available := map[string]bool{envRoot: true}
	if f.Data != "" {
		available[f.Data] = true
	}

	// Sessions resolve the same way variables do: an earlier step opens one,
	// later steps work on it, and a reference to one nothing opens is a
	// pre-run error rather than a connection refused at request time.
	open := map[string]bool{}

	ids := map[string]bool{}
	for i, st := range f.Steps {
		sp := fmt.Sprintf("%s: %sstep %d (%q)", path, posPrefix(st.Pos), i, st.ID)
		if ids[st.ID] {
			errs = append(errs, errf(sp, "duplicate step id"))
		}
		ids[st.ID] = true

		errs = append(errs, st.validate(sp)...)
		if st.WS != nil {
			switch {
			case !st.WS.Opens() && !open[st.WS.Session]:
				errs = append(errs, errf(sp, "works on %s, which no earlier step opens", st.WS.describeSession()))
			case st.WS.Opens() && open[st.WS.Session]:
				errs = append(errs, errf(sp, "opens %s, which is already open in this flow (a session lives until the iteration ends)", st.WS.describeSession()))
			case st.WS.Opens():
				open[st.WS.Session] = true
			}
		}

		for _, ref := range st.templateRefs() {
			if !available[rootOf(ref)] {
				errs = append(errs, errf(sp,
					"template {{ %s }} has no upstream source: not the env, not the flow's data pool, and no earlier step extracts %q",
					ref, rootOf(ref)))
			}
		}
		errs = append(errs, st.malformedTemplates(sp)...)

		for _, ex := range st.Extract {
			if identRe.MatchString(ex.Var) {
				available[ex.Var] = true
			}
		}
		for j, a := range st.Assert {
			if a.Source == AssertVar && a.Key != "" && !available[a.Key] {
				errs = append(errs, errf(sp, "assertion %d checks var %q which nothing extracts", j, a.Key))
			}
		}
	}
	return errs
}

func (st *Step) validate(path string) []error {
	var errs []error
	if !identRe.MatchString(st.ID) {
		errs = append(errs, errf(path, "step id %q must match %s (dots and @ are reserved for span names)", st.ID, identRe))
	}

	specs := 0
	for _, set := range []bool{st.Call != nil, st.GraphQL != nil, st.WS != nil, st.GRPC != nil, st.Wait != nil, st.Poll != nil} {
		if set {
			specs++
		}
	}
	if specs != 1 {
		errs = append(errs, errf(path, "exactly one spec must be set, found %d", specs))
	}

	specFor := map[StepType]bool{
		StepCall:    st.Call != nil,
		StepGraphQL: st.GraphQL != nil,
		StepWS:      st.WS != nil,
		StepGRPC:    st.GRPC != nil,
		StepWait:    st.Wait != nil,
		StepPoll:    st.Poll != nil,
	}
	matches, known := specFor[st.Type]
	switch {
	case !known:
		errs = append(errs, errf(path, "unknown step type %q (v0 executes call, graphql, ws, grpc, wait, poll)", st.Type))
	case !matches:
		errs = append(errs, errf(path, "type is %q but the %q spec is not set", st.Type, st.Type))
	}

	switch {
	case st.Call != nil:
		errs = append(errs, st.Call.validate(path)...)
	case st.GraphQL != nil:
		errs = append(errs, st.GraphQL.validate(path)...)
	case st.WS != nil:
		errs = append(errs, st.WS.validate(path)...)
		errs = append(errs, st.wsFrameChecks(path)...)
	case st.GRPC != nil:
		errs = append(errs, st.GRPC.validate(path)...)
	case st.Wait != nil && st.Wait.Duration <= 0:
		errs = append(errs, errf(path, "wait duration must be positive"))
	case st.Poll != nil:
		errs = append(errs, st.Poll.validate(path)...)
	}

	if st.Retry != nil {
		if st.Type != StepCall && st.Type != StepGraphQL && st.Type != StepGRPC {
			// A ws step is deliberately excluded: the retry loop re-sends a
			// request and reads a status, and neither has a meaning on a
			// session that is already open.
			errs = append(errs, errf(path, "retry policies apply to call, graphql, and grpc steps only (poll bounds itself)"))
		}
		errs = append(errs, st.Retry.validate(path)...)
	}

	if st.Auth != nil {
		if !st.MakesRequest() {
			errs = append(errs, errf(path, "auth applies to steps that make a request (call, graphql, poll, and the ws step that opens a session), not %q steps", st.Type))
		}
		errs = append(errs, st.Auth.validate(path)...)
	}

	switch st.OnFailure {
	case "", FailureAbortFlow, FailureAbortRun, FailureRecord:
	default:
		errs = append(errs, errf(path, "unknown on_failure %q", st.OnFailure))
	}
	if st.Capture != nil {
		switch st.Capture.Payloads {
		case "", CaptureAlways, CaptureNever:
		default:
			errs = append(errs, errf(path, "unknown capture mode %q", st.Capture.Payloads))
		}
	}

	for i, ex := range st.Extract {
		errs = append(errs, ex.validate(fmt.Sprintf("%s: extract %d", path, i))...)
	}
	for i, a := range st.Assert {
		errs = append(errs, a.validate(fmt.Sprintf("%s: assert %d", path, i))...)
	}
	return errs
}

// authFields is the per-scheme field vocabulary. Anything set outside a
// scheme's own vocabulary is an error rather than a silent no-op, so a
// `password` under `scheme: bearer` is caught at parse time instead of
// authenticating as nothing at request time.
var authFields = map[AuthScheme]struct{ required, optional []string }{
	AuthNone:   {},
	AuthBearer: {required: []string{"token"}},
	AuthBasic:  {required: []string{"username", "password"}},
	AuthAPIKey: {required: []string{"name", "value"}, optional: []string{"in"}},
	AuthCookie: {required: []string{"name", "value"}},
	AuthOAuth2: {
		required: []string{"token_url", "client_id", "client_secret"},
		optional: []string{"scopes"},
	},
	AuthHMAC: {
		required: []string{"secret"},
		optional: []string{"algorithm", "encoding", "header", "key_id", "key_id_header", "timestamp_header", "sign"},
	},
}

func (a *AuthSpec) validate(path string) []error {
	vocab, known := authFields[a.Scheme]
	if !known {
		return []error{errf(path, "unknown auth scheme %q (one of %s)", a.Scheme, strings.Join(authSchemeNames(), ", "))}
	}

	var errs []error
	set := a.setFields()
	for _, name := range vocab.required {
		if !slices.Contains(set, name) {
			errs = append(errs, errf(path, "auth scheme %q needs %q", a.Scheme, name))
		}
	}
	allowed := append(append([]string{}, vocab.required...), vocab.optional...)
	for _, name := range set {
		if !slices.Contains(allowed, name) {
			errs = append(errs, errf(path, "auth field %q does not apply to the %q scheme", name, a.Scheme))
		}
	}

	switch a.In {
	case "", InHeader, InQuery:
	default:
		errs = append(errs, errf(path, "auth in %q must be %q or %q", a.In, InHeader, InQuery))
	}
	switch a.Algorithm {
	case "", "sha256", "sha512":
	default:
		errs = append(errs, errf(path, "hmac algorithm %q must be sha256 or sha512", a.Algorithm))
	}
	switch a.Encoding {
	case "", "hex", "base64":
	default:
		errs = append(errs, errf(path, "hmac encoding %q must be hex or base64", a.Encoding))
	}

	// A templated token URL is only known at request time, where the
	// allow-list gate checks it; a literal one is checked here.
	if a.TokenURL != "" && !strings.Contains(a.TokenURL, "{{") {
		if u, err := url.Parse(a.TokenURL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, errf(path, "oauth2 token_url %q must be absolute (scheme and host)", a.TokenURL))
		}
	}
	return errs
}

// setFields names the fields carrying a value, in a fixed order so errors
// read the same every run.
func (a *AuthSpec) setFields() []string {
	var set []string
	for _, f := range []struct {
		name  string
		value string
	}{
		{"token", a.Token},
		{"username", a.Username},
		{"password", a.Password},
		{"name", a.Name},
		{"value", a.Value},
		{"in", string(a.In)},
		{"token_url", a.TokenURL},
		{"client_id", a.ClientID},
		{"client_secret", a.ClientSecret},
		{"secret", a.Secret},
		{"algorithm", a.Algorithm},
		{"encoding", a.Encoding},
		{"header", a.Header},
		{"key_id", a.KeyID},
		{"key_id_header", a.KeyIDHeader},
		{"timestamp_header", a.TimestampHeader},
		{"sign", a.Sign},
	} {
		if f.value != "" {
			set = append(set, f.name)
		}
	}
	if len(a.Scopes) > 0 {
		set = append(set, "scopes")
	}
	return set
}

func authSchemeNames() []string {
	names := make([]string, 0, len(authFields))
	for s := range authFields {
		names = append(names, string(s))
	}
	sort.Strings(names)
	return names
}

func (c *CallSpec) validate(path string) []error {
	var errs []error
	inline := c.Method != "" || c.URL != ""
	switch {
	case c.Endpoint != "" && inline:
		errs = append(errs, errf(path, "call is either an endpoint reference or an inline method+url, not both"))
	case c.Endpoint != "":
		if !identRe.MatchString(c.Endpoint) {
			errs = append(errs, errf(path, "endpoint reference %q must match %s", c.Endpoint, identRe))
		}
	case c.Method == "" || c.URL == "":
		errs = append(errs, errf(path, "inline call needs both a method and a url"))
	}
	if len(c.Body) > 0 && !json.Valid(c.Body) {
		errs = append(errs, errf(path, "body is not valid JSON"))
	}
	return errs
}

// MakesRequest reports whether the step sends something over the wire. It is
// what decides whether auth has anything to attach itself to, so the parser
// and the validator have to agree on it — a new protocol adapter that forgets
// this line silently loses its credentials.
//
// A ws step counts only when it opens the session: credentials go on the
// handshake, which is an ordinary HTTP request, and a step working on a
// session someone else opened has no request left to decorate.
func (st *Step) MakesRequest() bool {
	return st.Call != nil || st.GraphQL != nil || st.GRPC != nil || st.Poll != nil ||
		(st.WS != nil && st.WS.Opens())
}

// grpcMethodRe matches a fully-qualified method, with or without the leading
// slash the wire uses: `billing.v1.Billing/Charge`.
var grpcMethodRe = regexp.MustCompile(`^/?[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*/[A-Za-z_][A-Za-z0-9_]*$`)

func (g *GRPCSpec) validate(path string) []error {
	var errs []error
	switch {
	case g.Endpoint != "" && g.URL != "":
		errs = append(errs, errf(path, "grpc is either an endpoint reference or an inline url, not both"))
	case g.Endpoint != "" && !identRe.MatchString(g.Endpoint):
		errs = append(errs, errf(path, "endpoint reference %q must match %s", g.Endpoint, identRe))
	case g.URL != "" && !strings.Contains(g.URL, "{{"):
		// An address is the whole URL — a gRPC step has no path, because the
		// method is the path. A `/` here is almost always someone reaching for
		// call-step muscle memory.
		u, err := url.Parse(g.URL)
		switch {
		case err != nil:
			errs = append(errs, errf(path, "grpc url %q does not parse: %v", g.URL, err))
		case u.Scheme != "grpc" && u.Scheme != "grpcs":
			errs = append(errs, errf(path, "grpc url %q needs a grpc:// or grpcs:// scheme (grpcs is TLS)", g.URL))
		case u.Host == "":
			errs = append(errs, errf(path, "grpc url %q names no host", g.URL))
		case strings.Trim(u.Path, "/") != "":
			errs = append(errs, errf(path, "grpc url %q carries a path; the method is the path, so the url is just the address", g.URL))
		}
	}

	if strings.TrimSpace(g.Proto) == "" {
		errs = append(errs, errf(path, "grpc step needs a proto (the .proto file describing the method)"))
	}
	switch {
	case g.Method == "":
		errs = append(errs, errf(path, "grpc step needs a method (package.Service/Method)"))
	case !grpcMethodRe.MatchString(g.Method):
		errs = append(errs, errf(path, "grpc method %q must be fully qualified as package.Service/Method", g.Method))
	}

	if len(g.Message) > 0 {
		if !json.Valid(g.Message) {
			errs = append(errs, errf(path, "grpc message is not valid JSON"))
		} else if g.Message[0] != '{' {
			// A protobuf message is a mapping of fields; a bare list or scalar
			// cannot become one, so catching it here beats a runtime decode
			// error on the first iteration.
			errs = append(errs, errf(path, "grpc message must be a mapping of field to value"))
		}
	}
	return errs
}

func (w *WSSpec) validate(path string) []error {
	var errs []error
	if w.Endpoint != "" && w.URL != "" {
		errs = append(errs, errf(path, "ws is either an endpoint reference or an inline url, not both"))
	}
	if w.Endpoint != "" && !identRe.MatchString(w.Endpoint) {
		errs = append(errs, errf(path, "endpoint reference %q must match %s", w.Endpoint, identRe))
	}
	if w.Session != "" && !identRe.MatchString(w.Session) {
		errs = append(errs, errf(path, "session name %q must match %s", w.Session, identRe))
	}

	if !w.Opens() && len(w.Send) == 0 && w.Receive == nil {
		errs = append(errs, errf(path, "a ws step must open a session (url), send a frame, or receive one"))
	}
	if !w.Opens() {
		if len(w.Headers) > 0 {
			errs = append(errs, errf(path, "ws headers ride on the handshake, so they belong to the step that opens the session"))
		}
		if len(w.Subprotocols) > 0 {
			errs = append(errs, errf(path, "ws subprotocols are negotiated in the handshake, so they belong to the step that opens the session"))
		}
	}

	if len(w.Send) > 0 && !json.Valid(w.Send) {
		errs = append(errs, errf(path, "ws send frame is not valid JSON"))
	}
	if w.Receive != nil {
		if w.Receive.Timeout < 0 {
			errs = append(errs, errf(path, "ws receive timeout must not be negative"))
		}
		for i, m := range w.Receive.Match {
			errs = append(errs, m.validate(fmt.Sprintf("%s: match %d", path, i))...)
			errs = append(errs, wsFrameSource(fmt.Sprintf("%s: match %d", path, i), m.Source)...)
		}
	}
	return errs
}

// wsFrameChecks holds a ws step's extractions and assertions to what a frame
// actually is. A step that receives nothing has no frame to read, and a frame
// is a payload — it has no status line and no headers, however much the
// handshake that opened the session did.
func (st *Step) wsFrameChecks(path string) []error {
	var errs []error
	if st.WS.Receive == nil {
		if len(st.Extract) > 0 {
			errs = append(errs, errf(path, "this ws step receives nothing, so there is no frame to extract from"))
		}
		if len(st.Assert) > 0 {
			errs = append(errs, errf(path, "this ws step receives nothing, so there is no frame to assert on"))
		}
		return errs
	}
	for i, ex := range st.Extract {
		switch ex.From {
		case ExtractHeader, ExtractStatus:
			errs = append(errs, errf(fmt.Sprintf("%s: extract %d", path, i),
				"a WebSocket frame has no %s; extract from the frame body (the handshake's status is classified by the engine)", ex.From))
		}
	}
	for i, a := range st.Assert {
		errs = append(errs, wsFrameSource(fmt.Sprintf("%s: assert %d", path, i), a.Source)...)
	}
	return errs
}

func wsFrameSource(path string, source AssertionSource) []error {
	switch source {
	case AssertStatus, AssertHeader:
		return []error{errf(path,
			"a WebSocket frame has no %s; assert on the frame body or its latency (the handshake's status is classified by the engine)", source)}
	}
	return nil
}

func (g *GraphQLSpec) validate(path string) []error {
	var errs []error
	inline := g.URL != ""
	switch {
	case g.Endpoint != "" && inline:
		errs = append(errs, errf(path, "graphql is either an endpoint reference or an inline url, not both"))
	case g.Endpoint != "":
		if !identRe.MatchString(g.Endpoint) {
			errs = append(errs, errf(path, "endpoint reference %q must match %s", g.Endpoint, identRe))
		}
	case g.URL == "":
		errs = append(errs, errf(path, "graphql step needs a url (the endpoint the operation posts to)"))
	}

	if strings.TrimSpace(g.Query) == "" {
		errs = append(errs, errf(path, "graphql step needs a query (the operation document)"))
	}
	if len(g.Variables) > 0 {
		if !json.Valid(g.Variables) {
			errs = append(errs, errf(path, "graphql variables are not valid JSON"))
		} else if g.Variables[0] != '{' {
			// The spec requires a map; a bare list or scalar is rejected by
			// every server, so catching it here beats a runtime 400.
			errs = append(errs, errf(path, "graphql variables must be a mapping of name to value"))
		}
	}
	switch g.OnErrors {
	case "", GraphQLErrorsFail, GraphQLErrorsAllowPartial, GraphQLErrorsIgnore:
	default:
		errs = append(errs, errf(path, "unknown graphql on_errors %q (fail, allow_partial, ignore)", g.OnErrors))
	}
	return errs
}

func (p *PollSpec) validate(path string) []error {
	var errs []error
	errs = append(errs, p.Call.validate(path)...)
	if len(p.Until) == 0 {
		errs = append(errs, errf(path, "poll needs at least one until condition"))
	}
	for i, a := range p.Until {
		errs = append(errs, a.validate(fmt.Sprintf("%s: until %d", path, i))...)
	}
	if p.Interval <= 0 {
		errs = append(errs, errf(path, "poll interval must be positive"))
	}
	if p.Timeout <= 0 && p.MaxAttempts < 1 {
		errs = append(errs, errf(path, "poll must be bounded by a timeout, max_attempts, or both"))
	}
	if p.Timeout < 0 || p.MaxAttempts < 0 {
		errs = append(errs, errf(path, "poll bounds must not be negative"))
	}
	return errs
}

func (r *RetryPolicy) validate(path string) []error {
	var errs []error
	if len(r.OnStatus) == 0 {
		errs = append(errs, errf(path, "retry needs at least one status in on_status"))
	}
	for _, code := range r.OnStatus {
		if code < 100 || code > 599 {
			errs = append(errs, errf(path, "retry on_status %d is not an HTTP status", code))
		}
	}
	switch r.Backoff {
	case BackoffFixed, BackoffExponential, BackoffHonorRetryAfter:
	default:
		errs = append(errs, errf(path, "unknown backoff strategy %q", r.Backoff))
	}
	if r.MaxAttempts < 1 {
		errs = append(errs, errf(path, "retry max_attempts must be at least 1 so runs stay bounded"))
	}
	if r.BaseDelay < 0 {
		errs = append(errs, errf(path, "retry base_delay must not be negative"))
	}
	return errs
}

func (e *Extraction) validate(path string) []error {
	var errs []error
	if !identRe.MatchString(e.Var) {
		errs = append(errs, errf(path, "var %q must match %s (dots and @ are reserved)", e.Var, identRe))
	}
	switch e.From {
	case "", ExtractBody, ExtractHeader:
		if e.Path == "" {
			errs = append(errs, errf(path, "extraction from the %s needs a path", cmpOr(string(e.From), string(ExtractBody))))
		}
	case ExtractStatus:
		if e.Path != "" {
			errs = append(errs, errf(path, "status extraction takes no path"))
		}
	default:
		errs = append(errs, errf(path, "unknown extraction source %q", e.From))
	}
	return errs
}

func (a *Assertion) validate(path string) []error {
	var errs []error
	switch a.Source {
	case AssertStatus, AssertLatency:
		if a.Key != "" {
			errs = append(errs, errf(path, "%s assertions take no key", a.Source))
		}
	case AssertHeader, AssertBody:
		if a.Key == "" {
			errs = append(errs, errf(path, "%s assertions need a key", a.Source))
		}
	case AssertVar:
		if !identRe.MatchString(a.Key) {
			errs = append(errs, errf(path, "var assertion key %q must match %s", a.Key, identRe))
		}
	default:
		errs = append(errs, errf(path, "unknown assertion source %q", a.Source))
	}

	switch a.Op {
	case OpExists, OpNotExists:
		if len(a.Value) > 0 {
			errs = append(errs, errf(path, "%s takes no value", a.Op))
		}
	case OpEq, OpNe, OpLt, OpLte, OpGt, OpGte, OpContains, OpMatches:
		if len(a.Value) == 0 {
			errs = append(errs, errf(path, "%s needs a value", a.Op))
		} else if !json.Valid(a.Value) {
			errs = append(errs, errf(path, "value is not valid JSON"))
		}
	default:
		errs = append(errs, errf(path, "unknown assertion op %q", a.Op))
	}
	return errs
}

func (p *Profile) validate(path string) []error {
	var errs []error
	if !validMode(p.Mode) {
		errs = append(errs, errf(path, "unknown mode %q", p.Mode))
	}
	if p.VUs < 0 || p.Iterations < 0 || p.Hold < 0 {
		errs = append(errs, errf(path, "vus, iterations, and hold must not be negative"))
	}
	for i, t := range p.Thresholds {
		if strings.TrimSpace(t) == "" {
			errs = append(errs, errf(path, "threshold %d is empty", i))
		}
	}
	return errs
}

func validMode(m Mode) bool {
	switch m {
	case ModeIntegration, ModeSystem, ModeLoad, ModeStress, ModeSoak:
		return true
	}
	return false
}

func (st *Step) templateRefs() []string {
	var refs []string
	for _, field := range st.templatedFields() {
		for _, m := range templateRe.FindAllStringSubmatch(field, -1) {
			refs = append(refs, m[1])
		}
	}
	return refs
}

func (st *Step) malformedTemplates(path string) []error {
	var errs []error
	for _, field := range st.templatedFields() {
		if len(openBraceRe.FindAllString(field, -1)) != len(templateRe.FindAllString(field, -1)) {
			errs = append(errs, errf(path, "malformed template placeholder in %q", field))
		}
	}
	return errs
}

func (st *Step) templatedFields() []string {
	var fields []string
	collect := func(c *CallSpec) {
		fields = append(fields, c.URL)
		for _, v := range c.Headers {
			fields = append(fields, v)
		}
		for _, v := range c.Query {
			fields = append(fields, v)
		}
		if len(c.Body) > 0 {
			fields = append(fields, string(c.Body))
		}
	}
	switch {
	case st.Call != nil:
		collect(st.Call)
	case st.Poll != nil:
		collect(&st.Poll.Call)
	case st.GraphQL != nil:
		// The query document is deliberately excluded: values reach a GraphQL
		// operation through variables, and a `{{ }}` spliced into the document
		// itself would be string-substituted into a query rather than sent as
		// a typed, escaped variable.
		fields = append(fields, st.GraphQL.URL)
		for _, v := range st.GraphQL.Headers {
			fields = append(fields, v)
		}
		if len(st.GraphQL.Variables) > 0 {
			fields = append(fields, string(st.GraphQL.Variables))
		}
	case st.WS != nil:
		fields = append(fields, st.WS.URL)
		for _, v := range st.WS.Headers {
			fields = append(fields, v)
		}
		if len(st.WS.Send) > 0 {
			fields = append(fields, string(st.WS.Send))
		}
	case st.GRPC != nil:
		// proto and method are excluded: they name a schema and a method in it,
		// resolved once per run, so a per-iteration value has nothing to say
		// about either.
		fields = append(fields, st.GRPC.URL)
		for _, v := range st.GRPC.Headers {
			fields = append(fields, v)
		}
		if len(st.GRPC.Message) > 0 {
			fields = append(fields, string(st.GRPC.Message))
		}
	}
	if st.Auth != nil {
		fields = append(fields, st.Auth.templatedFields()...)
	}
	return fields
}

// templatedFields is every auth field resolved through the scope at request
// time. Sign is excluded: its `{placeholder}` vocabulary is the signer's, not
// the templater's, and single braces would trip the malformed-template check.
func (a *AuthSpec) templatedFields() []string {
	fields := []string{
		a.Token, a.Username, a.Password, a.Name, a.Value,
		a.TokenURL, a.ClientID, a.ClientSecret,
		a.Secret, a.Algorithm, a.Encoding, a.Header,
		a.KeyID, a.KeyIDHeader, a.TimestampHeader,
	}
	return append(fields, a.Scopes...)
}

func rootOf(ref string) string {
	root, _, _ := strings.Cut(ref, ".")
	return root
}

func cmpOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func errf(path, format string, args ...any) error {
	return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
}

func posPrefix(p *Pos) string {
	if p == nil {
		return ""
	}
	return p.String() + ": "
}
