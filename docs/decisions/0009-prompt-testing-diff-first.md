---
type: Decision
title: "ADR 0009: Prompt testing by observation — diff-first, never owning the LLM call"
description: The flow's own SDK code makes LLM calls; FlowBench wraps them to capture, hash, pace, and diff — no provider adapters, no model-behavior surface, no scoring.
status: Accepted
timestamp: 2026-07-12
---
# ADR 0009: Prompt testing by observation — diff-first, never owning the LLM call

Status: Accepted (per PRD v0.6, section 10.9)

## Context

Teams increasingly ship LLM-backed features, and prompt iteration happens by eyeballing playground outputs: nothing captures which prompt produced which completion, nothing diffs outputs across prompt versions, and nothing tests a prompt inside the real flow it belongs to (fetch context → prompt → parse → act). At the same time, teams already have SDKs, frameworks, model settings, and prompt templates — an earlier draft of this decision gave FlowBench its own `prompt` step type with an LLM provider adapter, which would have put FlowBench in the business of setting model behavior and chasing provider APIs. That is the wrong layer, and standalone eval harnesses (promptfoo, LangSmith, Braintrust) already show where that road leads: prompt-centric scoring platforms detached from the API-flow world FlowBench models.

## Decision

**Observe, don't own.** FlowBench never makes the LLM call, templates a prompt, or sets model parameters. The flow's own code calls whatever SDK or framework the team already uses inside a `logic` step, and the Python SDK's **prompt-observation API** wraps that call: `with ctx.prompt("classify") as p:` … `p.record(prompt, completion, usage=...)`. The wrapper emits a `prompt` span; the SDK's internal HTTP resolves as child spans via the existing auto-instrumentation, so the provider round-trip stays visible.

Four rules on top of the wrapper:

1. **Always capture.** Observations keep the recorded prompt and completion on every iteration, overriding the failures-plus-sample capture policy — a diff needs both sides. Size caps and redaction still apply.
2. **Prompt identity.** Each observation is hashed — an author-supplied `template=` when passed (stable across iterations whose variable values differ), else the recorded prompt content — so the diff view distinguishes "the prompt changed" from "the output changed under the same prompt."
3. **Diff, don't score.** The analysis primitive is the prompt diff view — variant vs variant within a run, same observation vs a baseline run; text diff by default, structural for JSON — extending regression comparison. Variants are **labels** (`variant="concise"` → span identity `classify@concise`), not engine machinery: the author's code decides what varies.
4. **Pace, don't get throttled.** An observation may declare a `timeout` and a `pace` (client-side rate ceiling with optional burst allowance), coordinated by the engine across VUs and Python workers, so calls repeated N times don't trip the provider's rate limit in the first place; throttles that still occur classify as `throttled` (ADR 0006).

LLM provider adapters, model configuration, LLM-as-judge, semantic-similarity scoring, eval-dataset management, and prompt registries are explicit non-goals; prompts live in the team's code in git.

## Consequences

- "How are my prompts doing" is answered by inspecting actual differences, reviewed like code — not by a score whose meaning drifts, and not through a FlowBench-owned model surface that would drift from the SDKs teams actually use.
- Any provider, framework, or future SDK works on day one — the observation layer is provider-blind, so there is no adapter matrix to maintain.
- Prompt testing is Python-surface-only in v1 (YAML cannot call SDKs); whether a YAML `call` step can opt in as an observed prompt is an open question in the PRD.
- The marginal engine cost is small (capture carve-out, hash, span kind, pace/timeout coordination); the diff view is the main new surface and shares machinery with run-vs-baseline regression comparison. Pace-guard coordination across the worker pool is the one new engine-side mechanism, and it rides the bridge design (ADR 0008).
- Capture depends on authors calling `p.record(...)` honestly; an unwrapped LLM call is invisible to diffing. The cookbook makes the wrapper the path of least resistance, and auto-instrumented HTTP spans still show the call happened.
- Nondeterministic completions make diffs noisy; the docs prescribe pinning model parameters in the author's own SDK call for regression-style tests, and spread rendering is flagged P2 rather than assumed.
- Variant renames break cross-run folding the same way step renames do — the existing parser warning covers both.
