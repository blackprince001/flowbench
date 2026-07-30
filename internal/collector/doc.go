// Package collector streams spans into the two storage tiers (folded
// aggregates and raw trace trees), applies redaction, evaluates thresholds
// and soak trends, and classifies the stress knee as degraded or throttled.
package collector
