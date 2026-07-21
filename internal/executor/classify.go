package executor

import (
	"net/http"

	"github.com/blackprince001/flowbench/internal/ir"
)

// isThrottled reports whether a response status is a throttle: HTTP 429 always,
// plus any author-mapped statuses on the step. (gRPC RESOURCE_EXHAUSTED lands
// in M3.)
func isThrottled(status int, spec *ir.ThrottleSpec) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if spec != nil {
		for _, s := range spec.Statuses {
			if s == status {
				return true
			}
		}
	}
	return false
}

// throttleIsError reports whether a throttle counts as a failure. Per ADR 0006
// it does in integration/system and does not in load/stress/soak; a step's
// as_error overrides that default.
func throttleIsError(spec *ir.ThrottleSpec, mode ir.Mode) bool {
	if spec != nil && spec.AsError != nil {
		return *spec.AsError
	}
	switch mode {
	case ir.ModeIntegration, ir.ModeSystem:
		return true
	default:
		return false
	}
}
