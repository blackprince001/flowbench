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

// classify maps a span to its kind from its name, depth, and whether it has
// children. Extraction spans are named for the variable they bind, so below
// depth 1 they are told apart from a used flow's own nested steps (#105) by
// children: an extraction or assertion leaf never has any, while a nested
// step almost always does (at least a network leg) — the one structural
// signal left once a flow can nest inside a `use` step, since spans carry no
// kind of their own (ADR 0007). A retry's `attempt N` wrapper is network, not
// logic — it is the call, made again.
func classify(name string, depth int, hasChildren bool) Kind {
	switch {
	case depth == 0 || strings.HasPrefix(name, "flow:"):
		return KindFlow
	case phases[name], strings.HasPrefix(name, "attempt "):
		return KindNet
	case name == "backoff":
		return KindRetry
	case strings.HasPrefix(name, "assert_"):
		return KindLogic
	case depth == 1, hasChildren:
		return KindStep
	default:
		return KindLogic
	}
}
