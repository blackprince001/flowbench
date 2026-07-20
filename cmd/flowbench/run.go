package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/adapters"
	"github.com/blackprince001/flowbench/internal/data"
	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/ir"
	"github.com/blackprince001/flowbench/internal/parser"
	"github.com/blackprince001/flowbench/internal/target"
)

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
		fmt.Fprintln(stderr, "usage: flowbench run <scenario.yaml> [--target name] [--targets-dir dir] [--seed n]")
		return exitPreRun
	}
	scenarioPath := positionals[0]

	res, err := parser.ParseFlowFile(scenarioPath, nil)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(stderr, "%s\n", w)
	}
	sc := res.Scenario

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

	return execute(stdout, stderr, sc, tgt, pools)
}

// execute runs each flow at one VU — once per fixture row when the flow binds
// a pool, otherwise once — and returns the process exit code.
func execute(stdout, stderr io.Writer, sc *ir.Scenario, tgt *target.Target, pools map[string]*data.Pool) int {
	start := time.Now()
	iterations, failed := 0, 0

	fmt.Fprintf(stdout, "running %q against %s (%s)\n", sc.Name, tgt.Config().Name, tgt.BaseURL())

	for _, flow := range sc.Flows {
		rows := iterationRows(flow, pools)
		for i, row := range rows {
			iterations++
			scope := executor.NewScope(flow.Data, row)
			runner := &executor.Runner{
				Session: adapters.NewSession(adapters.SessionOptions{}),
				BaseURL: tgt.BaseURL(),
				Mode:    sc.Profile.Mode,
				Allow:   tgt.Allows,
			}
			it, err := runner.RunFlow(context.Background(), flow, scope)
			if err != nil {
				fmt.Fprintf(stderr, "  %s [%d/%d]  error: %v\n", flow.Name, i+1, len(rows), err)
				failed++
				continue
			}
			if len(it.Failures) == 0 {
				fmt.Fprintf(stdout, "  %s [%d/%d]  ok\n", flow.Name, i+1, len(rows))
				continue
			}
			failed++
			fmt.Fprintf(stdout, "  %s [%d/%d]  FAIL (%d)\n", flow.Name, i+1, len(rows), len(it.Failures))
			for _, f := range it.Failures {
				fmt.Fprintf(stdout, "      %s: %s\n", f.StepID, f.Detail)
			}
			if it.Aborted {
				fmt.Fprintf(stderr, "flowbench: run aborted by %q\n", flow.Name)
			}
		}
	}

	passed := iterations - failed
	fmt.Fprintf(stdout, "%d iteration(s): %d passed, %d failed  (%s)\n",
		iterations, passed, failed, time.Since(start).Round(time.Millisecond))
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
