---
type: Decision
title: "ADR 0009: Prompt testing as diff-first prompt steps in the flow model"
description: LLM prompts are a step type in the same flows and IR; completions are always captured, and the v1 analysis primitive is the diff, not a score.
status: Accepted
timestamp: 2026-07-12
---
# ADR 0009: Prompt testing as diff-first prompt steps in the flow model

Status: Accepted (per PRD v0.5, section 10.9)

## Context

Teams increasingly ship LLM-backed features, and prompt iteration happens by eyeballing playground outputs: nothing captures which prompt produced which completion, nothing diffs outputs across prompt versions, and nothing tests a prompt inside the real flow it belongs to (fetch context → prompt → parse → act). Standalone eval harnesses (promptfoo, LangSmith, Braintrust) are prompt-centric and scoring-centric but live outside the API-flow world FlowBench already models — and building a scoring platform would be exactly the scope creep the non-goals exist to prevent.

## Decision

A prompt is a **step**, not a separate subsystem. A `prompt` step type calls an LLM provider (OpenAI-compatible chat-completions HTTP APIs first) with templated messages; its completion is extractable into flow variables, so a chain of prompts — or a mixed prompt+API flow — is just a flow. Prompt steps ride the existing mechanics: HTTP adapter, `{{ env.* }}` credentials and redaction, retry/backoff, `throttled` outcome classification, spans.

Three prompt-specific rules on top:

1. **Always capture.** Prompt steps capture the resolved prompt and full completion on every iteration, overriding the failures-plus-sample capture policy — a diff needs both sides. Size caps and redaction still apply.
2. **Prompt identity.** The resolved message template is hashed and stored per step per run, so the diff view distinguishes "the prompt changed" from "the output changed under the same prompt."
3. **Diff, don't score.** The v1 analysis primitive is the prompt diff view — variant vs variant within a run, same step/variant vs a baseline run; text diff by default, structural diff for JSON — extending the regression-comparison surface. A prompt step may declare named **variants** fanned out per iteration, each with its own structural span identity (`step@variant`), so folding, metrics, and diffs stay per-variant.

LLM-as-judge, semantic-similarity scoring, eval-dataset management, and any prompt registry are explicit non-goals; prompts live in flow files in git.

## Consequences

- "How are my prompts doing" is answered by inspection of actual differences, reviewed like code — not by a score whose meaning drifts.
- The marginal engine cost is small (one adapter, one capture carve-out, one hash, variant fan-out); the diff view is the main new surface, and it shares the comparison machinery with run-vs-baseline regression comparison.
- Nondeterministic completions make diffs noisy; the docs prescribe `temperature: 0` (and `seed` where honored) for regression-style prompt tests, and repeat sampling to expose spread is flagged P2 rather than assumed.
- Per-variant span identity means variants fold, chart, and compare cleanly, but variant renames break cross-run folding the same way step renames do — the existing parser warning covers both.
- Provider rate limits are already first-class via the `throttled` class (ADR 0006), which matters more for prompt steps than anywhere else.
