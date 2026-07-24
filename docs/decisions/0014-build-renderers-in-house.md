---
type: Decision
title: "ADR 0014: Build the flame-graph and waterfall renderers in-house"
description: The results server renders spans as server-side HTML from one shared stylesheet, rather than vendoring speedscope; a speedscope export is kept as an escape hatch.
status: Accepted
timestamp: 2026-07-24
---
# ADR 0014: Build the flame-graph and waterfall renderers in-house

Status: Accepted (resolves issue #33; PRD open question from section 10.5/10.7)

## Context

The results server needs two views over the same spans (ADR 0007): a flame graph answering "where does aggregate time go" and a waterfall answering "what exactly happened in one iteration". The PRD left open whether to adopt existing components — speedscope for the flame graph, a DevTools-panel-style component for the waterfall — or build both, ideally sharing a component library since both read the same data.

Speedscope (MIT) is the strongest adoption candidate. It ships as a self-contained `index.html` bundle, documents a JSON import format (`shared.frames` plus `profiles[]`, sampled or evented), and gives Time Order, Left Heavy and Sandwich views with keyboard navigation for free.

Two facts decided it:

1. **Speedscope covers one of the two views.** Its Time Order view is a chronological flame chart, not a waterfall: no per-span request/response payloads, no retry attempts as first-class rows, no parallel tracks for system-mode multi-flow scenarios. Adopting it leaves #36 to build anyway — and then the two views share nothing.
2. **Outcome is the axis that matters here, and it is ours.** `throttled` is a first-class outcome class (ADR 0006). Speedscope colours frames by name hash; it has no concept of an outcome, so the one distinction FlowBench exists to make would be invisible in the view.

Against that, adopting means vendoring a bundled JS SPA into a repo with exactly one Go dependency and no JavaScript toolchain — a posture change out of proportion to half a feature.

## Decision

Build both renderers in-house as server-side HTML in `internal/report`, sharing one stylesheet and one geometry vocabulary. Layout is computed in Go as percent-positioned frames and rows; the browser reflows on resize with no client-side rendering step and no JS build. Assets are `go:embed`-ed into the single binary, consistent with ADR 0004.

Colour follows two separate jobs. Span **kind** (network / assert-extract / backoff / step / flow) is categorical and takes validated slots that clear CVD and normal-vision separation on all pairs in both light and dark. Span **outcome** takes the reserved status palette, where `throttled` has its own colour and never borrows the failure red; every bar's row also carries a text chip, so outcome is never colour alone.

A speedscope export (`GET /runs/{id}/speedscope.json`, folded paths as a sampled profile weighted by self time) is kept as an escape hatch for anyone who wants the Sandwich view. It is an exporter, not a dependency.

## Consequences

- The two views cannot drift apart: one stylesheet, one kind classifier, one geometry helper.
- Interactions are split by whether they must survive without JavaScript. **Selection is a URL**: a frame, span, bucket or flow-run is addressed by query parameter and its detail panel is rendered server-side, so an inspected frame is a link someone can paste into a review and a stale link degrades to the plain view. **Live navigation is progressive enhancement**: the flame page ships a small dependency-free script (`assets/flame.js`, `go:embed`-ed) that gives the speedscope viewport — wheel to zoom, drag to pan, double-click to fit, an immediate tooltip, and span-path search — by recomputing each frame's left/width from a viewport rather than a CSS transform (a scaleX would stretch the labels, which is what makes a zoomed flame graph unreadable). With JavaScript off the server-rendered graph is still correct, just fixed at full extent; the URL-level `?zoom=` re-rooting remains as the no-JS path and as the shareable deep link. No bundler, no framework — the build stays `go build`.
- No npm, no bundler, no vendored SPA. The build stays `go build`.
- **Spans carry no kind of their own.** The renderer recovers it from name plus depth, which is unambiguous for every name the engine emits today, but extraction spans are named for the variable they bind and are identified only by position. A `Kind` field on `Span` would remove the inference; deferred until something needs it.
- **`FoldNode` has no outcome dimension**, so the flame graph cannot yet show throttled time as its own colour — folding collapses outcomes together. Splitting folded paths by outcome is the prerequisite for a throttle-aware flame graph; noted for #39.
- Building the prototype surfaced a live inconsistency: **a flow root's `Start` is an offset into the run, while the steps beneath it are stamped from the iteration's own anchor.** Folding never reads `Start`, so nothing had caught it. The waterfall pins the root to the left edge and places descendants by their iteration-relative offsets, and a regression test asserts a trace renders identically regardless of where in the run it occurred.
