// A market-feed service over WebSocket, small enough to read in one sitting.
//
// Two behaviours matter, and both are things HTTP never makes you think about.
// The feed talks when it feels like talking — a heartbeat lands before the ack
// a subscribe is waiting for, and keeps landing between ticks — so a step that
// took "the next frame" would get the wrong one. And when it is full it says
// so in band, with RFC 6455's close code 1013, "try again later": the
// WebSocket's 429, arriving on a connection that was already established.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// capacity is how many sessions the feed carries at once. Past it, new
// sessions are closed with 1013 rather than queued or refused at the
// handshake — the interesting case, because the connection opened first.
const capacity = 40

const (
	heartbeatEvery = 200 * time.Millisecond
	tickEvery      = 60 * time.Millisecond
)

var (
	live  atomic.Int64
	subs  atomic.Int64
	shed  atomic.Int64
	price atomic.Int64
)

type message struct {
	Op           string `json:"op"`
	Symbol       string `json:"symbol"`
	Subscription string `json:"subscription"`
}

func main() {
	price.Store(2500)
	http.HandleFunc("/feed", feed)

	log.Println("ws stub listening on :8092 — ws://localhost:8092/feed")
	log.Printf("  heartbeats every %s, ticks every %s once subscribed", heartbeatEvery, tickEvery)
	log.Printf("  %d concurrent sessions; past that it closes with 1013 (try again later)", capacity)
	log.Fatal(http.ListenAndServe(":8092", nil))
}

func feed(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"flowbench.v1"},
	})
	if err != nil {
		return
	}
	defer c.CloseNow()

	if live.Add(1) > capacity {
		live.Add(-1)
		if n := shed.Add(1); n%100 == 1 {
			log.Printf("feed at capacity: %d sessions shed with 1013 so far", n)
		}
		c.Close(websocket.StatusTryAgainLater, "feed at capacity")
		return
	}
	defer live.Add(-1)

	serve(r.Context(), c)
}

// serve is the session's only writer: coder/websocket allows one concurrent
// writer, and heartbeats, ticks and replies all want to be it. A single select
// loop is simpler than a lock.
func serve(ctx context.Context, c *websocket.Conn) {
	incoming := read(ctx, c)

	heartbeats := time.NewTicker(heartbeatEvery)
	defer heartbeats.Stop()
	ticks := time.NewTicker(tickEvery)
	defer ticks.Stop()

	// A greeting nobody asked for, so the very first frame on every session is
	// one a subscribe has to skip.
	if !write(ctx, c, `{"type":"heartbeat","at":%d}`, time.Now().UnixMilli()) {
		return
	}

	var symbol string
	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-incoming:
			if !ok {
				return
			}
			switch msg.Op {
			case "subscribe":
				symbol = msg.Symbol
				id := subs.Add(1)
				if !write(ctx, c, `{"type":"ack","id":"sub_%04d","status":"ok","symbol":%q}`, id, symbol) {
					return
				}
			case "unsubscribe":
				symbol = ""
				if !write(ctx, c, `{"type":"unsubscribed","subscription":%q}`, msg.Subscription) {
					return
				}
			default:
				if !write(ctx, c, `{"type":"error","message":"unknown op %q"}`, msg.Op) {
					return
				}
			}

		case <-heartbeats.C:
			if !write(ctx, c, `{"type":"heartbeat","at":%d}`, time.Now().UnixMilli()) {
				return
			}

		case <-ticks.C:
			if symbol == "" {
				continue // nothing subscribed: silence, not ticks
			}
			next := price.Add(int64(rand.Intn(21) - 10))
			if !write(ctx, c, `{"type":"tick","symbol":%q,"price":%d}`, symbol, next) {
				return
			}
		}
	}
}

// read pumps frames off the connection onto a channel so the write loop can
// select on them alongside its timers.
func read(ctx context.Context, c *websocket.Conn) <-chan message {
	out := make(chan message, 8)
	go func() {
		defer close(out)
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return // the client went away, or the run ended
			}
			var msg message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func write(ctx context.Context, c *websocket.Conn, format string, args ...any) bool {
	frame := fmt.Sprintf(format, args...)
	return c.Write(ctx, websocket.MessageText, []byte(frame)) == nil
}
