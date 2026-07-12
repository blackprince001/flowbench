// Package collector streams spans into the two storage tiers (ADR 0007):
// folded aggregates per structural span-path (the sole input to flame
// graphs) and raw trace trees kept per capture policy (the sole input to
// the waterfall view). It ingests agent and engine self-metric series,
// applies redaction before anything reaches the run store, evaluates
// thresholds and soak trends, and classifies stress knee points as
// degraded versus throttled (PRD sections 10.6, 10.7, 12).
package collector
