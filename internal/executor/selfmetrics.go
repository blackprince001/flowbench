package executor

import (
	"context"
	"runtime"
	"time"
)

// MetricSample is one reading of the generator's own resource use, so target
// saturation and generator saturation are never confused when the run is
// read back. CPUSeconds is cumulative process CPU time; a consumer diffs
// adjacent samples to get utilization.
type MetricSample struct {
	At         time.Duration `json:"at"`
	ActiveVUs  int           `json:"active_vus"`
	Goroutines int           `json:"goroutines"`
	HeapAlloc  uint64        `json:"heap_alloc"`
	CPUSeconds float64       `json:"cpu_seconds"`
}

// sampleMetrics blocks until ctx is done, appending a reading every interval,
// and returns the series. activeVUs reports the live VU count at each tick.
func sampleMetrics(ctx context.Context, start time.Time, interval time.Duration, activeVUs func() int) []MetricSample {
	if interval <= 0 {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	var series []MetricSample
	for {
		select {
		case <-ctx.Done():
			return series
		case now := <-t.C:
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			cpu, _ := cpuSeconds()
			series = append(series, MetricSample{
				At:         now.Sub(start),
				ActiveVUs:  activeVUs(),
				Goroutines: runtime.NumGoroutine(),
				HeapAlloc:  ms.HeapAlloc,
				CPUSeconds: cpu,
			})
		}
	}
}
