package parser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/blackprince001/flowbench/internal/ir"
)

// dataPoolVar is the variable root the YAML `data:` shorthand binds rows to.
const dataPoolVar = "user"

type Options struct {
	// PriorStepIDs maps flow name → step IDs earlier runs recorded; Parse
	// warns when one is missing, since a rename silently breaks cross-run
	// folding.
	PriorStepIDs map[string][]string
}

type Warning struct {
	Pos *ir.Pos
	Msg string
}

func (w Warning) String() string {
	if w.Pos != nil {
		return w.Pos.String() + ": warning: " + w.Msg
	}
	return "warning: " + w.Msg
}

type Result struct {
	Scenario *ir.Scenario
	Warnings []Warning
}

func ParseFlowFile(path string, opts *Options) (*Result, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read flow file: %w", err)
	}
	return ParseFlow(src, path, opts)
}

func ParseFlow(src []byte, filename string, opts *Options) (*Result, error) {
	file, err := parser.ParseBytes(src, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	if len(file.Docs) != 1 || file.Docs[0].Body == nil {
		return nil, fmt.Errorf("%s: a flow file holds exactly one YAML document", filename)
	}

	w := &walker{file: filename}
	sc := w.scenario(file.Docs[0].Body)
	if err := errors.Join(w.errs...); err != nil {
		return nil, err
	}
	if err := sc.Validate(); err != nil {
		return nil, err
	}

	res := &Result{Scenario: sc}
	if opts != nil {
		res.Warnings = renameWarnings(sc, opts.PriorStepIDs)
	}
	return res, nil
}

func renameWarnings(sc *ir.Scenario, prior map[string][]string) []Warning {
	var ws []Warning
	for i := range sc.Flows {
		f := &sc.Flows[i]
		current := map[string]bool{}
		for _, st := range f.Steps {
			current[st.ID] = true
		}
		for _, id := range prior[f.Name] {
			if !current[id] {
				ws = append(ws, Warning{Pos: f.Pos, Msg: fmt.Sprintf(
					"flow %q no longer has a step named %q; prior runs reference that name, so cross-run folding for it will break — if this is a rename, flame data continuity is lost",
					f.Name, id)})
			}
		}
	}
	return ws
}

// walker maps the YAML AST to IR, collecting every error with its position.
type walker struct {
	file string
	errs []error
}

func (w *walker) scenario(body ast.Node) *ir.Scenario {
	sc := &ir.Scenario{Profile: ir.Profile{Mode: ir.ModeIntegration}}
	flow := ir.Flow{Pos: w.pos(body)}

	entries, ok := mapEntries(body)
	if !ok {
		w.errAt(body, "a flow file is a mapping with flow/auth/data/steps/profile keys")
		return sc
	}
	var flowAuth *ir.AuthSpec
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "flow":
			flow.Name, _ = w.str(e.Value, "flow name")
		case "auth":
			flowAuth = w.auth(e.Value)
		case "data":
			if src, ok := w.str(e.Value, "data"); ok {
				sc.DataPools = []ir.DataPool{{Name: dataPoolVar, Source: src}}
				flow.Data = dataPoolVar
			}
		case "steps":
			if seq, ok := w.seq(e.Value, "steps"); ok {
				for _, item := range seq.Values {
					flow.Steps = append(flow.Steps, w.step(item))
				}
			}
		case "profile":
			sc.Profile = w.profile(e.Value)
		default:
			w.errAt(keyNode, "unknown key %q in flow file (expected flow, auth, data, steps, profile)", key)
		}
	}
	if flow.Name == "" {
		w.errAt(body, `missing required key "flow" (the flow's name)`)
	}
	flattenAuth(&flow, flowAuth)

	sc.Name = flow.Name
	sc.Flows = []ir.Flow{flow}
	return sc
}

func (w *walker) step(n ast.Node) ir.Step {
	st := ir.Step{Pos: w.pos(n)}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "a step is a mapping with an id and one of call, graphql, ws, grpc, wait, poll")
		return st
	}

	var callNode, graphqlNode, wsNode, grpcNode, waitNode, pollNode ast.Node
	var headers, query map[string]string
	var body json.RawMessage
	var headersNode, queryNode, bodyNode ast.Node
	var callOnlyNodes []ast.Node

	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "id":
			st.ID, _ = w.str(e.Value, "step id")
		case "call":
			callNode = e.Value
		case "graphql":
			graphqlNode = e.Value
		case "ws":
			wsNode = e.Value
		case "grpc":
			grpcNode = e.Value
		case "wait":
			waitNode = e.Value
		case "poll":
			pollNode = e.Value
		case "headers":
			headers = w.strMap(e.Value, "headers")
			headersNode = keyNode
			callOnlyNodes = append(callOnlyNodes, keyNode)
		case "query":
			query = w.strMap(e.Value, "query")
			queryNode = keyNode
			callOnlyNodes = append(callOnlyNodes, keyNode)
		case "body":
			body = w.bodyJSON(e.Value, "body")
			bodyNode = keyNode
			callOnlyNodes = append(callOnlyNodes, keyNode)
		case "extract":
			st.Extract = w.extractions(e.Value)
		case "assert":
			st.Assert = w.assertions(e.Value)
		case "retry":
			st.Retry = w.retry(e.Value)
		case "throttle":
			st.Throttle = w.throttle(e.Value)
		case "auth":
			st.Auth = w.auth(e.Value)
		case "on_failure":
			if s, ok := w.str(e.Value, "on_failure"); ok {
				st.OnFailure = ir.FailureAction(s)
			}
		default:
			w.errAt(keyNode, "unknown step key %q", key)
		}
	}

	kinds := 0
	for _, kn := range []ast.Node{callNode, graphqlNode, wsNode, grpcNode, waitNode, pollNode} {
		if kn != nil {
			kinds++
		}
	}
	switch {
	case kinds == 0:
		w.errAt(n, "step %q needs one of call, graphql, ws, grpc, wait, poll", st.ID)
	case kinds > 1:
		w.errAt(n, "step %q sets more than one of call, graphql, ws, grpc, wait, poll", st.ID)
	case callNode != nil:
		st.Type = ir.StepCall
		spec := w.callShorthand(callNode)
		spec.Headers, spec.Query, spec.Body = headers, query, body
		st.Call = spec
	case graphqlNode != nil:
		st.Type = ir.StepGraphQL
		spec := w.graphql(graphqlNode)
		// Headers are still ordinary HTTP headers on an ordinary POST, so they
		// stay where a call step puts them. Values, though, reach a GraphQL
		// operation as variables — a URL query string or a body of its own has
		// nowhere to go.
		spec.Headers = headers
		for _, kn := range []ast.Node{queryNode, bodyNode} {
			if kn != nil {
				w.errAt(kn, "a graphql step carries its values in the operation's variables, not query or body")
			}
		}
		st.GraphQL = spec
	case wsNode != nil:
		st.Type = ir.StepWS
		spec := w.ws(wsNode)
		// Headers are handshake headers on the HTTP request that opens the
		// session, so they stay where a call step puts them. A query string or
		// a body of its own has nowhere to go: a ws step's payload is the
		// frame it sends.
		spec.Headers = headers
		if headersNode != nil && !spec.Opens() {
			w.errAt(headersNode, "headers ride on the handshake, so they belong to the ws step that opens the session")
		}
		for _, kn := range []ast.Node{queryNode, bodyNode} {
			if kn != nil {
				w.errAt(kn, "a ws step carries its payload in the frame it sends, not query or body")
			}
		}
		st.WS = spec
	case grpcNode != nil:
		st.Type = ir.StepGRPC
		spec := w.grpc(grpcNode)
		// Metadata is HTTP/2 headers on the wire, so it stays where a call step
		// puts its headers and every auth scheme reaches a gRPC call unchanged.
		// A query string and a body have nowhere to go: the method is the path
		// and the payload is the message.
		spec.Headers = headers
		for _, kn := range []ast.Node{queryNode, bodyNode} {
			if kn != nil {
				w.errAt(kn, "a grpc step carries its payload in the message it sends, not query or body")
			}
		}
		st.GRPC = spec
	case waitNode != nil:
		st.Type = ir.StepWait
		d := w.duration(waitNode, "wait")
		st.Wait = &ir.WaitSpec{Duration: d}
		w.rejectCallOnly(callOnlyNodes, "wait")
	case pollNode != nil:
		st.Type = ir.StepPoll
		st.Poll = w.poll(pollNode)
		w.rejectCallOnly(callOnlyNodes, "poll (put them inside the poll block)")
	}
	return st
}

func (w *walker) rejectCallOnly(nodes []ast.Node, kind string) {
	for _, n := range nodes {
		w.errAt(n, "headers, query, and body belong to call steps, not %s steps", kind)
	}
}

func (w *walker) callShorthand(n ast.Node) *ir.CallSpec {
	s, ok := w.str(n, "call")
	if !ok {
		return &ir.CallSpec{}
	}
	method, rest, found := strings.Cut(strings.TrimSpace(s), " ")
	if !found || method != strings.ToUpper(method) || strings.TrimSpace(rest) == "" {
		w.errAt(n, `call must look like "POST /path" (an uppercase method, a space, a URL)`)
		return &ir.CallSpec{}
	}
	return &ir.CallSpec{Method: method, URL: strings.TrimSpace(rest)}
}

func (w *walker) graphql(n ast.Node) *ir.GraphQLSpec {
	spec := &ir.GraphQLSpec{}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "graphql is a mapping with url, query, and optionally variables, operation_name, on_errors")
		return spec
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "url":
			spec.URL, _ = w.str(e.Value, "url")
		case "endpoint":
			spec.Endpoint, _ = w.str(e.Value, "endpoint")
		case "query":
			spec.Query, _ = w.text(e.Value, "query")
		case "variables":
			spec.Variables = w.bodyJSON(e.Value, "variables")
		case "operation_name":
			spec.Operation, _ = w.str(e.Value, "operation_name")
		case "on_errors":
			if s, ok := w.str(e.Value, "on_errors"); ok {
				spec.OnErrors = ir.GraphQLErrorPolicy(s)
			}
		default:
			w.errAt(keyNode, "unknown graphql key %q", key)
		}
	}
	return spec
}

// ws reads one step's worth of WebSocket work. A `url` opens a session; its
// absence means the step joins one an earlier step opened.
func (w *walker) ws(n ast.Node) *ir.WSSpec {
	spec := &ir.WSSpec{}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "ws is a mapping with url (to open a session), session, send, receive")
		return spec
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "url":
			spec.URL, _ = w.str(e.Value, "url")
		case "endpoint":
			spec.Endpoint, _ = w.str(e.Value, "endpoint")
		case "session":
			spec.Session, _ = w.str(e.Value, "session")
		case "subprotocols":
			if seq, ok := w.seq(e.Value, "subprotocols"); ok {
				for _, item := range seq.Values {
					if s, ok := w.str(item, "subprotocol"); ok {
						spec.Subprotocols = append(spec.Subprotocols, s)
					}
				}
			}
		case "send":
			spec.Send = w.bodyJSON(e.Value, "send")
		case "receive":
			spec.Receive = w.wsReceive(e.Value)
		default:
			w.errAt(keyNode, "unknown ws key %q", key)
		}
	}
	return spec
}

// grpc reads one unary call. `url` is optional and is only an address: the
// method is the path, so a step against the target's own base URL names no url
// at all.
func (w *walker) grpc(n ast.Node) *ir.GRPCSpec {
	spec := &ir.GRPCSpec{}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "grpc is a mapping with proto, method, and optionally url, message, import_paths")
		return spec
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "proto":
			spec.Proto, _ = w.str(e.Value, "proto")
		case "import_paths":
			if seq, ok := w.seq(e.Value, "import_paths"); ok {
				for _, item := range seq.Values {
					if s, ok := w.str(item, "import path"); ok {
						spec.ImportPaths = append(spec.ImportPaths, s)
					}
				}
			}
		case "method":
			spec.Method, _ = w.str(e.Value, "method")
		case "url":
			spec.URL, _ = w.str(e.Value, "url")
		case "endpoint":
			spec.Endpoint, _ = w.str(e.Value, "endpoint")
		case "message":
			spec.Message = w.bodyJSON(e.Value, "message")
		default:
			w.errAt(keyNode, "unknown grpc key %q", key)
		}
	}
	return spec
}

// wsReceive reads the frame a step waits for. A bare `receive:` — no keys at
// all — is the shorthand for "the next frame, whatever it is".
func (w *walker) wsReceive(n ast.Node) *ir.WSReceive {
	rec := &ir.WSReceive{}
	if _, null := n.(*ast.NullNode); null {
		return rec
	}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "receive is a mapping with match and timeout, or empty for the next frame")
		return rec
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "match":
			rec.Match = w.matchConditions(e.Value)
		case "timeout":
			rec.Timeout = w.duration(e.Value, "timeout")
		default:
			w.errAt(keyNode, "unknown receive key %q", key)
		}
	}
	return rec
}

// matchConditions reads the frame filter, written either as one expression or
// as a list of them. One condition is the overwhelming case, and a `match:`
// should not have to be a list to say so.
func (w *walker) matchConditions(n ast.Node) []ir.Assertion {
	if _, seq := n.(*ast.SequenceNode); seq {
		return w.assertions(n)
	}
	expr, ok := w.str(n, "match")
	if !ok {
		return nil
	}
	a, err := parseAssertion(expr)
	if err != nil {
		w.errAt(n, "%v", err)
		return nil
	}
	return []ir.Assertion{a}
}

func (w *walker) poll(n ast.Node) *ir.PollSpec {
	spec := &ir.PollSpec{}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "poll is a mapping with call, until, interval, and a timeout or max_attempts bound")
		return spec
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "call":
			spec.Call = *w.callShorthand(e.Value)
		case "headers":
			spec.Call.Headers = w.strMap(e.Value, "headers")
		case "query":
			spec.Call.Query = w.strMap(e.Value, "query")
		case "body":
			spec.Call.Body = w.bodyJSON(e.Value, "body")
		case "until":
			spec.Until = w.assertions(e.Value)
		case "interval":
			spec.Interval = w.duration(e.Value, "interval")
		case "timeout":
			spec.Timeout = w.duration(e.Value, "timeout")
		case "max_attempts":
			spec.MaxAttempts, _ = w.intVal(e.Value, "max_attempts")
		default:
			w.errAt(keyNode, "unknown poll key %q", key)
		}
	}
	return spec
}

func (w *walker) retry(n ast.Node) *ir.RetryPolicy {
	pol := &ir.RetryPolicy{}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "retry is a mapping with on_status, backoff, max_attempts")
		return pol
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "on_status":
			if seq, ok := w.seq(e.Value, "on_status"); ok {
				for _, item := range seq.Values {
					if code, ok := w.intVal(item, "on_status entry"); ok {
						pol.OnStatus = append(pol.OnStatus, code)
					}
				}
			}
		case "backoff":
			if s, ok := w.str(e.Value, "backoff"); ok {
				pol.Backoff = ir.BackoffStrategy(s)
			}
		case "max_attempts":
			pol.MaxAttempts, _ = w.intVal(e.Value, "max_attempts")
		case "base_delay":
			pol.BaseDelay = w.duration(e.Value, "base_delay")
		default:
			w.errAt(keyNode, "unknown retry key %q", key)
		}
	}
	return pol
}

// flattenAuth pushes a flow-level default onto every request-making step that
// did not declare its own, then drops explicit opt-outs — so the IR the
// executor sees carries auth per step and needs no flow context to resolve it.
// The Python surface flattens identically (sdk-python/src/flowbench/flow.py);
// the conformance suite holds the two spellings to the same IR.
func flattenAuth(flow *ir.Flow, flowAuth *ir.AuthSpec) {
	for i := range flow.Steps {
		st := &flow.Steps[i]
		if st.Auth == nil && flowAuth != nil && st.MakesRequest() {
			st.Auth = flowAuth
		}
		if st.Auth != nil && st.Auth.Scheme == ir.AuthNone {
			st.Auth = nil
		}
	}
}

func (w *walker) auth(n ast.Node) *ir.AuthSpec {
	spec := &ir.AuthSpec{}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "auth is a mapping with a scheme and that scheme's fields")
		return spec
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "scheme":
			if s, ok := w.str(e.Value, "scheme"); ok {
				spec.Scheme = ir.AuthScheme(s)
			}
		case "token":
			spec.Token, _ = w.str(e.Value, "token")
		case "username":
			spec.Username, _ = w.str(e.Value, "username")
		case "password":
			spec.Password, _ = w.str(e.Value, "password")
		case "name":
			spec.Name, _ = w.str(e.Value, "name")
		case "value":
			spec.Value, _ = w.str(e.Value, "value")
		case "in":
			if s, ok := w.str(e.Value, "in"); ok {
				spec.In = ir.CredentialIn(s)
			}
		case "token_url":
			spec.TokenURL, _ = w.str(e.Value, "token_url")
		case "client_id":
			spec.ClientID, _ = w.str(e.Value, "client_id")
		case "client_secret":
			spec.ClientSecret, _ = w.str(e.Value, "client_secret")
		case "scopes":
			if seq, ok := w.seq(e.Value, "scopes"); ok {
				for _, item := range seq.Values {
					if s, ok := w.str(item, "scope"); ok {
						spec.Scopes = append(spec.Scopes, s)
					}
				}
			}
		case "secret":
			spec.Secret, _ = w.str(e.Value, "secret")
		case "algorithm":
			spec.Algorithm, _ = w.str(e.Value, "algorithm")
		case "encoding":
			spec.Encoding, _ = w.str(e.Value, "encoding")
		case "header":
			spec.Header, _ = w.str(e.Value, "header")
		case "key_id":
			spec.KeyID, _ = w.str(e.Value, "key_id")
		case "key_id_header":
			spec.KeyIDHeader, _ = w.str(e.Value, "key_id_header")
		case "timestamp_header":
			spec.TimestampHeader, _ = w.str(e.Value, "timestamp_header")
		case "sign":
			spec.Sign, _ = w.str(e.Value, "sign")
		default:
			w.errAt(keyNode, "unknown auth key %q", key)
		}
	}
	if spec.Scheme == "" {
		w.errAt(n, "auth needs a scheme")
	}
	return spec
}

func (w *walker) throttle(n ast.Node) *ir.ThrottleSpec {
	spec := &ir.ThrottleSpec{}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "throttle is a mapping with statuses, as_error")
		return spec
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "statuses":
			if seq, ok := w.seq(e.Value, "statuses"); ok {
				for _, item := range seq.Values {
					if code, ok := w.intVal(item, "statuses entry"); ok {
						spec.Statuses = append(spec.Statuses, code)
					}
				}
			}
		case "as_error":
			if b, ok := w.boolVal(e.Value, "as_error"); ok {
				spec.AsError = &b
			}
		default:
			w.errAt(keyNode, "unknown throttle key %q", key)
		}
	}
	return spec
}

func (w *walker) extractions(n ast.Node) []ir.Extraction {
	var exs []ir.Extraction
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "extract is a mapping of variable name to JSONPath")
		return nil
	}
	for _, e := range entries {
		name, _ := w.key(e)
		path, ok := w.str(e.Value, "extraction path")
		if !ok {
			continue
		}
		if !strings.HasPrefix(path, "$") {
			w.errAt(e.Value, `extraction path for %q must be a JSONPath starting with "$"`, name)
			continue
		}
		exs = append(exs, ir.Extraction{Var: name, Path: path})
	}
	return exs
}

func (w *walker) assertions(n ast.Node) []ir.Assertion {
	var as []ir.Assertion
	seq, ok := w.seq(n, "assert")
	if !ok {
		return nil
	}
	for _, item := range seq.Values {
		expr, ok := w.str(item, "assertion")
		if !ok {
			continue
		}
		a, err := parseAssertion(expr)
		if err != nil {
			w.errAt(item, "%v", err)
			continue
		}
		as = append(as, a)
	}
	return as
}

func (w *walker) profile(n ast.Node) ir.Profile {
	p := ir.Profile{}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "profile is a mapping with mode, vus, thresholds, ...")
		return p
	}
	var holdNodes []ast.Node
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "mode":
			if s, ok := w.str(e.Value, "mode"); ok {
				p.Mode = ir.Mode(s)
			}
		case "vus":
			w.vus(e.Value, &p, &holdNodes)
		case "hold":
			p.Hold = w.duration(e.Value, "hold")
			holdNodes = append(holdNodes, keyNode)
		case "iterations":
			p.Iterations, _ = w.intVal(e.Value, "iterations")
		case "arrival_cap":
			p.ArrivalCap, _ = w.str(e.Value, "arrival_cap")
		case "thresholds":
			if seq, ok := w.seq(e.Value, "thresholds"); ok {
				for _, item := range seq.Values {
					if s, ok := w.str(item, "threshold"); ok {
						p.Thresholds = append(p.Thresholds, s)
					}
				}
			}
		default:
			w.errAt(keyNode, "unknown profile key %q", key)
		}
	}
	if len(holdNodes) > 1 {
		w.errAt(holdNodes[len(holdNodes)-1], "hold is set more than once (inside vus and at the profile level)")
	}
	return p
}

// vus accepts a plain VU count or the mapping form { ramp:, hold: }.
func (w *walker) vus(n ast.Node, p *ir.Profile, holdNodes *[]ast.Node) {
	if count, ok := n.(*ast.IntegerNode); ok {
		p.VUs, _ = w.intVal(count, "vus")
		return
	}
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, `vus is a number or a mapping like { ramp: "0 -> 500 over 5m", hold: 10m }`)
		return
	}
	for _, e := range entries {
		key, keyNode := w.key(e)
		switch key {
		case "ramp":
			p.Ramp, _ = w.str(e.Value, "ramp")
		case "hold":
			p.Hold = w.duration(e.Value, "hold")
			*holdNodes = append(*holdNodes, keyNode)
		default:
			w.errAt(keyNode, "unknown vus key %q", key)
		}
	}
}

func mapEntries(n ast.Node) ([]*ast.MappingValueNode, bool) {
	switch m := n.(type) {
	case *ast.MappingNode:
		return m.Values, true
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{m}, true
	}
	return nil, false
}

func (w *walker) key(e *ast.MappingValueNode) (string, ast.Node) {
	if s, ok := e.Key.(*ast.StringNode); ok {
		return s.Value, e.Key
	}
	w.errAt(e.Key, "mapping keys must be plain strings")
	return "", e.Key
}

func (w *walker) str(n ast.Node, what string) (string, bool) {
	if s, ok := n.(*ast.StringNode); ok {
		return s.Value, true
	}
	w.errAt(n, "%s must be a string", what)
	return "", false
}

// text reads a string that may be written as a YAML block scalar (`|` or `>`),
// which is how anything multi-line — a GraphQL document, say — is written.
// goccy models those as a LiteralNode wrapping the string rather than as a
// StringNode, so str alone would reject them.
func (w *walker) text(n ast.Node, what string) (string, bool) {
	if lit, ok := n.(*ast.LiteralNode); ok {
		if lit.Value == nil {
			w.errAt(n, "%s is empty", what)
			return "", false
		}
		return lit.Value.Value, true
	}
	return w.str(n, what)
}

func (w *walker) seq(n ast.Node, what string) (*ast.SequenceNode, bool) {
	if s, ok := n.(*ast.SequenceNode); ok {
		return s, true
	}
	w.errAt(n, "%s must be a list", what)
	return nil, false
}

func (w *walker) boolVal(n ast.Node, what string) (bool, bool) {
	if b, ok := n.(*ast.BoolNode); ok {
		return b.Value, true
	}
	w.errAt(n, "%s must be a boolean", what)
	return false, false
}

func (w *walker) intVal(n ast.Node, what string) (int, bool) {
	in, ok := n.(*ast.IntegerNode)
	if !ok {
		w.errAt(n, "%s must be an integer", what)
		return 0, false
	}
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	switch v := in.Value.(type) {
	case int:
		return v, true
	case int64:
		if v > int64(maxInt) || v < int64(minInt) {
			break
		}
		return int(v), true
	case uint64:
		if v > uint64(maxInt) {
			break
		}
		return int(v), true
	}
	w.errAt(n, "%s is out of range", what)
	return 0, false
}

func (w *walker) duration(n ast.Node, what string) ir.Duration {
	s, ok := w.str(n, what)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		w.errAt(n, `%s %q is not a duration (use "500ms", "30s", "10m")`, what, s)
		return 0
	}
	return ir.Duration(d)
}

// strMap reads a mapping of string keys to scalars, keeping source text
// (so `X-Retry: 3` stays "3").
func (w *walker) strMap(n ast.Node, what string) map[string]string {
	entries, ok := mapEntries(n)
	if !ok {
		w.errAt(n, "%s must be a mapping", what)
		return nil
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		key, _ := w.key(e)
		switch v := e.Value.(type) {
		case *ast.StringNode:
			m[key] = v.Value
		case *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode:
			m[key] = e.Value.GetToken().Value
		default:
			w.errAt(e.Value, "%s value for %q must be a scalar", what, key)
		}
	}
	return m
}

func (w *walker) bodyJSON(n ast.Node, what string) json.RawMessage {
	var v any
	if err := yaml.NodeToValue(n, &v); err != nil {
		w.errAt(n, "%s is not decodable: %v", what, err)
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		w.errAt(n, "%s does not convert to JSON: %v", what, err)
		return nil
	}
	return b
}

func (w *walker) pos(n ast.Node) *ir.Pos {
	if tk := n.GetToken(); tk != nil && tk.Position != nil {
		return &ir.Pos{File: w.file, Line: tk.Position.Line, Col: tk.Position.Column}
	}
	return &ir.Pos{File: w.file}
}

func (w *walker) errAt(n ast.Node, format string, args ...any) {
	w.errs = append(w.errs, fmt.Errorf("%s: %s", w.pos(n), fmt.Sprintf(format, args...)))
}
