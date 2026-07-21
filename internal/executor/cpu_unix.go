//go:build unix

package executor

import "syscall"

// cpuSeconds returns cumulative process CPU time (user + system) in seconds.
func cpuSeconds() (float64, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	sec := func(tv syscall.Timeval) float64 {
		return float64(tv.Sec) + float64(tv.Usec)/1e6
	}
	return sec(ru.Utime) + sec(ru.Stime), true
}
