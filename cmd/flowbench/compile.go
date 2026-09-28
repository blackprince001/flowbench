package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/blackprince001/flowbench/internal/parser"
)

// compileCmd is `flowbench compile <path.flow.yaml>`: parses and validates a
// flow file the same way `run` does and prints its Scenario as JSON. This is
// the single source of truth the Python SDK's `Flow.load(...)` shells out to
// (#104), so a flow file used from Python is parsed by the same code that
// parses it for `flowbench run`, never a second YAML parser reimplemented in
// Python — the drift risk #104's checklist calls out as F1.
func compileCmd(stdout, stderr io.Writer, args []string) int {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	fs.SetOutput(stderr)

	// Accept the path before or after flags, matching every other subcommand
	// here — compile takes none today, but the parse mirrors the others so a
	// future flag doesn't have to change this.
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
		fmt.Fprintln(stderr, "usage: flowbench compile <path.flow.yaml>")
		return exitPreRun
	}

	res, err := parser.ParseFlowFile(positionals[0], nil)
	if err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitPreRun
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res.Scenario); err != nil {
		fmt.Fprintf(stderr, "flowbench: %v\n", err)
		return exitFail
	}
	return exitOK
}
