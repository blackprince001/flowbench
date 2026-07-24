package report

import "strings"

// Kind is the colour class a span belongs to. Spans carry no kind of their own
// — names are structural identity, not type (ADR 0007) — so it is recovered
// from the name plus the depth at which the name appears, which is unambiguous
// for every name the engine emits.
type Kind string

const (
	KindFlow  Kind = "flow"  // the iteration root: scaffolding, recessive
	KindStep  Kind = "step"  // one authored step
	KindNet   Kind = "net"   // the call and its protocol phases
	KindLogic Kind = "logic" // assertions and extractions — the flow's own work
	KindRetry Kind = "retry" // backoff waits between attempts
)

// phases are the protocol legs the HTTP adapter emits under a call.
var phases = map[string]bool{
	"http_call": true,
	"dns":       true,
	"connect":   true,
	"tls":       true,
	"ttfb":      true,
	"transfer":  true,
}

// classify maps a span name at a given depth to its kind. Extraction spans are
// named for the variable they bind, so they are identified by position: any
// unrecognized name below a step is the flow's own logic.
func classify(name string, depth int) Kind {
	switch {
	case depth == 0 || strings.HasPrefix(name, "flow:"):
		return KindFlow
	case phases[name]:
		return KindNet
	case name == "backoff":
		return KindRetry
	case strings.HasPrefix(name, "assert_"):
		return KindLogic
	case depth == 1:
		return KindStep
	default:
		return KindLogic
	}
}
