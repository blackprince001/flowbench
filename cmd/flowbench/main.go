// Command flowbench is the engine+CLI binary: it runs scenarios on demand and
// serves results locally. Run-now only, local-first, clean exit codes so CI
// integration stays possible later (PRD sections 10.5, 13).
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
  version    print the flowbench build identity

Run 'flowbench help' to show this message.
`

func main() {
	os.Exit(run(os.Stdout, os.Stderr, os.Args[1:]))
}

func run(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		io.WriteString(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, version.String())
		return 0
	case "help", "-h", "--help":
		io.WriteString(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "flowbench: unknown command %q\n\n", args[0])
		io.WriteString(stderr, usage)
		return 2
	}
}
