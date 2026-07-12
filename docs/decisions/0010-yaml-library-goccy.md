---
type: Decision
title: "ADR 0010: goccy/go-yaml for the YAML authoring surface"
description: The YAML DSL parser is built on goccy/go-yaml, chosen for active maintenance and AST-level position data that powers file/line pre-run errors.
status: Accepted
timestamp: 2026-07-12
---
# ADR 0010: goccy/go-yaml for the YAML authoring surface

Status: Accepted

## Context

The parser must turn `*.flow.yaml` into the canonical IR and report validation
failures as pre-run errors with file/line context (PRD 10.5, issue #3). That
requires a YAML library that is both maintained and exposes node positions,
not just decoded values. As of 2026-07: the original `go-yaml/yaml`
(`gopkg.in/yaml.v3`) was declared unmaintained by its author and adopted by
the YAML org as a frozen legacy line (v3 receives security fixes only); the
org's active successor, `go.yaml.in/yaml/v4`, is months old; and
`goccy/go-yaml` is an independent, actively maintained implementation
(~2,300 importers) whose `parser`/`ast` packages carry per-token line/column
positions and whose errors render caret-annotated source snippets.

## Decision

Use `github.com/goccy/go-yaml` — specifically its `parser` and `ast`
packages, walking the AST so every IR node the parser builds carries source
position. This is the engine's first third-party dependency.

## Consequences

- File/line (and column) context comes from AST tokens, and goccy's
  annotated-source error style sets the bar for the parser's own messages.
- Rejected: `gopkg.in/yaml.v3` — frozen legacy, security-fixes only; wrong
  foundation for a new P0 surface. `go.yaml.in/yaml/v4` — the eventual
  successor but too young to anchor the DSL today; revisit if goccy's
  maintenance changes. `sigs.k8s.io/yaml` — a JSON round-trip wrapper that
  discards position data.
- The dependency is confined to `internal/parser`; the IR and executor stay
  dependency-free, so swapping YAML libraries later touches one package.
