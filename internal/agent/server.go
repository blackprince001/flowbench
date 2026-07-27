package agent

import (
	"encoding/json"
	"net/http"
)

// Handler serves GET /metrics with the host's current Sample as JSON —
// the agent's whole wire surface (ADR 0016: scrape over HTTP, not a
// persistent push/stream). Every other method or path is a 404.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", handleMetrics)
	return mux
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	s, err := Read()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}
