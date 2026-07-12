// Package adapters implements protocol adapters (HTTP first; GraphQL,
// WebSocket, gRPC, later SOAP) behind a common step interface. Each call
// emits spans for its protocol phases (dns/connect/tls/ttfb/transfer),
// executes any retry/backoff policy with one span per attempt, and
// classifies the outcome as ok, failed, throttled, or skipped
// (PRD 10.2, 10.5; ADRs 0006, 0007).
package adapters
