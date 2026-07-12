# Conventional test-repo layout

This directory is the conventional layout FlowBench imposes (but does not require —
custom layouts are tolerated, PRD 10.8) for a repository of flows. It doubles as a
worked example: the files here mirror the PRD's `authenticated_checkout` sample and
will be the first inputs the parser targets.

```
tests/
  endpoints/        # reusable endpoint catalog (declare once, reference across flows)
  flows/            # *.flow.yaml and *.py flow files — the unit of authorship
  scenarios/        # flow + profile + target bindings — the runnable unit
  fixtures/         # data pools (csv/json); also the sanctioned seeding lever
  targets/          # local.yaml, dev.yaml, staging.yaml — never credentials
runs/               # run store (git-ignored); folded flame data + raw trace
                    # trees; `flowbench serve` reads from here
```

Two rules worth restating from the PRD:

- **No credentials in any of these files.** YAML resolves `{{ env.VAR_NAME }}`
  against the process environment at run time; Python reads secrets the way any
  script does. Everything here is safe to commit as-is (ADR 0005).
- **Target configs are the safety gate.** A run is refused if it would call hosts
  outside the target's declared base URLs, and the config's VU/RPS ceilings are
  enforced by the planner (PRD section 15).
