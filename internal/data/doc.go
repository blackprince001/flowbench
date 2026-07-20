// Package data loads fixture files into rows and dispenses them to concurrent
// consumers under a distribution and exhaustion policy. Keeping uniqueness
// under concurrency is the engine's job, not the flow author's, so a
// unique-per-vu pool never hands the same row to two VUs.
package data
