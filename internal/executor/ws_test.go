package executor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/auth"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
	"github.com/blackprince001/flowbench/internal/span"
	"github.com/blackprince001/flowbench/internal/target"
)

// wsStub is a small duplex service. What matters about it is that it talks
// when it feels like talking: /feed answers a subscribe with a heartbeat
// *before* the ack, so a step that took "the next frame" would get the wrong
// one, and the tick that follows is there for the next step to pick up.
func wsStub(t *testing.T, closed chan<- string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"flowbench.v1"},
		})
		if err != nil {
			return
		}
		defer func() {
			c.CloseNow()
			if closed != nil {
				closed <- "/feed"
			}
		}()

		for {
			_, data, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var msg struct {
				Op     string `json:"op"`
				Symbol string `json:"symbol"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Errorf("frame is not JSON: %s", data)
				return
			}
			switch msg.Op {
			case "subscribe":
				send(t, c, `{"type":"heartbeat","at":1}`)
				send(t, c, `{"type":"ack","id":"sub_1","status":"ok"}`)
				send(t, c, `{"type":"tick","symbol":"`+msg.Symbol+`","price":42}`)
			case "ping":
				send(t, c, `{"op":"pong"}`)
			}
		}
	})

	// A feed that never stops talking, so a receive's deadline lands while a
	// message is being read rather than on an idle connection.
	mux.HandleFunc("/chatty", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			if err := c.Write(r.Context(), websocket.MessageText,
				[]byte(`{"type":"heartbeat"}`)); err != nil {
				return
			}
		}
	})

	// Accepts the upgrade, then sheds load in-band.
	mux.HandleFunc("/busy", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.Close(websocket.StatusTryAgainLater, "at capacity")
	})

	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.Close(websocket.StatusInternalError, "resolver panicked")
	})

	// Refusals that never upgrade at all: ordinary HTTP responses.
	mux.HandleFunc("/limited", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		http.Error(w, "too many connections", http.StatusTooManyRequests)
	})
	mux.HandleFunc("/private", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer feed-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		send(t, c, `{"type":"welcome"}`)
		c.Read(context.Background())
		c.CloseNow()
	})

	return mux
}

func send(t *testing.T, c *websocket.Conn, frame string) {
	t.Helper()
	if err := c.Write(context.Background(), websocket.MessageText, []byte(frame)); err != nil {
		t.Errorf("stub write %s: %v", frame, err)
	}
}

func runWSFixture(t *testing.T, fixture, baseURL string) (*executor.Iteration, *executor.Scope) {
	t.Helper()

	path := "../../tests/flows/ws/" + fixture
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

	scope := executor.NewScope("", nil)
	runner := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: tgt.BaseURL(),
		Mode:    res.Scenario.Profile.Mode,
		Allow:   tgt.Allows,
	}
	it, err := runner.RunFlow(context.Background(), res.Scenario.Flows[0], scope)
	if err != nil {
		t.Fatalf("running %s: %v", path, err)
	}
	return it, scope
}

// TestWSSessionAcrossSteps is the issue #27 acceptance: a flow opens a
// session, exchanges matched messages across two steps, and asserts on a
// received frame.
func TestWSSessionAcrossSteps(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	it, scope := runWSFixture(t, "session.flow.yaml", srv.URL)
	if len(it.Failures) > 0 {
		t.Fatalf("session flow should pass, got %v", it.Failures)
	}
	if it.Outcome != span.OutcomeOK {
		t.Errorf("outcome = %s, want ok", it.Outcome)
	}
	// Extraction reads the matched frame, and the value carries into the next
	// step exactly as an HTTP extraction would.
	if got, _ := scope.Lookup("subscription"); got != "sub_1" {
		t.Errorf("subscription = %v, want sub_1", got)
	}
}

// TestWSMatchSkipsUnrelatedFrames is the reason a receive names what it wants.
// The stub sends a heartbeat before the ack; a step that took the next frame
// would extract nothing from it.
func TestWSMatchSkipsUnrelatedFrames(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	it, _ := runWSFixture(t, "session.flow.yaml", srv.URL)
	finalize(it)
	payload := receivedFrame(t, it.Spans[1])
	if !strings.Contains(payload, `"type":"ack"`) {
		t.Errorf("subscribe matched %s, want the ack (the heartbeat should have been skipped)", payload)
	}
}

// TestWSSessionIsIterationScoped: the flow never closes anything, and the
// server still sees the connection go away when the iteration ends.
func TestWSSessionIsIterationScoped(t *testing.T) {
	closed := make(chan string, 1)
	srv := httptest.NewServer(wsStub(t, closed))
	defer srv.Close()

	runWSFixture(t, "session.flow.yaml", srv.URL)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the session outlived its iteration: the server never saw it close")
	}
}

// TestWSSpansMatchHTTP: the handshake is an HTTP request, so it carries the
// same phase children a call step's does, and the frames get their own spans.
func TestWSSpansMatchHTTP(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	it, _ := runWSFixture(t, "session.flow.yaml", srv.URL)
	if len(it.Spans) != 3 {
		t.Fatalf("want a span per step, got %d", len(it.Spans))
	}

	finalize(it)

	open := it.Spans[0]
	if names := childNames(open); len(names) != 1 || !names["ws_open"] {
		t.Fatalf("open step children = %v, want [ws_open]", names)
	}
	handshake := open.Children[0]
	var httpCall *span.Span
	for _, c := range handshake.Children {
		if c.Name == "http_call" {
			httpCall = c
		}
	}
	if httpCall == nil {
		t.Fatalf("ws_open has no http_call child; children = %v", childNames(handshake))
	}
	phases := childNames(httpCall)
	for _, want := range []string{"connect", "ttfb"} {
		if !phases[want] {
			t.Errorf("handshake phases = %v, missing %q", phases, want)
		}
	}
	if handshake.Payload == nil || handshake.Payload.Status != http.StatusSwitchingProtocols {
		t.Errorf("ws_open should record the handshake's 101, got %+v", handshake.Payload)
	}

	subscribe := childNames(it.Spans[1])
	for _, want := range []string{"ws_send", "ws_receive"} {
		if !subscribe[want] {
			t.Errorf("subscribe children = %v, missing %q", subscribe, want)
		}
	}
}

// TestWSReceiveTimeoutNamesWhatItSkipped: a receive that never matches is the
// confusing failure, so the detail says what was wanted and what arrived.
func TestWSReceiveTimeoutNamesWhatItSkipped(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	it, _ := runWSFixture(t, "mismatch.flow.yaml", srv.URL)
	if len(it.Failures) != 1 {
		t.Fatalf("want one recorded failure, got %v", it.Failures)
	}
	detail := it.Failures[0].Detail
	for _, want := range []string{`$.type == "settlement"`, "300ms", "heartbeat", "skipped"} {
		if !strings.Contains(detail, want) {
			t.Errorf("failure detail %q does not mention %q", detail, want)
		}
	}
}

// A timeout against a chatty feed is still a timeout. The deadline lands
// mid-message, which tears the connection down, and reporting that as "use of
// closed network connection" would blame the target for the flow's mismatch.
func TestWSTimeoutOnAChattyFeedStillReadsAsATimeout(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	waiting := wsReceive("await_settlement", "$.type", "settlement")
	waiting.WS.Receive.Timeout = ir.Duration(300 * time.Millisecond)

	it := runWSSteps(t, srv.URL, ir.ModeIntegration,
		wsOpen("connect", "/chatty"),
		waiting,
	)
	if len(it.Failures) != 1 {
		t.Fatalf("want one failure, got %v", it.Failures)
	}
	if !strings.Contains(it.Failures[0].Detail, "no frame matching") {
		t.Errorf("failure detail %q should report the timeout, not the torn-down connection",
			it.Failures[0].Detail)
	}
}

// TestWSCloseTryAgainLaterIsThrottled: 1013 is the WebSocket's 429. In load
// mode a throttle is data, so it feeds throttle_rate without failing the run.
func TestWSCloseTryAgainLaterIsThrottled(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	it, _ := runWSFixture(t, "overloaded.flow.yaml", srv.URL)
	if !it.Throttled {
		t.Fatal("close 1013 should have flagged the iteration throttled")
	}
	if len(it.Failures) > 0 {
		t.Errorf("in load mode a throttle is data, not a failure: %v", it.Failures)
	}
	subscribe := it.Spans[1]
	if subscribe.Outcome != span.OutcomeThrottled {
		t.Errorf("step outcome = %s, want throttled", subscribe.Outcome)
	}
}

// The rest of the close codes are failures, and the code is what tells a
// reader whether the server crashed or hung up politely.
func TestWSAbnormalCloseNamesTheCode(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	it := runWSSteps(t, srv.URL, ir.Mode("integration"),
		wsOpen("connect", "/boom"),
		wsExchange("subscribe", `{"op":"subscribe"}`),
	)
	if len(it.Failures) != 1 {
		t.Fatalf("want one failure, got %v", it.Failures)
	}
	detail := it.Failures[0].Detail
	if !strings.Contains(detail, "1011") || !strings.Contains(detail, "internal error") {
		t.Errorf("failure detail %q should name the close code", detail)
	}
	if it.Throttled {
		t.Error("1011 is a failure, not a throttle")
	}
}

// A handshake that never upgrades is an ordinary HTTP response, and a 429
// among them is the same throttle it would be on any other request.
func TestWSHandshakeThrottleAndRefusal(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	throttled := runWSSteps(t, srv.URL, ir.ModeLoad,
		wsOpen("connect", "/limited"),
		wsExchange("subscribe", `{"op":"subscribe"}`))
	if !throttled.Throttled {
		t.Error("a 429 on the handshake should classify as throttled")
	}
	// In load mode a throttle is data and the flow carries on, so the step
	// after it has to explain the session it cannot use rather than crash the
	// iteration on a session that was never opened.
	if len(throttled.Failures) != 1 || !strings.Contains(throttled.Failures[0].Detail, "429") {
		t.Errorf("the step after a refused handshake should record why: %v", throttled.Failures)
	}

	refused := runWSSteps(t, srv.URL, ir.ModeIntegration, wsOpen("connect", "/private"))
	if len(refused.Failures) != 1 || !strings.Contains(refused.Failures[0].Detail, "401") {
		t.Errorf("an unauthenticated handshake should fail naming the status, got %v", refused.Failures)
	}
}

// Credentials ride on the handshake because the handshake is a request: the
// same auth block a call step declares opens the socket.
func TestWSHandshakeCarriesAuth(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	open := wsOpen("connect", "/private")
	open.Auth = &ir.AuthSpec{Scheme: ir.AuthBearer, Token: "feed-token"}
	it := runWSSteps(t, srv.URL, ir.ModeIntegration, open,
		wsReceive("welcome", `$.type`, "welcome"))
	if len(it.Failures) > 0 {
		t.Fatalf("authenticated handshake should pass, got %v", it.Failures)
	}
}

// A session cannot survive a read that timed out — the peer may have been
// mid-frame — so the next step naming it should say what ended it rather than
// fail on a closed socket.
func TestWSStepAfterDeadSessionExplainsItself(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	silent := wsReceive("await_settlement", "$.type", "settlement")
	silent.WS.Receive.Timeout = ir.Duration(200 * time.Millisecond)
	silent.OnFailure = ir.FailureRecord

	it := runWSSteps(t, srv.URL, ir.ModeLoad,
		wsOpen("connect", "/feed"),
		silent,
		wsReceive("later", "$.type", "tick"),
	)
	if len(it.Failures) != 2 {
		t.Fatalf("want the timeout and the step after it, got %v", it.Failures)
	}
	if !strings.Contains(it.Failures[1].Detail, "no longer open") {
		t.Errorf("second failure %q should explain that the session ended", it.Failures[1].Detail)
	}
}

// The allow-list gates a ws step the way it gates a call, and ws:// is the
// same origin as the http:// the target declared.
func TestWSAllowList(t *testing.T) {
	srv := httptest.NewServer(wsStub(t, nil))
	defer srv.Close()

	it := runWSSteps(t, srv.URL, ir.ModeIntegration,
		wsOpen("connect", "ws://evil.example/feed"))
	if len(it.Failures) != 1 || !strings.Contains(it.Failures[0].Detail, "allow-list") {
		t.Fatalf("a ws step outside the allow-list should be refused, got %v", it.Failures)
	}
}

// -- helpers ---------------------------------------------------------------

func runWSSteps(t *testing.T, baseURL string, mode ir.Mode, steps ...ir.Step) *executor.Iteration {
	t.Helper()

	tgt, err := target.New(&ir.TargetConfig{Name: "stub", BaseURLs: []string{baseURL}})
	if err != nil {
		t.Fatalf("building target: %v", err)
	}
	runner := &executor.Runner{
		Session: adapters.NewSession(adapters.SessionOptions{}),
		BaseURL: tgt.BaseURL(),
		Mode:    mode,
		Allow:   tgt.Allows,
		Auth:    auth.NewProvider(auth.Options{Allow: tgt.Allows}),
	}
	flow := ir.Flow{Name: "ws_adhoc", Steps: steps}
	it, err := runner.RunFlow(context.Background(), flow, executor.NewScope("", nil))
	if err != nil {
		t.Fatalf("running ad-hoc ws flow: %v", err)
	}
	return it
}

func wsOpen(id, url string) ir.Step {
	return ir.Step{ID: id, Type: ir.StepWS, WS: &ir.WSSpec{URL: url}}
}

func wsExchange(id, frame string) ir.Step {
	return ir.Step{ID: id, Type: ir.StepWS, WS: &ir.WSSpec{
		Send:    json.RawMessage(frame),
		Receive: &ir.WSReceive{Timeout: ir.Duration(2 * time.Second)},
	}}
}

func wsReceive(id, path, want string) ir.Step {
	return ir.Step{ID: id, Type: ir.StepWS, WS: &ir.WSSpec{
		Receive: &ir.WSReceive{
			Match: []ir.Assertion{{
				Source: ir.AssertBody, Key: path, Op: ir.OpEq,
				Value: json.RawMessage(`"` + want + `"`),
			}},
			Timeout: ir.Duration(2 * time.Second),
		},
	}}
}

// finalize materializes captured payloads the way the pool does for a kept
// trace; RunFlow alone holds them by reference.
func finalize(it *executor.Iteration) {
	for _, sp := range it.Spans {
		span.Finalize(sp, func(b []byte) []byte { return b }, 2048)
	}
}

// receivedFrame is the frame a ws step's span captured as its response.
func receivedFrame(t *testing.T, step *span.Span) string {
	t.Helper()
	if step.Payload == nil {
		t.Fatalf("step %q captured no payload", step.Name)
	}
	return step.Payload.Response
}
