"""Builds plain dicts matching internal/ir/ir.go's JSON shape exactly,
including its `omitempty` field-presence rules -- so a Python-compiled
scenario diffs cleanly against the YAML parser's output for the same flow.
"""

# The pool var name the `data=` shorthand binds rows to, matching
# internal/parser/parser.go's dataPoolVar constant.
_DATA_POOL_VAR = "user"


def build_scenario(name, data_source, steps, profile):
    flow = {"name": name, "steps": steps}

    data_pools = []
    if data_source:
        flow["data"] = _DATA_POOL_VAR
        # Format/distribution stay unset here, matching the YAML `data:`
        # shorthand (internal/parser/parser.go), which leaves them for
        # internal/data to infer at load time rather than at parse time.
        data_pools.append({"name": _DATA_POOL_VAR, "source": data_source})

    scenario = {"name": name, "flows": [flow], "profile": profile}
    if data_pools:
        scenario["data_pools"] = data_pools
    return scenario
