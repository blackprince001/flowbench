//go:build !linux

package agent

import "errors"

// ErrUnsupported is returned by Read on platforms without a host-sampling
// implementation yet (v1 is Linux-only; PRD 13 lists macOS as a compat
// target for a later pass — macOS has no /proc, needing real sysctl
// syscalls this package doesn't reach for yet). Fail-open applies here too:
// a macOS-hosted agent simply serves this error, and a run against it
// proceeds with no resource overlay rather than a broken one.
var ErrUnsupported = errors.New("agent: host sampling is not implemented on this OS yet (Linux only)")

func Read() (Sample, error) {
	return Sample{}, ErrUnsupported
}
