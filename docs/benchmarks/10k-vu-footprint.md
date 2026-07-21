---
type: Benchmark
title: "10k-VU generator footprint"
description: Sustained 10,000 concurrent VUs on a single node, with measured generator CPU headroom and per-VU memory overhead.
status: Baseline
timestamp: 2026-07-21
---
# 10k-VU generator footprint benchmark

Issue #21. Establishes the baseline the PRD NFR (§18, "generator CPU < [X]%") references and a regression harness re-runnable on any node.

## Harness

`internal/executor/bench_test.go` → `TestVUFootprintBenchmark`. Skipped under `-short`. It drives the pure-declarative fast path (a one-step HTTP flow) under the goroutine-per-VU pool at a configurable VU count, samples the engine's own self-metrics (goroutines, heap, process CPU via `getrusage`), and reports throughput, generator CPU, and per-VU memory.

The target stub answers after a fixed latency so VUs spend most of their time waiting on I/O — as they do against a real target. That is where *generator headroom* shows; an instant stub instead measures the raw request-rate ceiling and pins the generator at 100% CPU, which is not the question here.

```
FLOWBENCH_BENCH_VUS=10000 FLOWBENCH_BENCH_DUR=5s \
  go test -run TestVUFootprintBenchmark -v -timeout 180s ./internal/executor/
```

Env knobs: `FLOWBENCH_BENCH_VUS` (default 1000), `FLOWBENCH_BENCH_DUR` (default 2s), `FLOWBENCH_BENCH_LATENCY` (default 100ms). The node needs `ulimit -n` comfortably above the VU count (each VU holds a connection); the reference run had 1,048,576.

## Reference run

Apple-silicon laptop, 10 logical cores, macOS, Go 1.26; 10,000 VUs, 100 ms target latency, 5 s hold.

| Metric | 10,000 VUs | 2,000 VUs |
|---|---|---|
| Throughput | 47,667 iter/s | 17,689 iter/s |
| **Generator CPU** | **3.0 cores — 30% of 10 (70% headroom)** | 1.6 cores — 16% |
| Peak goroutines | 48,513 | 10,004 |
| Peak active VUs | 10,000 | 2,000 |
| **Per-VU heap** | **68.8 KiB** | 65.1 KiB |
| Peak heap | 672 MiB | 127 MiB |

**Sustained 10,000 concurrent VUs on a single node with the generator at 30% CPU — roughly 70% headroom** — confirming the generator is not the bottleneck against a latency-bound target. Per-VU memory holds at ~65–69 KiB from 2k to 10k VUs (its own HTTP session — cloned transport, connection buffers, cookie jar — plus a goroutine stack), so footprint scales linearly.

## Methodology notes

- **Per-VU heap** = (peak `HeapAlloc` − pre-run baseline) / VUs. It includes result data (latency samples, folded aggregates, sampled traces) accumulated over the run, so it is an upper bound on the pure per-VU overhead, not a floor.
- **Generator CPU** = Δ(process CPU seconds) / Δ(wall seconds) across the run's self-metric samples, in cores; the percentage is against `runtime.NumCPU()`.
- **Peak goroutines** ≈ VU goroutines + net/http client connection goroutines + the httptest server's accept/handler goroutines, hence ~4–5× the VU count with a local in-process target. Against a remote target only the VU and client-side goroutines are the generator's.

## Regression detection

Re-run the harness. It asserts goroutine-per-VU held (`peak goroutines ≥ VUs`) and per-VU heap stayed under a 100 KiB sanity budget. Compare the logged throughput / CPU / per-VU line against this table; a material CPU or memory increase at equal VUs and latency is a regression.
