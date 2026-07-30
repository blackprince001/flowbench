# Prompt observation

FlowBench tests prompts by watching them, not by making them: your code calls whatever LLM SDK it already uses, `ctx.prompt(...)` wraps that call so the exchange is recorded, and a prompt change is reviewed by diffing captured completions against a baseline — like code, not like a score. This page covers the observation API; it runs only on the [Python-driven path](python-sdk.md), because that is where your code runs.

## Observe, don't own

The design rule (ADR 0009) is that FlowBench never makes the LLM call, never templates a prompt, and never sets a model parameter. There are no provider adapters, no model configuration surface, and no LLM-as-judge in v1 — the analysis primitive is the **prompt diff**, and scoring what a difference *means* is deliberately not FlowBench's job. The payoff is that any provider, framework, or in-house client works on day one, and your prompts stay where they belong: in your code, in git.

```python
import os
import flowbench
from openai import OpenAI
from flowbench import Flow, Profile, expect

client = OpenAI(api_key=flowbench.secret(os.environ["OPENAI_API_KEY"]))
flow = Flow("triage", data="fixtures/tickets.csv")

SYSTEM = "Classify the support ticket. Answer with one word."

@flow.step
def classify(ctx):
    messages = [{"role": "system", "content": SYSTEM},
                {"role": "user", "content": str(ctx.user["ticket"])}]
    with ctx.prompt("classify", template=SYSTEM, pace="20/m", burst=3) as p:
        reply = client.chat.completions.create(
            model="gpt-4o-mini", messages=messages, temperature=0, seed=7,
        )
        p.record(messages, reply.choices[0].message.content, usage=reply.usage)
    expect(p.completion).to_contain("refund")

if __name__ == "__main__":
    flow.run(Profile(mode="integration"))
```

The wrapper emits a `prompt` span, and the HTTP round trip your SDK makes resolves as child spans beneath it via [auto-instrumentation](python-sdk.md#httpx-auto-instrumentation), so the provider call is never opaque in the flame graph. Assertions on the completion are ordinary `expect(...)` calls — no special machinery.

Run this with `python triage.py`. Compiling it with `flowbench run triage.py` is refused with a `FlowCompileError` rather than silently dropped: the Go engine runs no Python at execution time, so there is nothing there to wrap, and a flow whose observation quietly vanished would run at VU scale recording nothing.

## What the wrapper guarantees

**The pair is always captured.** The prompt and completion are kept on every iteration, whatever the outcome — a diff needs both sides to exist. This overrides the usual failures-plus-sample capture policy; size caps and redaction still apply, so a key flagged with `flowbench.secret(...)` never reaches a stored artifact.

**`template=` is the prompt's identity.** The template is hashed, so the diff view can tell "the prompt changed" from "the output changed under the same prompt." Without one, the recorded content is hashed instead — which moves whenever a substituted value does, so pass the template whenever you have one.

**Record exactly once.** One wrapped call is one observation. Calling `p.record(...)` twice raises; a block that never records raises too, because an observation nobody records is invisible to the diff view. A chain of prompts needs nothing special: each wrapped call is its own observation, and values flow between them as ordinary Python.

**Usage is normalized.** `record(usage=...)` accepts the usage object or mapping your SDK returned — OpenAI's `prompt_tokens`/`completion_tokens` and Anthropic's `input_tokens`/`output_tokens` both reduce to one prompt/completion/total vocabulary, so the per-observation table is not provider-specific. Pass whatever your SDK gave you; a value no token count can be read out of raises rather than being dropped.

**A recorded observation is a real call.** Every step must put a request on the wire, and a step whose only traffic is the observed provider exchange satisfies that rule — including when the provider is reached through a client auto-instrumentation cannot see, like a `requests`-based SDK. The prompt and completion you get back (`p.prompt`, `p.completion`) are real strings your code can parse and chain into later steps.

## Variants are labels

`variant="concise"` names which prompt version your code used; what actually varies — a different system prompt, model, chain — is decided entirely by your code. The label gives the observation its own structural span identity, `classify@concise`, so folding, metrics, and diffs stay per-variant. Two variants in one run is how you compare prompt versions side by side:

```python
VERBOSE = "Classify the ticket and explain your reasoning."
CONCISE = "Classify the ticket. One word."

@flow.step
def compare(ctx):
    ticket = str(ctx.user["ticket"])
    with ctx.prompt("classify", template=VERBOSE, variant="verbose", pace="20/m") as a:
        reply = ask(VERBOSE, ticket)
        a.record(ticket, reply)
    with ctx.prompt("classify", template=CONCISE, variant="concise", pace="20/m") as b:
        reply = ask(CONCISE, ticket)
        b.record(ticket, reply)
```

Each `with` block is one observation; take them in turn, as here, rather than opening two at once. The run then carries `classify@verbose` and `classify@concise` as separate identities, and the diff view compares their completions row by row.

Because it is an identity, renaming one splits a flame graph into a before and an after. A clean run therefore warns on stderr when an identity earlier runs of the same scenario recorded is no longer there — if that was a rename, cross-run folding for it is broken and the warning is your chance to notice. A failed run does not warn: it stopped short of identities it would otherwise have reached.

## Pace guards and timeouts

`pace="20/m"` is a client-side ceiling — a token bucket, module-level and keyed by the observation *name*, so a run over 500 fixture rows does not trip the provider's rate limit in the first place. Keying by name rather than span identity means two variants of one prompt share the ceiling, which is right: they hit the same provider. `burst=3` lets the first three calls run unspaced. A wait shows up as its own `pace` span beside the observation, like a retry's backoff span, so the waiting is visible without inflating the observation's latency.

`timeout=` is measured, not enforced: the call belongs to your SDK and FlowBench has no handle to cancel it, so an overrun fails the observation after the call returns. Pass the same bound to your SDK's own timeout argument to actually cut it short.

A provider `429` that gets through anyway classifies the observation as `throttled`, not `failed` — a rate limiter engaging is a different fact from your prompt being wrong, and the run reports them separately.

One structural limit: two observations open concurrently are not supported — gather a batch of calls inside one observation (async clients are fine, and each call lands beneath it), or take the observations one at a time.

## Make the diff meaningful

FlowBench reports differences honestly rather than smoothing them, so a nondeterministic model makes a noisy diff. For regression-style prompt tests, pin the model parameters in your own SDK call — `temperature=0` and a fixed `seed`, as in the example above — and run in `integration` mode. Run over run, the diff then shows what your prompt change did, not what sampling did. The wrapper cannot pin anything for you: model parameters are your SDK call's, by design.

The workflow is two runs and a comparison:

```bash
python triage.py            # baseline run, written to the run store
# ...edit the prompt...
python triage.py            # candidate run
flowbench serve             # open the prompt diff view and compare the two
```

Because the prompt's identity is hashed, the diff view knows whether the completions differ under the same prompt or because the prompt itself changed — and when the hash moved, it shows the prompt's own diff alongside the output diff.

From there: the [results server](results-server.md) documents the prompt diff view — variant vs variant within a run, or the same observation and variant against a baseline run, text diff by default and structural when both completions are JSON. The cookbook's [pinned-params regression recipe](../cookbook/prompt-testing.md) turns this into a repeatable check.
