// Command flowbench is the engine+CLI binary: it runs scenarios on demand and
// serves results locally.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/blackprince001/flowbench/internal/version"
)

const usage = `flowbench — scripting-first testing toolkit for API endpoints and multi-step flows

Usage:
  flowbench <command> [arguments]

Commands:
  run        run a scenario against a target (add --watch for a live view)
  serve      browse recorded runs from a run store on localhost
  target     resolve a target config and print it as JSON
  version    print the flowbench build identity

Run 'flowbench help' to show this message.
`

// Exit codes: the run loop distinguishes a clean pass, recorded test
// failures, and a pre-run error (bad args, parse/validate, or the safety gate).
const (
	exitOK     = 0
	exitFail   = 1
	exitPreRun = 2
)

func main() {
	os.Exit(run(os.Stdout, os.Stderr, os.Args[1:]))
}

func run(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		io.WriteString(stderr, usage)
		return exitPreRun
	}
	switch args[0] {
	case "run":
		return runScenario(stdout, stderr, args[1:])
	case "serve":
		return serve(stdout, stderr, args[1:])
	case "target":
		return targetCmd(stdout, stderr, args[1:])
	case "version", "--version":
		fmt.Fprintln(stdout, version.String())
		return exitOK
	case "help", "-h", "--help":
		io.WriteString(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "flowbench: unknown command %q\n\n", args[0])
		io.WriteString(stderr, usage)
		return exitPreRun
	}
}
