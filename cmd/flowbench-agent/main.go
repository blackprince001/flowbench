// Command flowbench-agent is the target-metrics agent (issue #32, ADR 0016):
// a small binary that runs on the system under test and serves its current
// resource use (CPU, memory, network, descriptors, load average) over HTTP
// for `flowbench run`'s poller to scrape and correlate with a run by
// elapsed time. It has no notion of runs, targets, or scenarios — it just
// answers "what does this host look like right now" on request.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
	"github.com/blackprince001/flowbench/internal/version"
)

func main() {
	os.Exit(run(os.Stdout, os.Stderr, os.Args[1:]))
}

func run(stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("flowbench-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", ":9090", "address to bind — unlike `flowbench serve`, a non-loopback "+
		"address is the normal case here: the agent must be reachable from wherever `flowbench run` runs")
	showVersion := fs.Bool("version", false, "print the build identity and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "flowbench-agent %s (commit %s)\n", version.Version, version.Commit)
		return 0
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench-agent: bind %s: %v\n", *addr, err)
		return 1
	}
	defer ln.Close()

	fmt.Fprintf(stdout, "flowbench-agent listening on http://%s/metrics\n", ln.Addr())

	srv := &http.Server{Handler: agent.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
		fmt.Fprintln(stdout, "stopped")
		return 0
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "flowbench-agent: %v\n", err)
			return 1
		}
		return 0
	}
}
