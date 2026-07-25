package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
)

// targetCmd is `flowbench target <name> [--targets-dir dir]`: resolves a
// target the same way `run` does and prints its config as JSON. This is the
// single source of truth the Python SDK's live-execution path shells out to
// for base URL and host allow-list resolution, so target-file parsing exists
// in exactly one place (ADR 0012's two-producer model shares the span model
// and run store, not a parser).
func targetCmd(stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("target", flag.ContinueOnError)
	fs.SetOutput(stderr)
	targetsDir := fs.String("targets-dir", "targets", "directory to resolve target names in")

	// Accept the target name before or after the flags: the stdlib flag
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
		fmt.Fprintln(stderr, "usage: flowbench target <name> [--targets-dir dir]")
		return exitPreRun
	}

	tgt, err := resolveTarget(positionals[0], *targetsDir)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tgt.Config()); err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitFail
	}
	return exitOK
}
