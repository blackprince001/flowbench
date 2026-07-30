# Targets and safety rails

A load test is a weapon pointed at something, so where it may point and how hard it may fire are configuration a run is gated on — not conventions a careful author remembers. The target config owns both, and every check happens before any load is generated.

```yaml
# targets/staging.yaml
name: staging
base_urls:
  - https://staging.shop.internal
max_vus: 500
max_rps: 1000
request_timeout: 5s
agent_addr: staging.shop.internal:9090
disallowed_modes: [stress, soak]
```

Target configs never carry credentials — auth material comes from the environment through `{{ env.* }}` ([authentication](auth.md)).

## The allow-list

`base_urls` is both the base URL requests resolve against and the **host allow-list**: a step whose URL resolves outside it — including a templated URL, an OAuth2 `token_url`, or a WebSocket handshake — is refused. The flow says what to do; the target says what it is allowed to touch. Unknown keys in a target file fail loudly rather than being ignored, for the same reason.

## Ceilings

`max_vus` caps the schedule's peak VU count; `max_rps` caps a declared `arrival_cap`. A scenario that would exceed either is refused pre-run with exit `2` — finding out at plan time beats finding out 10,000 goroutines in. `disallowed_modes` refuses whole profiles against a target: a shared staging environment can permit load but never stress or soak.

`request_timeout` bounds every single call against this target (default 30s), which is also what turns a hung endpoint into a clean, classified timeout failure instead of a stuck VU.

## Resolving targets

`flowbench run` takes `--target` as either a name resolved in `--targets-dir` (default `targets/`) or a direct path to a file:

```bash
flowbench run flow.yaml --target staging          # targets/staging.yaml
flowbench run flow.yaml --target ./local.yaml     # explicit path
```

With no target at all, `--target local` falls back to `http://localhost:8080` — the local dev loop needs no ceremony. `flowbench target <name>` prints the resolved config as JSON; it exists both for humans and as the single source of truth the [Python SDK](python-sdk.md) shells out to.

## Attaching an agent

`agent_addr` names a [`flowbench-agent`](agent.md) on the target host. When set, every load-family run polls it once a second and saves the target's CPU/memory series beside the run — the data that separates target saturation from generator saturation, and the input the [stress knee classification](profiles-and-thresholds.md) needs. Polling is fail-open: an unreachable or dying agent never blocks or fails a run.

## The kill switch

Every run can be stopped cleanly: Ctrl-C cancels the run context and unwinds every VU, and the partial run is still summarized and saved — a run that had to be killed is exactly the run worth reading. Under `--watch`, the live view adds a server-side abort button with the same semantics ([results server](results-server.md)).
