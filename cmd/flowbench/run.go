package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/auth"
	"github.com/blackprince001/flowbench/internal/collector"
	"github.com/blackprince001/flowbench/internal/data"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
	"github.com/blackprince001/flowbench/internal/planner"
	"github.com/blackprince001/flowbench/internal/span"
	"github.com/blackprince001/flowbench/internal/target"
)

// integrationBodyCap mirrors pool.go's defaultBodyCap: the captured request/
// response body size, in bytes, kept in a persisted integration/system trace.
const integrationBodyCap = 2048

// runScenario is `flowbench run <scenario.yaml> --target <name>`: parse,
// validate, gate against the target, then execute at one VU — once, or once
// per fixture row — printing a terse summary. Exit codes distinguish a clean
// pass, recorded failures, and a pre-run error.
func runScenario(stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	targetName := fs.String("target", "local", "target config name or path to a target file")
	targetsDir := fs.String("targets-dir", "targets", "directory to resolve --target names in")
	seed := fs.Int64("seed", 1, "seed for random data-pool draws")
	storeDir := fs.String("store", "runs", "run store directory for load/stress/soak artifacts")
	watch := fs.Bool("watch", false, "serve a live view of the run, abortable from the browser (load/stress/soak)")
	addr := fs.String("addr", defaultAddr, "address for the --watch live server")

	// Accept the scenario path before or after the flags: the stdlib flag
	// package stops at the first positional, so pull a leading one out first.
	var lead []string
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		lead, rest = args[:1], args[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return exitPreRun
	}
	positionals := append(lead, fs.Args()...)
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "usage: flowbench run <scenario.yaml> [--target name] [--targets-dir dir] [--seed n] [--watch [--addr host:port]]")
		return exitPreRun
	}
	scenarioPath := positionals[0]

	var sc *ir.Scenario
	var err error
	if strings.HasSuffix(scenarioPath, ".py") {
		// Python that only constructs the IR compiles here and runs entirely
		// on this engine (ADR 0012) — no different from the YAML path below
		// once we have a *ir.Scenario in hand.
		sc, err = compilePythonScenario(scenarioPath)
		if err != nil {
			fmt.Fprintf(stderr, "flowbench: %v\n", err)
			return exitPreRun
		}
		if err := sc.Validate(); err != nil {
			fmt.Fprintf(stderr, "flowbench: %v\n", err)
			return exitPreRun
		}
	} else {
		res, err := parser.ParseFlowFile(scenarioPath, nil)
		if err != nil {
			fmt.Fprintf(stderr, "flowbench: %v\n", err)
			return exitPreRun
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(stderr, "%s\n", w)
		}
		sc = res.Scenario
	}

	tgt, err := resolveTarget(*targetName, *targetsDir)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}
	if err := tgt.Check(sc); err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}

	pools, err := loadPools(sc, filepath.Dir(scenarioPath), *seed)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}

	// gRPC schemas are compiled here, next to the allow-list and the ceilings,
	// because they belong to the same class of problem: a method that is not in
	// the .proto is as knowable before the run as a host that is not allowed,
	// and finding it out on the first iteration of a 10k-VU run is finding it
	// out 10k times.
	protos := adapters.NewProtoRegistry(filepath.Dir(scenarioPath))
	if err := protos.Prepare(context.Background(), sc); err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}

	watchAddr := ""
	if *watch {
		watchAddr = *addr
	}
	return execute(stdout, stderr, sc, tgt, pools, protos, *storeDir, scenarioPath, watchAddr)
}

// execute dispatches on the profile's mode: load/stress/soak run under the
// goroutine-per-VU pool with threshold evaluation; integration/system run once
// (or once per fixture row) with loud per-iteration failures.
func execute(stdout, stderr io.Writer, sc *ir.Scenario, tgt *target.Target, pools map[string]*data.Pool, protos *adapters.ProtoRegistry, storeRoot, scenarioPath, watchAddr string) int {
	switch sc.Profile.Mode {
	case ir.ModeLoad, ir.ModeStress, ir.ModeSoak:
		return executeLoad(stdout, stderr, sc, tgt, pools, protos, storeRoot, scenarioPath, watchAddr)
	default:
		if watchAddr != "" {
			fmt.Fprintln(stderr, "flowbench: --watch applies to load/stress/soak runs; ignoring")
		}
		return executeOnce(stdout, stderr, sc, tgt, pools, protos, storeRoot, scenarioPath)
	}
}

// executeLoad plans the profile into a schedule, drives it under the VU pool,
// prints an aggregate summary, and evaluates thresholds — point-in-time, plus
// soak trend drift. The run exits nonzero only on a threshold breach or an
// abort; individual failures are data in these modes, not test failures.
func executeLoad(stdout, stderr io.Writer, sc *ir.Scenario, tgt *target.Target, pools map[string]*data.Pool, protos *adapters.ProtoRegistry, storeRoot, scenarioPath, watchAddr string) int {
	sched, err := planner.Plan(sc.Profile)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}
	if err := planner.CheckCeilings(sched, tgt.Config().MaxVUs, tgt.Config().MaxRPS); err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}
	thresholds, err := collector.ParseThresholds(sc.Profile.Thresholds)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}

	// The live view runs the scenario and serves it from one process, with abort
	// as its one write path (PRD 10.7).
	if watchAddr != "" {
		return runLive(stdout, stderr, sc, tgt, pools, protos, storeRoot, scenarioPath, watchAddr, sched, thresholds)
	}

	fmt.Fprintf(stdout, "running %q against %s (%s) [%s, %d VUs]\n",
		sc.Name, tgt.Config().Name, tgt.BaseURL(), sc.Profile.Mode, sched.PeakVUs)

	// Ctrl-C cancels the run context, which unwinds every VU; the partial run is
	// still summarized and flushed to the store below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	startedAt := time.Now()
	stopAgent := startAgentPoll(tgt.AgentAddr())
	res, err := executor.Run(ctx, executor.Options{
		Schedule:       sched,
		Flows:          sc.Flows,
		Pools:          pools,
		BaseURL:        tgt.BaseURL(),
		Allow:          tgt.Allows,
		Protos:         protos,
		RequestTimeout: tgt.RequestTimeout(),
	})
	agentSeries := stopAgent()
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitFail
	}
	interrupted := ctx.Err() != nil

	printRunSummary(stdout, res)

	outcomes := collector.Evaluate(thresholds, res)
	if sc.Profile.Mode == ir.ModeSoak {
		outcomes = append(outcomes, collector.EvaluateTrends(res)...)
	}
	breached := printOutcomes(stdout, outcomes)

	// Persist the run artifact regardless of pass/fail — a breaching run is
	// exactly the one worth keeping. A store failure is a warning, not an exit.
	if dir, err := saveRun(storeRoot, scenarioPath, sc, tgt, startedAt, res, outcomes, agentSeries); err != nil {
		fmt.Fprintf(stderr, "flowbench: could not save run: %v\n", err)
	} else {
		fmt.Fprintf(stdout, "run saved to %s\n", dir)
	}

	if interrupted {
		fmt.Fprintln(stderr, "flowbench: interrupted; partial run saved")
		return exitFail
	}
	if res.Aborted {
		fmt.Fprintln(stderr, "flowbench: run aborted (kill switch)")
		return exitFail
	}
	if breached {
		return exitFail
	}
	return exitOK
}

func printRunSummary(w io.Writer, res *executor.Result) {
	fmt.Fprintf(w, "  %d iteration(s), %d flow-run(s) in %s\n",
		res.Iterations, len(res.Samples), time.Duration(res.Duration).Round(time.Millisecond))
	fmt.Fprintf(w, "  error_rate=%.2f%%  throttle_rate=%.2f%%  p50=%s p95=%s p99=%s\n",
		res.ErrorRate()*100, res.ThrottleRate()*100,
		executor.Percentile(res.Samples, 0.50).Round(time.Microsecond),
		executor.Percentile(res.Samples, 0.95).Round(time.Microsecond),
		executor.Percentile(res.Samples, 0.99).Round(time.Microsecond))
}

// printOutcomes prints each threshold/trend result and reports whether any
// breached.
func printOutcomes(w io.Writer, outcomes []collector.Outcome) bool {
	breached := false
	for _, o := range outcomes {
		status := "ok"
		if !o.Pass {
			status = "BREACH"
			breached = true
		}
		fmt.Fprintf(w, "  %s: %s  (%s)\n", o.Expr, status, o.Detail)
	}
	return breached
}

// executeOnce runs each flow at one VU — once per fixture row when the flow
// binds a pool, otherwise once — and returns the process exit code. Unlike
// load/stress/soak, integration/system previously never persisted a run
// artifact; it now builds the same executor.Result shape executeLoad does
// (every iteration kept — the scale here is small enough that sampling would
// only lose signal) and saves it the same way, so an integration-mode run is
// inspectable via `flowbench serve` too, and comparable against a
// Python-driven run of the same flow (ADR 0012's two-producer model).
func executeOnce(stdout, stderr io.Writer, sc *ir.Scenario, tgt *target.Target, pools map[string]*data.Pool, protos *adapters.ProtoRegistry, storeRoot, scenarioPath string) int {
	start := time.Now()
	iterations, failed := 0, 0

	var samples []executor.Sample
	var traces []*span.Span
	folded := span.NewFolded()
	aborted := false

	fmt.Fprintf(stdout, "running %q against %s (%s)\n", sc.Name, tgt.Config().Name, tgt.BaseURL())

	// One provider for the whole command, so a token fetched for the first
	// fixture row serves the rest, gated by the same allow-list as the calls.
	credentials := auth.NewProvider(auth.Options{Allow: tgt.Allows})

	for _, flow := range sc.Flows {
		rows := iterationRows(flow, pools)
		for i, row := range rows {
			iterations++
			iterStart := time.Now()
			scope := executor.NewScope(flow.Data, row)
			channels := adapters.NewGRPCConns(tgt.RequestTimeout())
			runner := &executor.Runner{
				Session: adapters.NewSession(adapters.SessionOptions{Timeout: tgt.RequestTimeout()}),
				GRPC:    channels,
				Protos:  protos,
				BaseURL: tgt.BaseURL(),
				Mode:    sc.Profile.Mode,
				Allow:   tgt.Allows,
				Auth:    credentials,
			}
			it, err := runner.RunFlow(context.Background(), flow, scope)
			channels.Close()
			if err != nil {
				fmt.Fprintf(stderr, "  %s [%d/%d]  error: %v\n", flow.Name, i+1, len(rows), err)
				failed++
				continue
			}
			if len(it.Failures) == 0 {
				fmt.Fprintf(stdout, "  %s [%d/%d]  ok\n", flow.Name, i+1, len(rows))
			} else {
				failed++
				fmt.Fprintf(stdout, "  %s [%d/%d]  FAIL (%d)\n", flow.Name, i+1, len(rows), len(it.Failures))
				for _, f := range it.Failures {
					fmt.Fprintf(stdout, "      %s: %s\n", f.StepID, f.Detail)
				}
				if it.Aborted {
					aborted = true
					fmt.Fprintf(stderr, "flowbench: run aborted by %q\n", flow.Name)
				}
			}

			dispatch := iterStart.Sub(start)
			service := time.Since(iterStart)
			samples = append(samples, executor.Sample{
				Flow: flow.Name, Intended: dispatch, Actual: dispatch, Service: service,
				Outcome: it.Outcome, Throttled: it.Throttled,
			})

			root := span.New("flow:"+flow.Name, dispatch)
			root.Duration = service
			root.Outcome = it.Outcome
			root.Children = it.Spans
			folded.Add(root)
			span.Finalize(root, scope.Secrets().RedactBytes, integrationBodyCap)
			traces = append(traces, root)
		}
	}

	passed := iterations - failed
	fmt.Fprintf(stdout, "%d iteration(s): %d passed, %d failed  (%s)\n",
		iterations, passed, failed, time.Since(start).Round(time.Millisecond))

	res := &executor.Result{
		Duration:   time.Since(start),
		Iterations: iterations,
		Outcomes:   executor.Tally(samples),
		Samples:    samples,
		Traces:     traces,
		Folded:     folded,
		Metrics:    []executor.MetricSample{},
		Aborted:    aborted,
	}
	if dir, err := saveRun(storeRoot, scenarioPath, sc, tgt, start, res, nil, nil); err != nil {
		fmt.Fprintf(stderr, "flowbench: could not save run: %v\n", err)
	} else {
		fmt.Fprintf(stdout, "run saved to %s\n", dir)
	}

	if failed > 0 {
		return exitFail
	}
	return exitOK
}

// iterationRows is the rows a flow runs over: one per fixture row when it binds
// a pool, otherwise a single nil row (run once).
func iterationRows(flow ir.Flow, pools map[string]*data.Pool) []data.Row {
	p := pools[flow.Data]
	if flow.Data == "" || p == nil {
		return []data.Row{nil}
	}
	rows := make([]data.Row, 0, p.Len())
	for i := 0; i < p.Len(); i++ {
		row, ok, err := p.Next()
		if err != nil || !ok {
			break
		}
		rows = append(rows, row)
	}
	return rows
}

func loadPools(sc *ir.Scenario, baseDir string, seed int64) (map[string]*data.Pool, error) {
	pools := make(map[string]*data.Pool, len(sc.DataPools))
	for _, dp := range sc.DataPools {
		p, err := data.Load(dp, baseDir, seed)
		if err != nil {
			return nil, err
		}
		pools[dp.Name] = p
	}
	return pools, nil
}

func resolveTarget(nameOrPath, dir string) (*target.Target, error) {
	if looksLikePath(nameOrPath) {
		return loadTargetFile(nameOrPath)
	}
	path := target.Resolve(nameOrPath, dir)
	if fileExists(path) {
		return loadTargetFile(path)
	}
	if nameOrPath == "local" {
		return target.New(&ir.TargetConfig{Name: "local", BaseURLs: []string{"http://localhost:8080"}})
	}
	return nil, fmt.Errorf("no target config %q found (looked for %s)", nameOrPath, path)
}

func loadTargetFile(path string) (*target.Target, error) {
	cfg, err := parser.ParseTargetFile(path)
	if err != nil {
		return nil, err
	}
	return target.New(cfg)
}

func looksLikePath(s string) bool {
	return strings.ContainsAny(s, "/\\") || strings.HasSuffix(s, ".yaml") || strings.HasSuffix(s, ".yml")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
