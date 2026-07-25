package report

// LivePage is the view of a run in flight: a handful of figures the server-sent
// event stream keeps current. It shows only what the executor exposes live —
// throughput, VUs, and the outcome rates; percentiles and the flame graph appear
// on the dashboard once the run completes and its tiers are on disk.
type LivePage struct {
	Shell
	Scenario  string
	Mode      string
	Target    string
	Stat      LiveStat
	StreamURL string
	AbortURL  string
}

// LiveStat is one live reading. It doubles as the server-sent event payload, so
// its JSON tags are the keys the live.js client reads; the same struct fills the
// server-rendered page, so a value is present before any script runs.
type LiveStat struct {
	Elapsed      string `json:"elapsed"`
	VUs          int    `json:"vus"`
	PeakVUs      int    `json:"peak"`
	Completed    int64  `json:"completed"`
	RPS          string `json:"rps"`
	ErrorRate    string `json:"error"`
	ThrottleRate string `json:"throttle"`
}
