package parser_test

import (
	"strings"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
)

// TestParsesWSSessionFixture reads the checked-in fixture, so the shape the
// docs show is the shape the parser accepts.
func TestParsesWSSessionFixture(t *testing.T) {
	res, err := parser.ParseFlowFile("../../tests/flows/ws/session.flow.yaml", nil)
	if err != nil {
		t.Fatalf("fixture should parse, got:\n%v", err)
	}
	flow := res.Scenario.Flows[0]
	if len(flow.Steps) != 3 {
		t.Fatalf("want 3 steps, got %d", len(flow.Steps))
	}

	connect, subscribe, tick := flow.Steps[0], flow.Steps[1], flow.Steps[2]
	if connect.Type != ir.StepWS || connect.WS == nil {
		t.Fatalf("step 0 = %q with ws=%v", connect.Type, connect.WS)
	}
	if !connect.WS.Opens() || connect.WS.URL != "/feed" {
		t.Errorf("step 0 should open the session at /feed, got %+v", connect.WS)
	}
	if len(connect.WS.Subprotocols) != 1 || connect.WS.Subprotocols[0] != "flowbench.v1" {
		t.Errorf("subprotocols = %v", connect.WS.Subprotocols)
	}

	// A later step joins the session rather than opening one.
	if subscribe.WS.Opens() {
		t.Errorf("step 1 should join the open session, got url %q", subscribe.WS.URL)
	}
	if string(subscribe.WS.Send) != `{"op":"subscribe","symbol":"FB-001"}` {
		t.Errorf("send frame = %s", subscribe.WS.Send)
	}
	if got := time.Duration(subscribe.WS.Receive.Timeout); got != 2*time.Second {
		t.Errorf("receive timeout = %s, want 2s", got)
	}

	// One condition written as a bare expression is still a list of one.
	if len(subscribe.WS.Receive.Match) != 1 {
		t.Fatalf("match = %+v, want one condition", subscribe.WS.Receive.Match)
	}
	m := subscribe.WS.Receive.Match[0]
	if m.Source != ir.AssertBody || m.Key != "$.type" || m.Op != ir.OpEq {
		t.Errorf("match condition = %+v", m)
	}
	if tick.WS.Receive == nil || len(tick.WS.Receive.Match) != 1 {
		t.Errorf("tick match = %+v", tick.WS.Receive)
	}
}

// A `match:` is one condition far more often than several, and should not have
// to be written as a list to say so — but a list still means all of them.
func TestWSMatchAcceptsOneConditionOrMany(t *testing.T) {
	res, err := parser.ParseFlow([]byte(`
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: many
    ws:
      receive:
        match:
          - $.type == "tick"
          - $.symbol == "FB-001"
`), "many.flow.yaml", nil)
	if err != nil {
		t.Fatalf("should parse, got:\n%v", err)
	}
	match := res.Scenario.Flows[0].Steps[1].WS.Receive.Match
	if len(match) != 2 || match[1].Key != "$.symbol" {
		t.Errorf("match = %+v, want both conditions", match)
	}
}

// A bare `receive:` is the shorthand for "the next frame, whatever it is".
func TestWSBareReceiveTakesTheNextFrame(t *testing.T) {
	res, err := parser.ParseFlow([]byte(`
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: whatever
    ws:
      receive:
`), "bare.flow.yaml", nil)
	if err != nil {
		t.Fatalf("should parse, got:\n%v", err)
	}
	rec := res.Scenario.Flows[0].Steps[1].WS.Receive
	if rec == nil {
		t.Fatal("bare receive should still be a receive")
	}
	if len(rec.Match) != 0 || rec.Timeout != 0 {
		t.Errorf("bare receive = %+v, want an unfiltered, unbounded-by-the-flow wait", rec)
	}
}

func TestWSErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "a session nothing opens",
			src: `
flow: f
steps:
  - id: s
    ws:
      send: { op: ping }
`,
			want: "which no earlier step opens",
		},
		{
			name: "a named session nothing opens",
			src: `
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: s
    ws:
      session: control
      send: { op: ping }
`,
			want: `ws session "control", which no earlier step opens`,
		},
		{
			name: "opening the same session twice",
			src: `
flow: f
steps:
  - id: one
    ws: { url: /feed }
  - id: two
    ws: { url: /feed }
`,
			want: "already open in this flow",
		},
		{
			name: "a step that does nothing",
			src: `
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: s
    ws: {}
`,
			want: "must open a session (url), send a frame, or receive one",
		},
		{
			name: "asserting on a status a frame does not have",
			src: `
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: s
    ws:
      receive:
    assert:
      - status == 200
`,
			want: "a WebSocket frame has no status",
		},
		{
			name: "asserting on a frame the step never receives",
			src: `
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: s
    ws:
      send: { op: ping }
    assert:
      - $.op == "pong"
`,
			want: "receives nothing, so there is no frame to assert on",
		},
		{
			name: "handshake headers on a step with no handshake",
			src: `
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: s
    ws:
      send: { op: ping }
    headers: { X-Trace: abc }
`,
			want: "belong to the ws step that opens the session",
		},
		{
			name: "call-shaped body has nowhere to go",
			src: `
flow: f
steps:
  - id: s
    ws: { url: /feed }
    body: { a: 1 }
`,
			want: "carries its payload in the frame it sends",
		},
		{
			name: "retry does not apply to a session",
			src: `
flow: f
steps:
  - id: s
    ws: { url: /feed }
    retry:
      on_status: [429]
      backoff: fixed
      max_attempts: 2
`,
			want: "retry policies apply to call and graphql steps only",
		},
		{
			name: "auth on a step with no request to sign",
			src: `
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: s
    ws:
      send: { op: ping }
    auth:
      scheme: bearer
      token: "{{ env.T }}"
`,
			want: "auth applies to steps that make a request",
		},
		{
			name: "unknown ws key",
			src: `
flow: f
steps:
  - id: s
    ws:
      url: /feed
      protocol: mqtt
`,
			want: `unknown ws key "protocol"`,
		},
		{
			name: "a sent frame referencing nothing upstream",
			src: `
flow: f
steps:
  - id: open
    ws: { url: /feed }
  - id: s
    ws:
      send: { id: "{{ nothing_extracts_this }}" }
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

// A flow-level auth default reaches the handshake and stops there: a step
// working on an already-open session has no request left to decorate.
func TestWSAuthFlattensOntoTheHandshakeOnly(t *testing.T) {
	res, err := parser.ParseFlow([]byte(`
flow: f
auth:
  scheme: bearer
  token: "{{ env.FEED_TOKEN }}"
steps:
  - id: open
    ws: { url: /feed }
  - id: ping
    ws:
      send: { op: ping }
`), "auth.flow.yaml", nil)
	if err != nil {
		t.Fatalf("should parse, got:\n%v", err)
	}
	steps := res.Scenario.Flows[0].Steps
	if steps[0].Auth == nil || steps[0].Auth.Scheme != ir.AuthBearer {
		t.Errorf("the opening step should inherit the flow's auth, got %+v", steps[0].Auth)
	}
	if steps[1].Auth != nil {
		t.Errorf("a step on an open session should carry no auth, got %+v", steps[1].Auth)
	}
}
