package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/auth"
	"github.com/blackprince001/flowbench/internal/data"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
	"github.com/blackprince001/flowbench/internal/span"
)

const (
	defaultMetricInterval = time.Second
	defaultMaxTraces      = 200   // safety cap on retained sampled-success traces
	maxFailTraces         = 10000 // safety ceiling on retained failure traces
	defaultSampleEvery    = 100   // keep 1 of every N successful/throttled traces
	defaultBodyCap        = 2048  // captured request/response body cap, bytes
)

// Options configures a pool run. BaseURL, Allow, and the schedule's mode mirror
// the single-VU Runner; the pool owns the concurrency and isolation on top.
type Options struct {
	Schedule *planner.Schedule
	Flows    []ir.Flow
	Pools    map[string]*data.Pool
	BaseURL  string
	Allow    func(string) (bool, error)

	// Auth applies steps' declared credentials. One provider serves the whole
	// run, so an OAuth2 token endpoint sees one fetch rather than one per VU.
	Auth *auth.Provider

	// Protos is the run's compiled gRPC schemas, shared by every VU. A run with
	// no grpc steps never touches it, and nil is only a wiring error for a run
	// that has some.
	Protos *adapters.ProtoRegistry

	// Metrics is the self-metric sample interval; 0 uses a default, negative
	// disables sampling.
	Metrics time.Duration

	// Progress, when set, is called about once per metric interval with a live
	// snapshot of the run in flight — the data source for the live view. It is
	// never called after Run returns; nil (the default) disables it.
	Progress func(Progress)

	// Capture policy (ADR 0007). All failures are kept up to an internal safety
	// ceiling; successes and throttled responses are sampled.
	//
	// SampleEvery keeps 1 of every N successful/throttled traces (0 uses a
	// default, 1 keeps all). MaxTraces caps how many sampled successes are kept
	// and MaxFailures how many failures (both 0 use defaults) — safety ceilings
	// against unbounded memory, not policy. MaxBodyBytes truncates captured
	// request/response bodies (0 uses a default, negative captures no bodies).
	// Bodies are always redacted before storage.
	SampleEvery  int
	MaxTraces    int
	MaxFailures  int
	MaxBodyBytes int

	// RequestTimeout bounds a single call. Without it a target that accepts a
	// connection and then says nothing holds a VU for the adapter's default,
	// which is a long time to lose a worker for. Zero uses that default.
	RequestTimeout time.Duration
}

// Result is the raw product of a run: latency samples, retained trace trees,
// the generator's self-metric series, and outcome counts. Aggregation into
// percentiles, thresholds, and storage lives in the collector.
type Result struct {
	Duration   time.Duration
	Iterations int
	Outcomes   map[span.Outcome]int
	Samples    []Sample
	Traces     []*span.Span
	Folded     *span.Folded
	Metrics    []MetricSample
	Aborted    bool
}

// Failed is the count of flow-runs counted as errors. A throttle counted as an
// error (integration/system default) is included; a throttle treated as data
// (load/stress/soak default) is not.
func (r *Result) Failed() int { return r.Outcomes[span.OutcomeFailed] }

// Throttled is the count of flow-runs that hit a throttle, in every mode —
// including those that also failed.
func (r *Result) Throttled() int {
	n := 0
	for _, s := range r.Samples {
		if s.Throttled {
			n++
		}
	}
	return n
}

// ErrorRate is the fraction of flow-runs counted as errors. Throttles excluded
// from error accounting (per mode) do not raise it.
func (r *Result) ErrorRate() float64 {
	if len(r.Samples) == 0 {
		return 0
	}
	return float64(r.Failed()) / float64(len(r.Samples))
}

// ThrottleRate is the fraction of flow-runs that hit a throttle, tracked
// separately from ErrorRate everywhere.
func (r *Result) ThrottleRate() float64 {
	if len(r.Samples) == 0 {
		return 0
	}
	return float64(r.Throttled()) / float64(len(r.Samples))
}

// Progress is a live snapshot of a run in flight, for the live view. Completed
// is the flow-runs finished so far; Failed and Throttled count them the same way
// the final Result does, so a live rate matches the run's own once it ends.
type Progress struct {
	At        time.Duration
	ActiveVUs int
	PeakVUs   int
	Completed int64
	Failed    int64
	Throttled int64
}

// Run drives opts.Schedule to completion with one goroutine per virtual user,
// each isolated in its own session (cookie jar and connection pool), drawing
// its own data rows and running every iteration in a fresh variable scope.
// It returns when the schedule elapses, ctx is cancelled, or an abort_run
// failure trips the kill switch.
func Run(ctx context.Context, opts Options) (*Result, error) {
	if opts.Schedule == nil {
		return nil, errors.New("executor: nil schedule")
	}
	if len(opts.Flows) == 0 {
		return nil, errors.New("executor: no flows to run")
	}
	maxTraces := opts.MaxTraces
	if maxTraces == 0 {
		maxTraces = defaultMaxTraces
	}
	maxFail := opts.MaxFailures
	if maxFail == 0 {
		maxFail = maxFailTraces
	}
	sampleEvery := opts.SampleEvery
	if sampleEvery == 0 {
		sampleEvery = defaultSampleEvery
	}
	bodyCap := opts.MaxBodyBytes
	if bodyCap == 0 {
		bodyCap = defaultBodyCap
	}
	metricInterval := opts.Metrics
	if metricInterval == 0 {
		metricInterval = defaultMetricInterval
	}
	if opts.Auth == nil {
		// One provider for the run, gated by the same allow-list as the calls.
		opts.Auth = auth.NewProvider(auth.Options{Allow: opts.Allow})
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p := &pool{
		opts:         opts,
		sched:        opts.Schedule,
		start:        time.Now(),
		cancel:       cancel,
		folded:       span.NewFolded(),
		sampleEvery:  sampleEvery,
		maxBodyBytes: bodyCap,
	}
	p.successBudget.Store(int64(maxTraces))
	p.failBudget.Store(int64(maxFail))

	// Sample the generator's own resource use for the run's lifetime.
	mctx, mstop := context.WithCancel(context.Background())
	var mwg sync.WaitGroup
	mwg.Go(func() {
		p.metrics = sampleMetrics(mctx, p.start, metricInterval, func() int { return int(p.active.Load()) })
	})

	// Stream live progress to a watcher on the same cadence, if one is attached.
	// It reads the same atomics off the hot path, so it never touches the run.
	if opts.Progress != nil {
		mwg.Go(func() {
			t := time.NewTicker(metricInterval)
			defer t.Stop()
			for {
				select {
				case <-mctx.Done():
					opts.Progress(p.snapshot()) // one final reading at the end
					return
				case <-t.C:
					opts.Progress(p.snapshot())
				}
			}
		})
	}

	if p.sched.Arrival == planner.Open && p.sched.ArrivalCap != nil {
		p.runOpen(ctx)
	} else {
		p.runClosed(ctx)
	}

	mstop()
	mwg.Wait()

	res := &Result{
		Duration:   time.Since(p.start),
		Iterations: p.iterations,
		Outcomes:   Tally(p.samples),
		Samples:    p.samples,
		Traces:     p.traces,
		Folded:     p.folded,
		Metrics:    p.metrics,
		Aborted:    p.aborted.Load(),
	}
	return res, nil
}

type pool struct {
	opts   Options
	sched  *planner.Schedule
	start  time.Time
	cancel context.CancelFunc

	active  atomic.Int32 // iterations currently in flight
	aborted atomic.Bool

	// Live running tallies for the progress snapshot, incremented per flow-run so
	// they track the final Result's own counts.
	completed atomic.Int64
	failed    atomic.Int64
	throttled atomic.Int64

	successBudget atomic.Int64
	failBudget    atomic.Int64
	successSeen   atomic.Int64
	sampleEvery   int
	maxBodyBytes  int

	mu         sync.Mutex
	samples    []Sample
	traces     []*span.Span
	folded     *span.Folded
	iterations int

	metrics []MetricSample // written by the sampler goroutine, read after it joins
}

func (p *pool) peak() int {
	if p.sched.PeakVUs < 1 {
		return 1
	}
	return p.sched.PeakVUs
}

// snapshot is a thread-safe live read of the run's progress, from the atomics
// the hot path maintains — safe to call from another goroutine (the live view)
// while the run is in flight.
func (p *pool) snapshot() Progress {
	return Progress{
		At:        time.Since(p.start),
		ActiveVUs: int(p.active.Load()),
		PeakVUs:   p.peak(),
		Completed: p.completed.Load(),
		Failed:    p.failed.Load(),
		Throttled: p.throttled.Load(),
	}
}

// runClosed is the VU-driven model: a fixed population loops iterations
// back-to-back, ramped up to the peak per the schedule's segments. Since the
// planner's shapes only ever ramp up then hold, the spawner is monotonic.
func (p *pool) runClosed(ctx context.Context) {
	var wg sync.WaitGroup

	if p.sched.Stop == planner.StopOnce {
		for i := 0; i < p.peak(); i++ {
			wg.Add(1)
			go p.vu(ctx, &wg, time.Time{}, true)
		}
		wg.Wait()
		return
	}

	deadline := p.start.Add(time.Duration(p.sched.Duration))
	spawned := 0
	spawn := func(target int) {
		for spawned < target {
			spawned++
			wg.Add(1)
			go p.vu(ctx, &wg, deadline, false)
		}
	}

	spawn(curveAt(p.sched.Segments, 0))
	tick := time.NewTicker(spawnTick(time.Duration(p.sched.Duration), p.peak()))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case now := <-tick.C:
			if !now.Before(deadline) {
				wg.Wait()
				return
			}
			spawn(curveAt(p.sched.Segments, now.Sub(p.start)))
		}
	}
}

// vu is one virtual user: its own session, looping iterations until the
// deadline, ctx cancellation, or an abort. When once is set it runs a single
// iteration (integration/system).
func (p *pool) vu(ctx context.Context, wg *sync.WaitGroup, deadline time.Time, once bool) {
	defer wg.Done()
	clients := p.newClients()
	defer clients.close()
	local := newAcc()
	for ctx.Err() == nil {
		if !once && !time.Now().Before(deadline) {
			break
		}
		p.iterate(ctx, clients, local, 0, false)
		if once {
			break
		}
	}
	p.merge(local)
}

// vuClients are one VU's protocol clients, held for its whole life rather than
// per iteration: an HTTP session with its own transport and cookie jar, and
// gRPC channels opened on first use. Connection reuse across iterations is the
// point — a VU that re-dialled every pass would measure handshakes.
type vuClients struct {
	http *adapters.Session
	grpc *adapters.GRPCConns
}

func (p *pool) newClients() *vuClients {
	return &vuClients{
		http: adapters.NewSession(adapters.SessionOptions{Timeout: p.opts.RequestTimeout}),
		grpc: adapters.NewGRPCConns(p.opts.RequestTimeout),
	}
}

// close releases what the VU held. The HTTP session's transport is reclaimed
// with it; a gRPC channel is not, so it is closed explicitly.
func (c *vuClients) close() { c.grpc.Close() }

// runOpen enforces a profile's arrival cap as a hard ceiling (ADR 0013): a
// generator issues iterations on a fixed 1/N wall-clock schedule and a bounded
// worker pool serves them, so the target never sees more than N/s regardless of
// VU count. Intended timestamps are a pure function of the iteration index, so a
// backed-up pool shows up as latency rather than as omitted requests. The VU
// curve sizes the worker pool (capacity), not the rate; sustaining the cap needs
// enough workers (concurrency ≥ N × latency), else the rate honestly undershoots.
func (p *pool) runOpen(ctx context.Context) {
	rate := p.sched.ArrivalCap.PerSecond()
	if rate <= 0 {
		return
	}
	interval := time.Duration(float64(time.Second) / rate)
	deadline := p.start.Add(time.Duration(p.sched.Duration))

	jobs := make(chan time.Duration)
	var wg sync.WaitGroup
	for i := 0; i < p.peak(); i++ {
		wg.Add(1)
		go p.worker(ctx, jobs, &wg)
	}

	for n := 0; ; n++ {
		intended := time.Duration(n) * interval
		due := p.start.Add(intended)
		if !due.Before(deadline) {
			break
		}
		if wait := time.Until(due); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				close(jobs)
				wg.Wait()
				return
			case <-timer.C:
			}
		}
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		case jobs <- intended:
		}
	}
	close(jobs)
	wg.Wait()
}

func (p *pool) worker(ctx context.Context, jobs <-chan time.Duration, wg *sync.WaitGroup) {
	defer wg.Done()
	clients := p.newClients()
	defer clients.close()
	local := newAcc()
	for intended := range jobs {
		if ctx.Err() != nil {
			break
		}
		p.iterate(ctx, clients, local, intended, true)
	}
	p.merge(local)
}

// iterate runs one pass over the flows through the VU's clients, recording a
// sample and (per the retention budget) a trace tree per flow. intended is the
// scheduled dispatch offset for coordinated-omission accounting; when
// hasIntended is false the run is its own reference (closed model).
func (p *pool) iterate(ctx context.Context, clients *vuClients, local *acc, intended time.Duration, hasIntended bool) {
	p.active.Add(1)
	defer p.active.Add(-1)

	runner := &Runner{
		Session: clients.http,
		GRPC:    clients.grpc,
		Protos:  p.opts.Protos,
		BaseURL: p.opts.BaseURL,
		Mode:    p.sched.Mode,
		Allow:   p.opts.Allow,
		Auth:    p.opts.Auth,
	}
	for i := range p.opts.Flows {
		fl := p.opts.Flows[i]
		actual := time.Since(p.start)
		ref := actual
		if hasIntended {
			ref = intended
		}

		row := p.drawRow(fl)
		scope := NewScope(fl.Data, row)
		began := time.Now()
		it, err := runner.RunFlow(ctx, fl, scope)
		service := time.Since(began)

		outcome := span.OutcomeOK
		throttled := false
		if it != nil {
			outcome = it.Outcome
			throttled = it.Throttled
			// Any recorded failure — a real one or a throttle-as-error whose
			// span stays throttled — makes the flow-run an error.
			if len(it.Failures) > 0 {
				outcome = span.OutcomeFailed
			}
		}
		if err != nil {
			outcome = span.OutcomeFailed
		}

		local.samples = append(local.samples, Sample{
			Flow:      fl.Name,
			Intended:  ref,
			Actual:    actual,
			Service:   service,
			Outcome:   outcome,
			Throttled: throttled,
		})
		p.completed.Add(1)
		if outcome == span.OutcomeFailed {
			p.failed.Add(1)
		}
		if throttled {
			p.throttled.Add(1)
		}
		if it != nil {
			p.record(local, fl.Name, actual, service, outcome, it.Spans, scope.Secrets().RedactBytes)
			if it.Aborted {
				p.aborted.Store(true)
				p.cancel()
			}
		}
	}
	local.iters++
}

// record assembles a flow-run's trace tree and routes it to both storage tiers:
// folded into the aggregate on every iteration (unbounded-VU-safe), and kept as
// a raw tree per the capture policy. A kept trace has its bodies captured —
// redacted, then size-capped — so sampling and redaction cost nothing for the
// traces that are dropped.
func (p *pool) record(local *acc, flow string, start, dur time.Duration, outcome span.Outcome, steps []*span.Span, redact func([]byte) []byte) {
	root := span.New("flow:"+flow, start)
	root.Duration = dur
	root.Outcome = outcome
	root.Children = steps

	local.folded.Add(root)

	if p.keepTrace(outcome) {
		span.Finalize(root, redact, p.maxBodyBytes)
		local.traces = append(local.traces, root)
	}
}

// keepTrace is the capture policy: keep every failure (up to a safety ceiling),
// and one of every sampleEvery successful/throttled traces (up to the success
// cap). The counters are shared so the success sample rate holds across VUs.
func (p *pool) keepTrace(outcome span.Outcome) bool {
	if outcome == span.OutcomeFailed {
		return p.failBudget.Add(-1) >= 0
	}
	n := p.successSeen.Add(1)
	if p.sampleEvery > 1 && (n-1)%int64(p.sampleEvery) != 0 {
		return false
	}
	return p.successBudget.Add(-1) >= 0
}

func (p *pool) drawRow(fl ir.Flow) map[string]string {
	if fl.Data == "" {
		return nil
	}
	pl := p.opts.Pools[fl.Data]
	if pl == nil {
		return nil
	}
	row, ok, err := pl.Next()
	if err != nil || !ok {
		return nil
	}
	return row
}

func (p *pool) merge(local *acc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.samples = append(p.samples, local.samples...)
	p.traces = append(p.traces, local.traces...)
	p.folded.Merge(local.folded)
	p.iterations += local.iters
}

// acc is a VU's private accumulator, merged once at exit to keep the hot path
// off the shared mutex.
type acc struct {
	samples []Sample
	traces  []*span.Span
	folded  *span.Folded
	iters   int
}

func newAcc() *acc { return &acc{folded: span.NewFolded()} }

// curveAt evaluates the piecewise-linear VU curve at an elapsed offset.
func curveAt(segs []planner.Segment, elapsed time.Duration) int {
	if len(segs) == 0 {
		return 0
	}
	var base time.Duration
	for _, s := range segs {
		d := time.Duration(s.Duration)
		if d <= 0 {
			continue
		}
		if elapsed < base+d {
			frac := float64(elapsed-base) / float64(d)
			return s.StartVUs + int(frac*float64(s.EndVUs-s.StartVUs)+0.5)
		}
		base += d
	}
	return segs[len(segs)-1].EndVUs
}

// spawnTick paces the ramp spawner: fine enough to add VUs smoothly, coarse
// enough not to spin.
func spawnTick(total time.Duration, peak int) time.Duration {
	if total <= 0 || peak <= 0 {
		return 50 * time.Millisecond
	}
	t := total / time.Duration(2*peak+1)
	switch {
	case t < 5*time.Millisecond:
		return 5 * time.Millisecond
	case t > 200*time.Millisecond:
		return 200 * time.Millisecond
	default:
		return t
	}
}
