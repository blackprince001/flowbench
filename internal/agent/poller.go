package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// pollTimeout bounds a single scrape so a stalled or unreachable agent can
// never make the poller wait past one tick. Well under the default poll
// interval so one slow response doesn't cascade into a missed next tick.
const pollTimeout = 2 * time.Second

// Poll blocks until ctx is done, scraping addr's /metrics every interval and
// appending a PolledSample on success. A failed or timed-out scrape is
// silently skipped -- never blocks, never errors out -- so a dead or
// unreachable agent never affects the run it's attached to (fail-open,
// issue #32's acceptance criterion). Returns the accumulated series when ctx
// is done. Structurally this mirrors internal/executor's sampleMetrics: an
// HTTP scrape in place of a local runtime read, same ticker shape.
func Poll(ctx context.Context, addr string, interval time.Duration) []PolledSample {
	if interval <= 0 {
		<-ctx.Done()
		return nil
	}
	client := &http.Client{Timeout: pollTimeout}
	url := "http://" + addr + "/metrics"
	start := time.Now()

	t := time.NewTicker(interval)
	defer t.Stop()

	var series []PolledSample
	for {
		select {
		case <-ctx.Done():
			return series
		case now := <-t.C:
			if s, ok := scrape(ctx, client, url); ok {
				series = append(series, PolledSample{At: now.Sub(start), Sample: s})
			}
		}
	}
}

func scrape(ctx context.Context, client *http.Client, url string) (Sample, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Sample{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return Sample{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Sample{}, false
	}
	var s Sample
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return Sample{}, false
	}
	return s, true
}
