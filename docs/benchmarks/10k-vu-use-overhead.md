---
type: Benchmark
title: "10k-VU `use` step overhead"
description: The generator cost of wrapping a call in a `use` step against the same call run flat, at 10,000 concurrent VUs.
status: Baseline
timestamp: 2026-10-01
---
# 10k-VU `use` step overhead benchmark

Issue #105. F1 of its reliability checklist: a `use` step must not recompile or
copy the child flow per iteration. Reading the engine first — `ir.UseSpec.Flow`
is a single `*Flow` the parser compiles once, and `Runner.runUse` only builds a
lightweight child `Scope` per iteration — the regression F1 describes does not
exist structurally. This benchmark is the number that backs that up.

## Harness

`internal/executor/bench_test.go` → `TestVUFootprintBenchmark` (flat) and
`TestVUFootprintBenchmarkWithUse` (the identical call wrapped one level inside
a `use` step), both built on the shared `runFootprintBenchmark` helper added
for this comparison so the two runs measure the exact same things the same
way. Same stub, same env knobs as the #21 harness
([10k-vu-footprint.md](10k-vu-footprint.md)).

```
FLOWBENCH_BENCH_VUS=10000 FLOWBENCH_BENCH_DUR=5s \
  go test -run TestVUFootprintBenchmark -v -timeout 180s ./internal/executor/
FLOWBENCH_BENCH_VUS=10000 FLOWBENCH_BENCH_DUR=5s \
  go test -run TestVUFootprintBenchmarkWithUse -v -timeout 180s ./internal/executor/
```

## Reference run

Linux container, 8 logical cores, Go toolchain current as of this branch;
10,000 VUs, 100 ms target latency, 5 s hold. Both runs on the same machine,
back to back, so the comparison is apples to apples even though the absolute
numbers differ from the #21 baseline doc's Apple-silicon reference.

| Metric | Flat call | Wrapped in `use` | Delta |
|---|---|---|---|
| Throughput | 19,243 iter/s | 19,452 iter/s | +1.1% (noise) |
| **Generator CPU** | 7.06 cores — 88% of 8 | 7.09 cores — 89% of 8 | +0.03 cores (~0.4%) |
| Peak goroutines | 44,692 | 48,045 | +3,353 |
| **Per-VU heap** | 66.7 KiB | 71.9 KiB | **+5.2 KiB (~7.8%)** |

**The `use` step's overhead is small and matches the structural reason for it:**
one extra `Scope` (the child's isolated `inputs`) and one extra `span.Span` per
iteration, not a recompiled or copied child flow. ~5 KiB of per-VU heap and
under half a percent of generator CPU is the stated small regression the
acceptance allows for proving this at 10k VUs, not something to chase further.

## Methodology notes

Same as [10k-vu-footprint.md](10k-vu-footprint.md): per-VU heap is peak
`HeapAlloc` minus pre-run baseline, divided by VU count, so it is an upper
bound (it includes accumulated result data) rather than a floor; generator CPU
is Δ(process CPU seconds)/Δ(wall seconds) in cores.

## Regression detection

Re-run both harnesses on the same machine back to back and diff the two
per-VU-heap and generator-CPU lines against this table. A gap that grows well
past ~8% per-VU heap or shows up as material CPU (not noise-level, as
throughput was here) means a `use` step started doing real per-iteration work
it should not be doing — check `Runner.runUse` for anything that stopped being
a shared pointer.
