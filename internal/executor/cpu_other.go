//go:build !unix

package executor

// cpuSeconds is unavailable outside unix; the series omits process CPU there.
func cpuSeconds() (float64, bool) { return 0, false }
