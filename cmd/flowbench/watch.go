package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/data"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
	"github.com/blackprince001/flowbench/internal/report"
	"github.com/blackprince001/flowbench/internal/server"
	"github.com/blackprince001/flowbench/internal/store"
	"github.com/blackprince001/flowbench/internal/target"
)

// liveRun holds the most recent progress snapshot for the live view, updated off
// the executor's Progress callback and read by the HTTP handlers.
type liveRun struct {
	mu       sync.Mutex
	prog     executor.Progress
	scenario string
	mode     string
	target   string
}

func (l *liveRun) update(p executor.Progress) {
	l.mu.Lock()
	l.prog = p
	l.mu.Unlock()
}

func (l *liveRun) stat() report.LiveStat {
	l.mu.Lock()
	p := l.prog
	l.mu.Unlock()
	return statOf(p)
}

// statOf derives the shown figures from a raw snapshot. Rates are over completed
// flow-runs so a live rate reads the same way the run's final one will.
func statOf(p executor.Progress) report.LiveStat {
	rps := 0.0
	if s := p.At.Seconds(); s > 0 {
		rps = float64(p.Completed) / s
	}
	er, tr := 0.0, 0.0
	if p.Completed > 0 {
		er = float64(p.Failed) / float64(p.Completed)
		tr = float64(p.Throttled) / float64(p.Completed)
	}
	return report.LiveStat{
		Elapsed:      p.At.Round(time.Second).String(),
		VUs:          p.ActiveVUs,
		PeakVUs:      p.PeakVUs,
		Completed:    p.Completed,
		RPS:          fmt.Sprintf("%.0f/s", rps),
		ErrorRate:    fmt.Sprintf("%.2f%%", er*100),
		ThrottleRate: fmt.Sprintf("%.2f%%", tr*100),
	}
}

// liveServer serves an in-progress run and, once it finishes, hands off to the
// read-only results server over the stored run. Its only write path is abort,
// which cancels the run's context (PRD 10.7).
type liveServer struct {
	live   *liveRun
	cancel context.CancelFunc

	done      atomic.Bool
	completed http.Handler // the read-only server over the store, set on completion
	runURL    string       // the completed run's dashboard, the redirect target
}

func newLiveServer(l *liveRun, cancel context.CancelFunc) *liveServer {
	return &liveServer{live: l, cancel: cancel}
}

func (ls *liveServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/live/stream":
		ls.stream(w, r)
		return
	case strings.HasPrefix(r.URL.Path, report.AssetPrefix):
		// The live page wears the same stylesheet, so it needs the same fonts —
		// from the first tick, not only once the read-only server takes over.
		report.ServeAssets().ServeHTTP(w, r)
		return
	case r.URL.Path == "/abort" && r.Method == http.MethodPost:
		ls.cancel() // the server's one write path: cancel the run's context
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, "aborting")
		return
	}

	// Once the run is stored, the browser walks into its full views.
	if ls.done.Load() && ls.completed != nil {
		if r.URL.Path == "/" {
			http.Redirect(w, r, ls.runURL, http.StatusFound)
			return
		}
		ls.completed.ServeHTTP(w, r)
		return
	}

	if r.URL.Path == "/" && r.Method == http.MethodGet {
		ls.renderLive(w)
		return
	}
	http.NotFound(w, r)
}

func (ls *liveServer) renderLive(w http.ResponseWriter) {
	ls.live.mu.Lock()
	scenario, mode, tgt := ls.live.scenario, ls.live.mode, ls.live.target
	ls.live.mu.Unlock()

	page := report.LivePage{
		Shell:     report.Shell{Title: "watching " + scenario},
		Scenario:  scenario,
		Mode:      mode,
		Target:    tgt,
		Stat:      ls.live.stat(),
		StreamURL: "/live/stream",
		AbortURL:  "/abort",
	}
	var buf bytes.Buffer
	if err := report.RenderLive(&buf, page); err != nil {
		http.Error(w, "flowbench: render live: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// stream is the server-sent event source the live page subscribes to: one
// snapshot per tick until the run ends, then a terminating `done` event carrying
// the stored run's URL so the client follows it in.
func (ls *liveServer) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")

	done := func() {
		fmt.Fprintf(w, "event: done\ndata: %s\n\n", ls.runURL)
		fl.Flush()
	}
	if ls.done.Load() {
		done()
		return
	}
	send := func() {
		if b, err := json.Marshal(ls.live.stat()); err == nil {
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
	send() // an immediate first frame so the page fills before the first tick

	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			if ls.done.Load() {
				done()
				return
			}
			send()
		}
	}
}

// complete points the server at the stored run once the run is over, so the
// browser walks straight into its dashboard, flame graph and traces.
func (ls *liveServer) complete(runDir string) error {
	st, err := store.Open(filepath.Dir(runDir))
	if err != nil {
		return err
	}
	ws := store.WorkspaceOf(st)
	p := ws.Projects()[0]
	ls.runURL = "/p/" + p.Slug + "/runs/" + filepath.Base(runDir) + "/dashboard"
	ls.completed = server.New(ws)
	ls.done.Store(true)
	return nil
}

// runLive executes a load run while serving a live view of it, abortable from the
// browser — the results server's one write path (PRD 10.7). When the run ends it
// flushes the artifacts and keeps serving the stored run until the operator quits
// (a second Ctrl-C), so watching flows straight into inspecting.
func runLive(stdout, stderr io.Writer, sc *ir.Scenario, tgt *target.Target, pools map[string]*data.Pool, storeRoot, scenarioPath, addr string, sched *planner.Schedule, thresholds []collector.Threshold) int {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: bind %s: %v\n", addr, err)
		return exitPreRun
	}
	defer ln.Close()
	if !isLoopback(ln.Addr()) {
		fmt.Fprintf(stderr, "flowbench: warning: %s is not loopback — anyone who can reach it can abort the run\n", ln.Addr())
	}

	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, os.Interrupt)
	defer signal.Stop(sigc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lr := &liveRun{scenario: sc.Name, mode: string(sc.Profile.Mode), target: tgt.Config().Name}
	ls := newLiveServer(lr, cancel)

	srv := &http.Server{Handler: ls, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	fmt.Fprintf(stdout, "watching %q live at http://%s  (Ctrl-C to abort)\n", sc.Name, ln.Addr())

	startedAt := time.Now()
	stopAgent := startAgentPoll(tgt.AgentAddr())
	resc := make(chan *executor.Result, 1)
	go func() {
		res, _ := executor.Run(ctx, executor.Options{
			Schedule: sched,
			Flows:    sc.Flows,
			Pools:    pools,
			BaseURL:  tgt.BaseURL(),
			Allow:    tgt.Allows,
			Progress: lr.update,
		})
		resc <- res
	}()

	// The run ends on its own, on a browser abort (which cancels ctx), or on the
	// first Ctrl-C.
	var res *executor.Result
	select {
	case res = <-resc:
	case <-sigc:
		cancel()
		res = <-resc
	}
	ended := ctx.Err() != nil // cut short rather than run to its scheduled end
	agentSeries := stopAgent()

	printRunSummary(stdout, res)
	outcomes := collector.Evaluate(thresholds, res)
	if sc.Profile.Mode == ir.ModeSoak {
		outcomes = append(outcomes, collector.EvaluateTrends(res)...)
	}
	breached := printOutcomes(stdout, outcomes)

	if dir, err := saveRun(storeRoot, scenarioPath, sc, tgt, startedAt, res, outcomes, agentSeries); err != nil {
		fmt.Fprintf(stderr, "flowbench: could not save run: %v\n", err)
		ls.done.Store(true) // let the live page settle even with nothing stored to open
	} else {
		fmt.Fprintf(stdout, "run saved to %s\n", dir)
		if err := ls.complete(dir); err != nil {
			fmt.Fprintf(stderr, "flowbench: %v\n", err)
			ls.done.Store(true)
		} else {
			fmt.Fprintf(stdout, "  browse the run at http://%s%s  (Ctrl-C to stop)\n", ln.Addr(), ls.runURL)
		}
	}

	<-sigc // keep serving the finished run until the operator quits
	shutdown, stopShut := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopShut()
	srv.Shutdown(shutdown)
	fmt.Fprintln(stdout, "stopped")

	if ended || breached {
		return exitFail
	}
	return exitOK
}
