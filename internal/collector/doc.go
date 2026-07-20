// Package collector streams spans into the two storage tiers (folded
// aggregates and raw trace trees), applies redaction, and evaluates
// thresholds and soak trends.
package collector
