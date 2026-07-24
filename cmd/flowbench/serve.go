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
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/server"
	"github.com/blackprince001/flowbench/internal/store"
)

// defaultAddr binds the loopback interface only. Runs hold captured request and
// response bodies, so the server is not something to expose by accident
// (ADR 0004) — a non-loopback --addr is allowed but announced.
const defaultAddr = "127.0.0.1:7580"

// storeList collects a repeatable --store flag.
type storeList []string

func (l *storeList) String() string { return strings.Join(*l, ",") }
func (l *storeList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// serve is `flowbench serve [--store dir ...] [--addr host:port]`: a read-only
// browser view over one or more run stores, in the pprof-http idiom. Each
// --store is a project; repeat the flag to serve several at once, and label one
// with `name=dir` to give the project a name of its own.
func serve(stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var stores storeList
	fs.Var(&stores, "store", "run store to serve as a project; repeat for several, `name=dir` to label one")
	addr := fs.String("addr", defaultAddr, "address to bind")
	if err := fs.Parse(args); err != nil {
		return exitPreRun
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: flowbench serve [--store [name=]dir ...] [--addr host:port]")
		return exitPreRun
	}
	if len(stores) == 0 {
		stores = storeList{"runs"} // the conventional default store
	}

	ws, err := store.NewWorkspace(stores)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: bind %s: %v\n", *addr, err)
		return exitPreRun
	}
	defer ln.Close()

	if !isLoopback(ln.Addr()) {
		fmt.Fprintf(stderr, "flowbench: warning: %s is not loopback — runs carry captured request/response bodies\n", ln.Addr())
	}

	for _, p := range ws.Projects() {
		runs, _ := p.Runs()
		fmt.Fprintf(stdout, "serving %d run(s) from %s (%s)\n", len(runs), p.Store.Root(), p.Name)
	}
	fmt.Fprintf(stdout, "  http://%s\n", ln.Addr())

	srv := &http.Server{Handler: server.New(ws), ReadHeaderTimeout: 10 * time.Second}

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
		return exitOK
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "flowbench: %v\n", err)
			return exitPreRun
		}
		return exitOK
	}
}

func isLoopback(a net.Addr) bool {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}
