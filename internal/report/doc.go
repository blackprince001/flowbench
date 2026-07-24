// Package report renders run artifacts as HTML: flame graphs from folded
// aggregates and waterfalls from raw trace trees. Both views are laid out here
// as plain data (percent-positioned frames and rows) and share one stylesheet,
// so the two readings of the same spans (ADR 0007) cannot drift apart visually.
//
// Layout is computed in Go and emitted as static HTML; there is no client-side
// rendering step and no JavaScript build (ADR 0014).
package report
