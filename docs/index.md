okf_version: "0.1"

# FlowBench documentation

Entry point for the FlowBench knowledge bundle. Read this index first; open documents as needed.

## Product

* [PRD](prd.md) - Full product requirements document (v0.5 DRAFT); the source of truth for v1 scope.
* [CONTEXT.md](CONTEXT.md) - Ubiquitous-language glossary: Endpoint, Flow, Step, Profile, Scenario, Span, Run, Agent, and friends.

## Decisions

* [decisions/](decisions/) - Architecture decision records, one per major decision already made in the PRD:
  * [0001 Go engine with goroutine-per-VU concurrency](decisions/0001-go-engine-goroutine-per-vu.md) - Why Go and goroutine-per-VU for the 10k-VU target.
  * [0002 One canonical flow IR, two authoring surfaces](decisions/0002-one-ir-two-authoring-surfaces.md) - YAML and Python both compile to one IR; the executor accepts only the IR.
  * [0003 Four test categories as execution profiles](decisions/0003-test-categories-as-execution-profiles.md) - Integration/system/load/soak are profiles over the same flow.
  * [0004 Tooling packages, not a platform](decisions/0004-packages-not-platform.md) - Local-first binaries and packages; no hosted anything in v1.
  * [0005 No bespoke secrets mechanism](decisions/0005-no-bespoke-secrets.md) - Env vars and existing vaults; the engine only owns redaction.
  * [0006 Rate limiting as a first-class signal](decisions/0006-rate-limiting-first-class-signal.md) - `throttled` is its own outcome class with mode-aware semantics.
  * [0007 Span model as single source of truth](decisions/0007-span-model-single-source-of-truth.md) - One span model feeds both flame graphs and the waterfall view; two storage tiers.
  * [0008 Python logic steps via bridged worker pool](decisions/0008-python-bridge-worker-pool.md) - Pure-declarative fast path vs bridged Python path, honestly reported.
  * [0009 Prompt testing as diff-first prompt steps](decisions/0009-prompt-testing-diff-first.md) - LLM prompts are a step type in the same flows; completions always captured; diff, don't score.

## Planning

* [planning/milestones.md](planning/milestones.md) - M1–M4 milestone plan, mirrored as GitHub milestones and issues.

## History

* [log.md](log.md) - Decision/work log, newest first.
