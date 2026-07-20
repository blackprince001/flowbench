---
type: Decision
title: "ADR 0011: hand-rolled JSONPath subset for extraction and body assertions"
description: Extraction and body assertions evaluate a small hand-rolled JSONPath subset in internal/eval rather than a third-party library, keeping the evaluation layer dependency-free behind a swappable seam.
status: Accepted
timestamp: 2026-07-20
---
# ADR 0011: hand-rolled JSONPath subset for extraction and body assertions

Status: Accepted

## Context

Chaining (issue #5) extracts values from a JSON response into flow variables
and asserts on body content, both addressed by JSONPath (PRD 10.2: "JSONPath
for JSON; XPath for XML bodies when SOAP lands"). The M1 flows use only simple
paths — `$.data.access_token`, `$.data.id`, `$.items[0].id`. Full JSONPath
(filters, wildcards, recursive descent, unions) is a large surface to import
or implement, and none of it appears in the milestone's flows.

ADR 0010 set the project's dependency posture: goccy/go-yaml is the engine's
first third-party dependency, confined to `internal/parser`, so "the IR and
executor stay dependency-free." Extraction and assertion evaluation live in
that dependency-free layer.

## Decision

Hand-roll a minimal JSONPath subset in `internal/eval`, behind the
`queryJSON(body, path)` function. Supported: a leading `$`, dot keys
(`$.a.b`), bracket keys (`$['a.b']`), and array indices including negative
(`$.a[0]`, `$.a[-1]`). Explicitly unsupported and rejected at parse time:
recursive descent (`..`), wildcards, filter expressions, and unions.

`internal/eval` is adapter-neutral (it reads a small `Target` interface —
status, header, body, latency), so the same evaluator serves the HTTP adapter
now and GraphQL/gRPC in M3.

## Consequences

- No new dependency; the evaluation layer stays importable without pulling in
  a JSONPath engine, honoring ADR 0010's boundary.
- The subset is a single function seam. If a flow ever needs full JSONPath, a
  library (e.g. ohler55/ojg, theory/jsonpath) slots in behind `queryJSON`
  without touching callers — and would get its own ADR then.
- Rejected: adopting a JSONPath library now — premature for the paths M1
  actually uses, and it would widen the dependency-free evaluation boundary
  for expressiveness nothing yet exercises.
- Unsupported syntax fails as a pre-run/eval error rather than silently
  mismatching, so a flow author learns the boundary immediately.
