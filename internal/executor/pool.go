package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/data"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/planner"
	"github.com/blackprince001/flowbench/internal/span"
)

const (
	defaultMetricInterval = time.Second
	defaultMaxTraces      = 200   // retained success traces; failures kept up to maxFailTraces
	maxFailTraces         = 10000 // ceiling on retained failure traces before capture policy (#19)
)

// Options configures a pool run. BaseURL, Allow, and the schedule's mode mirror
// the single-VU Runner; the pool owns the concurrency and isolation on top.
type Options struct {
	Schedule *planner.Schedule
	Flows    []ir.Flow
	Pools    map[string]*data.Pool
	BaseURL  string
	Allow    func(string) (bool, error)

	// Metrics is the self-metric sample interval; 0 uses a default, negative
	// disables sampling.
	Metrics time.Duration
	// MaxTraces caps retained success traces; 0 uses a default. Failure traces
	// are kept regardless, up to an internal ceiling.
	MaxTraces int
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
	metricInterval := opts.Metrics
	if metricInterval == 0 {
		metricInterval = defaultMetricInterval
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p := &pool{opts: opts, sched: opts.Schedule, start: time.Now(), cancel: cancel}
	p.successBudget.Store(int64(maxTraces))
	p.failBudget.Store(maxFailTraces)

	// Sample the generator's own resource use for the run's lifetime.
	mctx, mstop := context.WithCancel(context.Background())
	var mwg sync.WaitGroup
	mwg.Go(func() {
		p.metrics = sampleMetrics(mctx, p.start, metricInterval, func() int { return int(p.active.Load()) })
	})

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
		Outcomes:   tally(p.samples),
		Samples:    p.samples,
		Traces:     p.traces,
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

	successBudget atomic.Int64
	failBudget    atomic.Int64

	mu         sync.Mutex
	samples    []Sample
	traces     []*span.Span
	iterations int

	metrics []MetricSample // written by the sampler goroutine, read after it joins
}

func (p *pool) peak() int {
	if p.sched.PeakVUs < 1 {
		return 1
	}
	return p.sched.PeakVUs
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
	sess := adapters.NewSession(adapters.SessionOptions{})
	local := &acc{}
	for ctx.Err() == nil {
		if !once && !time.Now().Before(deadline) {
			break
		}
		p.iterate(ctx, sess, local, 0, false)
		if once {
			break
		}
	}
	p.merge(local)
}

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
	sess := adapters.NewSession(adapters.SessionOptions{})
	local := &acc{}
	for intended := range jobs {
		if ctx.Err() != nil {
			break
		}
		p.iterate(ctx, sess, local, intended, true)
	}
	p.merge(local)
}

// iterate runs one pass over the flows through sess, recording a sample and
// (per the retention budget) a trace tree per flow. intended is the scheduled
// dispatch offset for coordinated-omission accounting; when hasIntended is
// false the run is its own reference (closed model).
func (p *pool) iterate(ctx context.Context, sess *adapters.Session, local *acc, intended time.Duration, hasIntended bool) {
	p.active.Add(1)
	defer p.active.Add(-1)

	runner := &Runner{Session: sess, BaseURL: p.opts.BaseURL, Mode: p.sched.Mode, Allow: p.opts.Allow}
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
		if it != nil {
			p.retain(local, fl.Name, actual, service, outcome, it.Spans)
			if it.Aborted {
				p.aborted.Store(true)
				p.cancel()
			}
		}
	}
	local.iters++
}

// retain keeps a flow-run's trace tree when the budget allows: failures up to a
// ceiling, successes up to the configured cap. A synthetic root gathers the
// step spans so the run reads as one tree in the waterfall view.
func (p *pool) retain(local *acc, flow string, start, dur time.Duration, outcome span.Outcome, steps []*span.Span) {
	budget := &p.successBudget
	if outcome == span.OutcomeFailed {
		budget = &p.failBudget
	}
	if budget.Add(-1) < 0 {
		return
	}
	root := span.New("flow:"+flow, start)
	root.Duration = dur
	root.Outcome = outcome
	root.Children = steps
	local.traces = append(local.traces, root)
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
	p.iterations += local.iters
}

// acc is a VU's private accumulator, merged once at exit to keep the hot path
// off the shared mutex.
type acc struct {
	samples []Sample
	traces  []*span.Span
	iters   int
}

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
