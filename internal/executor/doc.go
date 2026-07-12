// Package executor runs scenarios as a goroutine-per-VU pool, sized for 10k
// concurrent VUs on a single node (ADR 0001). Each VU runs iterations with
// its own cookie jar, data row, and variable scope. The executor accepts only
// the canonical flow IR (ADR 0002); protocol work is delegated to the
// adapters and Python logic steps to the bridged worker pool (ADR 0008).
package executor
