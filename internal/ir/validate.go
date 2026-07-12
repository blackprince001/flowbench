package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// identRe restricts flow names, step IDs, variable names, pool names, and
// endpoint references. Dots and "@" are excluded because the span-naming
// scheme reserves them: spans address steps as dot-paths
// ("login.http_call.tls") and variants as "name@variant" (ADRs 0007, 0009),
// so an ID containing either would corrupt structural span identity.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// templateRe matches "{{ ref }}" placeholders; refs are dotted paths whose
// first segment is the variable root ("user.email" → "user").
var templateRe = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)*)\s*\}\}`)

// openBraceRe finds anything that looks like a template opener, so malformed
// placeholders fail validation instead of going to the target verbatim.
var openBraceRe = regexp.MustCompile(`\{\{`)

// envRoot is the template root always available: "{{ env.VAR }}" resolves
// against the process environment at run time (ADR 0005).
const envRoot = "env"

// Validate checks the scenario is structurally sound and internally
// consistent: discriminated unions carry exactly the spec their type names,
// identifiers are span-safe, bounds are set where unbounded execution could
// hang a run, and every template reference resolves to the env, the flow's
// data pool, or an upstream extraction. All problems are reported at once
// via errors.Join; each error is prefixed with the path to the offending
// node. Cross-file concerns (target existence, endpoint catalog, pool file
// shape) belong to the parser and run-time binding, not here.
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
		path := fmt.Sprintf("flow %d (%q)", i, f.Name)
		if flowNames[f.Name] {
			errs = append(errs, errf(path, "duplicate flow name"))
		}
		flowNames[f.Name] = true
		errs = append(errs, f.validate(path, pools)...)
	}

	errs = append(errs, s.Profile.validate("profile")...)
	return errors.Join(errs...)
}

// Validate checks a target config in isolation; run-time binding checks the
// scenario against it (host allow-list, ceilings, disallowed modes).
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

	// Variable graph: template injection sees roots from the env, the bound
	// pool, and extractions of strictly earlier steps; var assertions also
	// see the current step's extractions (extraction precedes assertion
	// within a step — the PRD's login step asserts the token it extracted).
	available := map[string]bool{envRoot: true}
	if f.Data != "" {
		available[f.Data] = true
	}

	ids := map[string]bool{}
	for i, st := range f.Steps {
		sp := fmt.Sprintf("%s: step %d (%q)", path, i, st.ID)
		if ids[st.ID] {
			errs = append(errs, errf(sp, "duplicate step id"))
		}
		ids[st.ID] = true

		errs = append(errs, st.validate(sp)...)

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
	for _, set := range []bool{st.Call != nil, st.Logic != nil, st.Wait != nil, st.Poll != nil, st.Verify != nil} {
		if set {
			specs++
		}
	}
	if specs != 1 {
		errs = append(errs, errf(path, "exactly one spec must be set, found %d", specs))
	}

	specFor := map[StepType]bool{
		StepCall:   st.Call != nil,
		StepLogic:  st.Logic != nil,
		StepWait:   st.Wait != nil,
		StepPoll:   st.Poll != nil,
		StepVerify: st.Verify != nil,
	}
	matches, known := specFor[st.Type]
	switch {
	case !known:
		errs = append(errs, errf(path, "unknown step type %q (v0 executes call, logic, wait, poll, verify)", st.Type))
	case !matches:
		errs = append(errs, errf(path, "type is %q but the %q spec is not set", st.Type, st.Type))
	}

	switch {
	case st.Call != nil:
		errs = append(errs, st.Call.validate(path)...)
	case st.Logic != nil && st.Logic.Hook == "":
		errs = append(errs, errf(path, "logic step needs a hook name"))
	case st.Wait != nil && st.Wait.Duration <= 0:
		errs = append(errs, errf(path, "wait duration must be positive"))
	case st.Poll != nil:
		errs = append(errs, st.Poll.validate(path)...)
	case st.Verify != nil:
		if st.Verify.Connection == "" || st.Verify.Query == "" {
			errs = append(errs, errf(path, "verify step needs a connection and a query"))
		}
	}

	if st.Retry != nil {
		if st.Type != StepCall {
			errs = append(errs, errf(path, "retry policies apply to call steps only (poll bounds itself)"))
		}
		errs = append(errs, st.Retry.validate(path)...)
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

// templateRefs collects every well-formed "{{ ref }}" in the fields the
// engine templates when building this step's work: URL, headers, query,
// body, and verify args.
func (st *Step) templateRefs() []string {
	var refs []string
	for _, field := range st.templatedFields() {
		for _, m := range templateRe.FindAllStringSubmatch(field, -1) {
			refs = append(refs, m[1])
		}
	}
	return refs
}

// malformedTemplates reports "{{" openers that templateRe cannot parse, so
// a typo like "{{ user..email }}" fails validation rather than being sent
// to the target as a literal.
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
	case st.Verify != nil:
		fields = append(fields, st.Verify.Args...)
	}
	return fields
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
