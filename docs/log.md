# Log

## 2026-07-12

- **Scope: AI prompt testing (PRD v0.4 → v0.5, ADR 0009).** Added prompt testing to v1 scope as diff-first `prompt` steps in the existing flow model: LLM calls with templated messages whose completions chain like any extraction; resolved prompt + completion always captured (carve-out from the failures-plus-sample capture policy); prompt-identity hashing; named variants with per-variant span identity (`step@variant`); and a results-server prompt diff view (variant vs variant, run vs baseline; text and structural JSON diffs). Explicit non-goals: LLM-as-judge, semantic-similarity scoring, eval datasets, prompt registries. New PRD section 10.9; adapter/variants land in M3, diff view in M4 — mirrored in GitHub milestone descriptions and new tracer-bullet issues.

## 2026-07-10

- **Creation** Scaffolded the FlowBench repository from the PRD (v0.4): documentation bundle (README, docs index, PRD copy, CONTEXT.md glossary, eight ADRs recording the PRD's settled decisions, milestone plan), Go+Python `.gitignore`, private GitHub repo `blackprince001/flowbench`, four GitHub milestones (M1–M4) mirroring the PRD's sequencing, track/type labels, and tracer-bullet issues per milestone with blocking edges. No code yet; the engine design doc (Go↔Python bridge mechanics, span encoding) is future work flagged in the PRD's open questions.
