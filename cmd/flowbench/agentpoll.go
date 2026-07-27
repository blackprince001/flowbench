package main

import (
	"context"
	"time"

	"github.com/blackprince001/flowbench/internal/agent"
)

// defaultAgentPollInterval matches internal/executor's self-metrics sample
// interval, so a target's resource series and the generator's own line up
// on the same cadence.
const defaultAgentPollInterval = time.Second

// startAgentPoll begins polling addr's attached agent (issue #32), if addr
// is non-empty, for the duration between this call and the returned stop
// function's call. The poll runs on its own context — deliberately not
// sharing executor.Run's — so a stalled or unreachable agent can never
// block flow execution (fail-open), mirroring why internal/executor's
// self-metrics sampler also owns an independent context rather than the
// run's. stop must be called exactly once.
func startAgentPoll(addr string) (stop func() []agent.PolledSample) {
	if addr == "" {
		return func() []agent.PolledSample { return nil }
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []agent.PolledSample, 1)
	go func() {
		done <- agent.Poll(ctx, addr, defaultAgentPollInterval)
	}()

	return func() []agent.PolledSample {
		cancel()
		return <-done
	}
}
