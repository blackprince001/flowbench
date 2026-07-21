package main

import (
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

const (
	rate    = 200
	burst   = 200
	latency = 15 * time.Millisecond
	errRate = 0.005
)

type limiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * rate
	if l.tokens > burst {
		l.tokens = burst
	}
	l.last = now
	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}

func main() {
	start := time.Now()
	lim := &limiter{tokens: burst, last: start}

	// Rate-limited: admits ~200/s, 429s the rest — throttle_rate vs error_rate.
	http.HandleFunc("/checkout", func(w http.ResponseWriter, r *http.Request) {
		if !lim.allow() {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		time.Sleep(latency)
		if rand.Float64() < errRate {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	// Echoes the request body back — so payload capture has something to redact,
	// in both the request and an echoing response.
	http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})

	// Latency climbs with uptime, so a soak run's second half is slower than its
	// first — the creep trend evaluation flags.
	http.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		creep := 5*time.Millisecond + time.Duration(time.Since(start).Seconds()*8)*time.Millisecond
		time.Sleep(creep)
		w.WriteHeader(http.StatusOK)
	})

	log.Println("stub listening on :8080 — /checkout (rate-limited), /login (echo), /slow (creeping)")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
