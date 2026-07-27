// Package agent is the target-metrics agent's protocol: the wire type both
// the standalone agent binary (cmd/flowbench-agent) and the engine's poller
// share, the OS-level host sampling behind it, the HTTP server the agent
// exposes, and the client-side poller the engine uses to correlate a
// target's resource use with a run (ADR 0016, issue #32).
package agent

import "time"

// Sample is one reading of the target host's resource use, so target
// saturation and generator saturation (internal/executor.MetricSample) are
// never confused when a run is read back.
//
// CPUSeconds is cumulative non-idle CPU time summed across every core since
// boot, paired with NumCPU so a consumer can normalize: diff two samples'
// CPUSeconds, divide by (elapsed wall time × NumCPU), to get a 0..1
// utilization fraction. This mirrors MetricSample's own "cumulative, a
// consumer diffs adjacent samples" convention. NetRxBytes/NetTxBytes are
// cumulative the same way; MemUsedBytes/MemTotalBytes/OpenFDs/LoadAvg1 are
// point-in-time OS-reported values, not counters.
type Sample struct {
	CPUSeconds    float64 `json:"cpu_seconds"`
	NumCPU        int     `json:"num_cpu"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	MemTotalBytes uint64  `json:"mem_total_bytes"`
	NetRxBytes    uint64  `json:"net_rx_bytes"`
	NetTxBytes    uint64  `json:"net_tx_bytes"`
	OpenFDs       int     `json:"open_fds"`
	LoadAvg1      float64 `json:"load_avg_1"`
}

// PolledSample is one Sample as the engine's poller recorded it: At is
// elapsed time since the poller started, the same convention as
// executor.MetricSample.At, collector.SeriesPoint.At, and
// collector.FlowRun.At.
type PolledSample struct {
	At time.Duration `json:"at"`
	Sample
}
