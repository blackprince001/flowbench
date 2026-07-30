# Prompt testing

FlowBench never makes the LLM call, templates a prompt, or sets a model parameter — your code calls whatever SDK you already use, and the Python SDK's prompt-observation API wraps that call so the exchange is recorded ([ADR 0009](../decisions/0009-prompt-testing-diff-first.md)). The analysis primitive is a diff, not a score: a prompt change is reviewed by diffing captured completions against a baseline, the way a code change is reviewed. These patterns are Python-surface-only — observation lives where your code runs, so the flow executes as `python file.py`; `flowbench run file.py` refuses to compile an observed prompt rather than silently dropping it.

## The wrapper

From [`sdk-python/README.md`](../../sdk-python/README.md):

```python
client = OpenAI(api_key=flowbench.secret(os.environ["OPENAI_API_KEY"]))

@flow.step
def classify(ctx):
    messages = [{"role": "system", "content": SYSTEM},
                {"role": "user", "content": str(ctx.user["ticket"])}]
    with ctx.prompt("classify", template=SYSTEM, pace="20/m", burst=3) as p:
        reply = client.chat.completions.create(model="gpt-4o-mini", messages=messages)
        p.record(messages, reply.choices[0].message.content, usage=reply.usage)
    expect(p.completion).to_contain("refund")
```

The pair is always captured, every iteration, whatever the outcome — a diff needs both sides. Redaction and the size cap still apply. `template=` is the prompt's identity: it is hashed, so the diff view can tell "the prompt changed" from "the output changed under the same prompt"; without one the recorded content is hashed instead, which moves whenever a substituted value does. Assertions on the completion are ordinary `expect(...)` calls — no special machinery.

## Pinned-params regression

Nondeterministic completions make diffs noisy, so for regression-style tests, pin the model parameters in your own SDK call — FlowBench has no surface for them by design:

```python
        reply = client.chat.completions.create(
            model="gpt-4o-mini", messages=messages,
            temperature=0, seed=7,          # pinned in YOUR call, not by FlowBench
        )
```

Run in integration mode over your fixture rows, once per prompt version:

```bash
python flows/classify.py            # writes a run to runs/
# ... edit the prompt ...
python flows/classify.py            # a second run, same observation names
flowbench serve --store runs
```

Open the newer run's **Prompts** tab and pick the earlier run as the baseline. With parameters pinned, the diff is meaningful run over run: what changed is what your edit changed. Because the `template=` hash differs between the two runs, the view shows the prompt's own diff alongside the completion diff — the edit and its consequence, side by side.

## Variants: compare within one run

When you want two prompt versions in the same run, label them. What varies is decided entirely by your code — a different system prompt, template, model, or chain — and `variant="concise"` gives the observation its own structural span identity (`classify@concise`), so folding, metrics, and diffs stay per-variant:

```python
    with ctx.prompt("classify", template=SYSTEM_CONCISE, variant="concise") as p:
        ...
```

The **Prompts** tab then diffs variant against variant within the run — the completion for `classify@concise` beside the one for `classify@verbose`, per fixture row. Because the label is an identity, renaming one splits flame graphs into a before and an after; a clean run warns on stderr when a name earlier runs of the same scenario recorded is no longer there.

## Pace guards under provider rate limits

An integration run over 500 fixture rows is 500 provider calls in quick succession. `pace="20/m"` is a client-side ceiling keyed to the observation name, so repetition doesn't trip the provider's rate limit in the first place; `burst=3` lets the first three calls go unspaced. A wait shows up as its own `pace` span beside the observation, so paced time is visible, not vanished. Throttles that still occur classify as `throttled`, not `failed` — a provider `429` reads in the run exactly as any other rate limit does ([ADR 0006](../decisions/0006-rate-limiting-first-class-signal.md)).

Two more mechanics worth knowing from the [SDK README](../../sdk-python/README.md): `timeout=` is measured, not enforced — the call is your SDK's, so pass the same bound to your SDK's own timeout to actually cut it short — and the provider's HTTP resolves as child spans of the observation, so the round trip is never opaque in the flame graph.

## Reviewing in the prompt diff view

The **Prompts** tab on a run page is the review surface: side-by-side and inline diffs of completions, variant vs variant within a run, or the same observation (and same variant) against a baseline run. Text diff by default, structural diff when both completions are JSON. Token usage passed to `record(usage=...)` surfaces in the per-observation table. What a difference *means* is deliberately left to you — LLM-as-judge, semantic scoring, and eval datasets are explicit non-goals; the toolkit reports differences honestly and you review them like code.

## Why it works

Because FlowBench observes rather than owns the call, any provider, framework, or future SDK works on day one, and the prompt lives where it belongs — in your code, in git, tested inside the real flow it belongs to (fetch context → prompt → parse → act). The capture carve-out, the identity hash, and the pace guard are thin layers over machinery that already exists: spans, redaction, and run-vs-baseline comparison. See [Prompt observation](../guide/prompt-observation.md) and the [Python SDK guide](../guide/python-sdk.md) for the full surface.
