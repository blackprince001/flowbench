---
type: Plan
title: "Flowbench landing page"
description: Product narrative, visual system, page structure, implementation architecture, and staged delivery plan for the Flowbench marketing site.
status: Draft
timestamp: 2026-07-30
---

# Flowbench landing page

## Destination

Build a polished, responsive landing page for Flowbench that borrows the information
rhythm and interaction quality of [Visitors](https://visitors.now/) while remaining
recognizably Flowbench:

- the same clear progression from promise → product proof → features → workflow → decision
  support → call to action;
- a floating pill navigation, compact labels, generous white space, rounded product stages,
  and restrained motion;
- original Flowbench copy, product visuals, palette, iconography, and interaction details;
- no copied Visitors assets, logos, screenshots, or product claims.

The page should feel like the public front door to the repository, not a disconnected
template placed in front of it.

## Working assumptions

These are the current defaults and should be confirmed before implementation:

1. **Primary audience:** backend, platform, SRE, and performance engineers who currently
   stitch together functional tests, load tools, and result dashboards.
2. **Primary conversion:** open the GitHub repository — read the code, install from a release,
   star the project.
3. **Secondary conversion:** open the hosted documentation.
4. **Release posture:** market the existing `v0.1.0` release honestly; do not imply a hosted
   service, team accounts, or cloud storage.
5. **Hosting:** the site will be a static build, with GitHub Pages as the default deployment
   target unless a custom host is chosen.

### Confirmed decisions

**View on GitHub is the primary CTA. View docs is the secondary CTA.** There is no
“Get started” destination — the product has no onboarding surface to send people to, and a
button that promises one would break on the first click.

| Action | Label | Destination |
| --- | --- | --- |
| Primary | `View on GitHub` | <https://github.com/blackprince001/flowbench> |
| Secondary | `View docs` | <https://blackprince001.gitbook.io/flowbench-documentation> |

Both destinations are external and live. This shapes the page: it is an ad page for a
repository, so the repository is the conversion, and every section either earns that click or
hands the visitor to the docs.

## Product story

### Positioning

Flowbench is the scripting-first toolkit that lets a team author a flow once, apply every
execution profile to it, and inspect exactly where time and failures went.

### Message hierarchy

1. **One authored flow** — YAML or Python, with chaining, extraction, and assertions.
2. **Every testing profile** — integration, system, load/stress, and soak use the same model.
3. **Honest outcomes** — `throttled` is not silently folded into `failed`.
4. **Explanatory results** — flame graph, waterfall, failure drill-down, and live view share
   one span model.
5. **Local-first ownership** — one binary, plain run directories, no hosted control plane.
6. **Measured scale** — 10,000 sustained VUs with roughly 70% generator CPU headroom on the
   documented reference machine.

### Recommended hero copy

**Release badge**

`v0.1.0 · First public release`

**Headline**

`One flow. Every test profile.`

**Supporting line**

`Author API journeys once in YAML or Python. Run them from integration to stress and soak,
then see exactly where the time—and the failure—went.`

**Actions**

- Primary: `View on GitHub` — solid dark pill, with the star count when it can be fetched at
  build time rather than at runtime.
- Secondary: `View docs` — outlined pill with an external-link affordance.

The hero should not attempt to explain the whole architecture. Its job is to make the reuse
promise and the inspection promise legible in one glance.

## Reference translation

The layout is a close structural translation, not a reskin.

| Visitors pattern | Flowbench translation | Purpose |
| --- | --- | --- |
| Floating dark pill navigation | Flowbench mark, Product, Workflow, Results, Docs, GitHub | A compact persistent wayfinding surface |
| Announcement pill | `v0.1.0 · First public release` | Signals that the project is real and available |
| Short centered value proposition | “One flow. Every test profile.” | Makes reuse the memorable idea |
| Trial and demo CTAs | View on GitHub and View docs | Matches an open-source conversion path |
| Customer logo strip | Measured proof strip | Avoids invented social proof |
| Tabbed dashboard showcase | Dashboard, Flame graph, Waterfall, Live run | Lets the real product explain itself |
| Three short trust statements | 10k VUs, local-first, YAML + Python | Compresses technical confidence |
| Interactive feature cards | Reuse, outcome model, target telemetry, prompt diffs | Shows differentiation rather than a feature dump |
| Three-step “How it works” | Author → Run → Inspect | Gives the page a simple mental model |
| Competitor comparison | Flowbench vs a stitched testing stack | Supports the switch without fragile competitor claims |
| Pricing card | Open-source install card | Converts attention into a command a developer can run |
| FAQ accordion | Scope, safety, protocols, storage, CI, Python | Removes adoption objections |
| Large final CTA and logo footer | “Stop rewriting the same journey” plus stacked-bar mark | Finishes on the core promise |

## Page architecture

### 1. Floating header

- Desktop: a centered, dark, 520–600 px pill, 52 px tall.
- Mobile: inset 16 px from the viewport with logo, GitHub action, and menu button.
- The header remains visually light: no full-width chrome and no oversized brand wordmark.
- The pill CTA is `GitHub`; `Docs` sits beside the section anchors as a text link with an
  external-link affordance.
- Anchor links use native smooth scrolling only when reduced motion is not requested.

### 2. Hero

- Starts below the floating header with approximately 150 px of desktop top breathing room.
- Maximum copy width: 720 px; headline measure: roughly 11–14 words over two lines at most.
- Two pill actions, stacked and full-width on narrow mobile.
- A faint field of flow paths sits behind the hero, but never crosses the text at readable
  contrast.
- The announcement, headline, supporting line, and actions enter once in a short structural
  stagger.

### 3. Proof rail

Use verifiable product facts instead of logos:

- `10,000` sustained concurrent VUs
- `~70%` generator CPU headroom in the reference run
- `68.8 KiB` measured heap per VU at 10k
- `1 flow` shared by YAML and Python

The benchmark caveat belongs in an accessible note or link, not hidden in tiny legal copy.

### 4. Product stage

This is the visual anchor of the page.

- A full-bleed, saturated blue-to-indigo field — the one loud band on a near-black page — holds
  the product window.
- The tab switcher hangs from the field’s top edge in the page’s own colour, so it reads as the
  page cut into the field: `Dashboard`, `Flame graph`, `Waterfall`, `Outcomes`. Without
  JavaScript the switcher is hidden and the four views stack, each keeping a visible caption.
- **Every view is a real screenshot of the results server, not a drawing of it.**
  `website/scripts/capture.sh` serves `runs/load-local`, drives a headless Chromium over the
  four views, and writes them to `website/src/assets/product/`. Re-run it when the report UI
  changes; never hand-build a mockup of a UI that already exists and already has data.
- The captured run is the one that makes the argument: `stress.flow.yaml`, 2,000 flow-runs,
  0.45% failed, **49.20% throttled**, thresholds still `PASSED`. The Outcomes view filtered to
  throttled — 984 amber squares — is the throttling story told without a word of copy.
- Each view is deep-linked to a selection (`?at=`, `?frame=`, `?span=`, `?outcome=`) so the
  detail rail carries content instead of its empty state.
- On mobile the tabs become a compact select-like control and the product window shows one
  simplified vertical composition.

The tab views tell a sequence:

1. Dashboard: what happened?
2. Flame graph: where did the time go?
3. Waterfall: what happened in this iteration?
4. Outcomes: how much of it was throttling?

### 5. Fast facts

Three compact statements immediately below the product stage:

1. **One canonical flow.** Author in YAML or Python; both resolve to the same model.
2. **One measured engine.** Go, goroutine-per-VU, with explicit safety ceilings.
3. **One span vocabulary.** Dashboard, flame graph, and waterfall agree because they share
   the same source.

### 6. Feature system

Every long-page section opens the same way — centred eyebrow pill, centred heading, one muted
line — because that repetition is what makes the page scan as a single document rather than a
stack of blocks. `.eyebrow` and `.section-head` in `global.css` own it; no section rolls its own.

The features themselves are a 2×2 bento of tall cards, then a row of two short strips. Each
bento card is the same five-part shape, in this order:

1. an icon in a tinted rounded chip, carrying the card's accent;
2. a coloured category line — the accent is per-card (blue, amber, orange, green);
3. the claim as a large headline, two lines, capped near 20ch;
4. three checkable facts, each with a small accent tick;
5. a `Learn more` pill, then a visual **pinned to the bottom of the card** so the four cards
   share a baseline however deep their headlines run.

The bento is asymmetric: one full-width lead card (copy beside the interactive profile
switcher), then two rows of two. Card visuals are app windows cut into the card's bottom edge —
`border-radius` on top only, pinned flush with an auto margin — so they ground the card instead
of floating in it.

Where a card's subject is a screen that exists, the visual **is a capture of that screen**:
`shot.mjs --selector` clips one results-server card to its border box, so the Honest-outcomes
card shows the real outcomes strip and the span card shows the real flame workspace
(`card-outcomes.png`, `card-flame.png`, regenerated by `capture.sh`). Only subjects with no
screen — the run directory, the knee classification — get purpose-built markup, from real
values. Checklists, repeated "Learn more" pills, and four-identical-cards symmetry are the
slop patterns this section exists to avoid.

One lead feature with a live demonstration, then three cards. Every snippet is lifted from
`examples/load-local/` or the quickstart — nothing on this page is invented syntax.

#### A. Change the profile, not the journey (lead)

The real `stress.flow.yaml` steps sit above a rule that never moves; chips swap only the
`profile:` block between integration, load, stress, and soak. It is the page’s opening claim
made literal rather than restated. Same switcher contract as the product stage: without
JavaScript every profile is visible and labelled.

#### B. Keep throttling honest

Two adjacent meters — `error_rate 0.10%` against `throttle_rate 40.55%` — under the line that
makes the distinction concrete: a working rate limiter is not a broken target.

#### C. Separate target pressure from generator pressure

A `target.yaml` with `agent_addr` set, and the two knee classifications the collector actually
emits: `throttled` when the throttle rate climbs while target resources stay flat, `degraded`
when they do not.

#### D. The summary tells the truth before you open anything

The real terminal summary — rates, percentiles, and each threshold with the number that
decided it. Names the CI consequence: a breach exits non-zero.

Code is highlighted with Astro’s built-in Shiki under a small custom theme
(`src/lib/code-theme.ts`) built from the report’s palette, so a snippet on the page and a value
in a run agree. Ligatures are off everywhere — a reader should see the `==` they would type.

Prompt-diff review is deliberately **not** a landing-page feature in the first release. It is
real, but leading with it blurs the toolkit’s API-testing identity, and four differentiators is
already the limit of what a visitor will hold.

### 7. Workflow

Three cards under the same centred header, but each window is the program the step actually
happens in — the chrome does the explaining:

1. **Author** — an editor: monochrome window dots and a `stress.flow.yaml` tab over the YAML.
2. **Run** — a terminal: the invocation, the truthful summary, and `$ echo $?` → `0`.
3. **Inspect** — a browser: a URL pill reading `127.0.0.1:7580` over the real dashboard
   capture, cropped to the run head and summary tiles.

Circled chevrons sit in the gaps between cards (rotating downward when the grid stacks), so
the row reads as a pipeline — each step hands its artifact to the next — rather than three
tiles. Fixed pane heights keep the three captions on one line. Static markup throughout; the
sequence needs no JavaScript at all.

### 8. Decision table

Avoid claims about named competitors until each one has been verified and sourced. The first
version compares Flowbench with the common “stitched stack”:

Layout: `table-layout: fixed` with explicit column tracks (42 / 31 / 27), and a highlight rail
absolutely positioned at exactly those same numbers — so the Flowbench column sits inside a
rounded blue outline that can never drift from the cells it wraps. Row labels carry small icon
chips; Flowbench cells open with a blue check dot, stitched-stack cells with a muted dash dot,
each followed by a short phrase rather than a bare tick — the phrase is the claim. Below 860px
the whole composition scrolls horizontally rather than stacking into unreadable blocks.

| Capability | Flowbench | Stitched stack |
| --- | --- | --- |
| One journey across every profile | The same file, integration through soak | Duplicated per tool, then drifts |
| Throttling as its own outcome | `throttled` never folds into `failed` | 429s land in the error bucket |
| Functional and load share a result model | One collector, one set of views | Two result formats to reconcile |
| Aggregate cost and one trace | Flame graph and waterfall, same spans | Instrumentation glue, if at all |
| Target resources aligned to the run | Agent samples into the run itself | Two timelines, eyeballed side by side |
| Runs as plain local artifacts | A directory of JSON you own | Whatever each tool decided |

Copy must remain specific and restrained. If named comparisons are added later, every row becomes
a sourced claim with a “last verified” date.

### 9. Open-source install card

This replaces the reference’s pricing section.

- Heading: `One binary. No account, no tier, no seat.` — the anti-pricing framing is the point.
- Install switcher: **release binary / from source / Python SDK** — only the paths
  `docs/getting-started/installation.md` documents. There is no Homebrew tap, so none is
  offered.
- One card, split: inclusion list and CTAs on the left, a terminal window (dots + method tabs)
  on the right, matching the workflow’s chrome.
- The note names the real caveats: checksums.txt on every release; the agent is Linux-only in
  v1 and fails open elsewhere.
- CTAs: `View on GitHub` primary, `View docs` secondary — the same two destinations as the
  hero, not a third.

### 10. FAQ

Initial questions:

1. Is Flowbench a hosted service?
2. Which protocols are supported?
3. How is Flowbench different from running pytest and a load tool separately?
4. Can the same flow run from Python?
5. Where are results stored?
6. How does Flowbench keep load tests from hitting the wrong host?
7. Does `throttled` fail a run?
8. Can I use it in CI?

Answers come from the repository’s existing docs and link to the authoritative page.

### 11. Final CTA and footer

- CTA headline: `Stop rewriting the same journey.`
- Supporting line: `Author it once. Change the profile. Keep the evidence.`
- Repeat the two hero actions in the same order and hierarchy, under the same blue wash that
  lit the hero — the page closes where it opened.
- Footer navigation: on-page sections plus outbound Docs, GitHub, Releases, Issues, License.
  Doc links go to the hosted documentation, not local pages.
- The mark is three plain divs at the logo's 8.5/13/20 proportions — blue on top, and the
  widest bar ends the document mid-bar via a negative margin, so the page reads as cut rather
  than finished. (An SVG viewBox version was tried first and scaled unusably; divs with
  clamped heights are deterministic.) No motion in the first release.

## Visual system

### Identity

Use the product’s existing report UI as the source of truth:

- **Sans:** Open Runde, 400/500/600.
- **Mono:** JetBrains Mono for commands, metrics, run IDs, badges, and machine-produced facts.
- **Logo:** the existing three stacked rounded bars.
- **Core palette:** the results server’s dark tokens, near-black through warm greys, with
  Flowbench blue as the primary action.

### Tokens

**The page is dark.** The results server ships dark by default, and a light marketing page in
front of a dark product means every screenshot and every mockup arrives as a contradiction. So
the marketing tokens are not derived from the report tokens — they *are* the report tokens,
value for value, lifted from `internal/report/assets/report.css`:

| Role | Token | Value |
| --- | --- | --- |
| Page | `--ground` | `#0c0a09` |
| Surface | `--surface` | `#191614` |
| Raised surface | `--raised` | `#221d19` |
| Hairline | `--hairline` | `#2e2723` |
| Soft hairline | `--hairline-soft` | `#241f1b` |
| Headings | `--ink` | `#f5f3f0` |
| Body copy | `--ink-2` | `#a9a19a` |
| Muted copy | `--ink-3` | `#7c746d` |
| Primary action | `--blue` | `#3987e5` |
| Logic accent | `--orange` | `#d95926` |
| Retry / healthy | `--green` | `#199e70` |
| Ok | `--ok` | `#0ca30c` |
| Throttled | `--amber` | `#fab219` |
| Failure | `--red` | `#d03b3b` |

Use blue as the only broad marketing accent. Orange, green, amber, and red remain semantic
inside product visuals. On a near-black ground a drop shadow reads as nothing, so depth comes
from a lit top edge (`--edge`) plus a wide, deep drop — and the floating header cannot be
darker than the page, so it lifts on a translucent raised surface with a hairline and a blur.

No light theme in the first release. The report supports one; the marketing page does not need
to until someone asks for it.

### Type scale

- Hero: `clamp(3rem, 7vw, 5.5rem)`, tight but not touching line-height.
- Section heading: `clamp(2.1rem, 4vw, 3.5rem)`.
- Card heading: `1.25–1.5rem`.
- Body: `1rem–1.125rem`.
- Label/metric: `0.75–0.875rem`, mono only when the content is machine-produced.

### Shape and depth

- Header and primary buttons: full pills.
- Product stages: 24–32 px outer radius; 12–16 px internal cards.
- Feature cards: subtle border plus warm surface shift, not large diffuse shadows.
- Shadows explain a floating header or a raised product window only.
- Use a small set of repeated radii instead of rounding every element differently.

## Motion specification

Motion has a job on this page: explain profile reuse, connect result views, and acknowledge input.

- `--ease-out: cubic-bezier(0.23, 1, 0.32, 1)`
- `--ease-in-out: cubic-bezier(0.77, 0, 0.175, 1)`
- Button press: `scale(0.97)`, 140 ms.
- Header/links: color or opacity only, 140–180 ms.
- Hero entrance: opacity + 8 px vertical travel, 280–360 ms, 40 ms stagger.
- Product tab change: 180–220 ms opacity + 8 px travel; the tab itself uses an interruptible
  CSS transition.
- Feature diagrams: explanatory transitions may run 400–700 ms because they are infrequent
  marketing demonstrations.
- No autoplay carousel, scroll hijacking, cursor follower, or decorative parallax.
- Hover effects are gated behind `(hover: hover) and (pointer: fine)`.
- Reduced motion keeps state/color changes and removes travel, breathing, and looping.

## Technical architecture

### Recommended stack

Use **Astro + TypeScript + authored CSS**, without React or a runtime UI library.

Why:

- the page is content-first and should ship as static HTML;
- component files give the long page maintainable section boundaries;
- only the product tabs, mobile navigation, and FAQ require small client scripts;
- scoped styles make it practical to recreate the reference’s precision without importing its
  utility-class structure;
- the build can deploy to GitHub Pages or any static host.

Tailwind is intentionally omitted. The page has one bespoke visual system, and authored CSS keeps
tokens, reduced-motion behavior, and interaction states explicit.

### Proposed repository shape

```text
website/
  astro.config.mjs
  package.json
  tsconfig.json
  public/
    favicon.svg
    fonts/
    og/
  src/
    components/
      SiteHeader.astro
      Hero.astro
      ProofRail.astro
      ProductStage.astro
      FeatureGrid.astro
      Workflow.astro
      DecisionTable.astro
      InstallCard.astro
      Faq.astro
      FinalCta.astro
      SiteFooter.astro
      demos/
        DashboardDemo.astro
        FlameGraphDemo.astro
        WaterfallDemo.astro
        LiveRunDemo.astro
    content/
      landing.ts
    layouts/
      BaseLayout.astro
    pages/
      index.astro
    styles/
      tokens.css
      global.css
```

The demos should reuse the vocabulary and color contracts of `internal/report`, but remain
marketing components. They must not import server templates or couple the landing build to Go
template internals.

### Content and links

- Keep section copy and FAQ data in one typed content module.
- Keep the two destinations in one place — `REPO_URL` and `DOCS_URL` — so the primary and
  secondary CTAs can never drift apart across the header, hero, install card, final CTA, and
  footer.
- Deep links (quickstart, CLI reference, benchmark) resolve against `DOCS_URL`; only link to a
  subpage after confirming it exists on the published site.
- Store the canonical site URL in one configuration value for OG metadata and sitemap output.
- Generate an original Open Graph image from the hero and product-stage composition.

### JavaScript budget

- No client framework hydration.
- Product tabs, menu, and FAQ share one small script.
- Target: less than 15 KiB compressed first-party JavaScript.
- Product visualizations use semantic HTML and inline SVG, not canvas or a chart dependency.

## Accessibility and responsive behavior

- Semantic landmarks and a skip link.
- One `h1`; section headings follow a meaningful order.
- Native buttons for tabs/menu; correct tab roles and keyboard arrow behavior.
- FAQ uses native `details`/`summary` unless a custom accordion demonstrates a clear benefit.
- Every chart communicates its point in adjacent text and does not rely on color alone.
- Status colors always pair with a label or glyph.
- Visible focus rings use Flowbench blue with sufficient offset.
- Minimum 44 px touch targets for primary controls.
- Layout checks at 320, 375, 768, 1024, 1440, and 1920 px.
- Product mockups simplify at mobile rather than shrinking desktop UI until it becomes illegible.
- Full support for `prefers-reduced-motion`.

## Performance and quality gates

Project budgets, chosen to keep the marketing claim aligned with the product’s engineering tone:

- static render with useful content when JavaScript is unavailable;
- no layout shift from local fonts or product visuals;
- local font subsets with `font-display: swap`;
- responsive images only; no Visitors images or remote runtime assets;
- initial page transfer target under 500 KiB compressed, excluding the optional OG image;
- Lighthouse targets: Performance ≥ 95, Accessibility ≥ 95, Best Practices ≥ 95, SEO ≥ 95;
- LCP target under 2.5 s and CLS under 0.05 in the agreed test profile;
- no animation that continues when off-screen or when the document is hidden.

## Verification seams

1. **Static build seam:** the production build succeeds and contains the expected page, fonts,
   metadata, and internal links.
2. **Browser behavior seam:** one end-to-end suite covers navigation, tabs, FAQ, keyboard control,
   mobile menu, and reduced motion.
3. **Visual seam:** screenshot baselines at mobile, tablet, and desktop catch layout drift.
4. **Content seam:** a link checker and a small assertion list protect the primary CTAs and
   benchmark wording.
5. **Repository seam:** existing Go and Python checks remain untouched and green.

Suggested commands after scaffolding:

```bash
npm --prefix website run check
npm --prefix website run build
npm --prefix website run test:e2e
go test ./...
```

## Delivery plan

### Task 0 — Confirm conversion and host

**Size:** S  
**Dependencies:** none  
**Status:** CTA hierarchy decided; host and release label still open

- ~~Decide whether Get started or GitHub is the primary CTA.~~ Decided: `View on GitHub` is
  primary, `View docs` is secondary, and there is no Get started.
- Decide the canonical URL and deployment target.
- Confirm whether the page launches with `v0.1.0` copy or an evergreen release label.

**Acceptance**

- The CTA hierarchy and destination URLs are written down.
- The canonical URL and deployment target are known.

**Verification:** links open the intended public destinations.

### Task 1 — Establish the static shell and identity

**Size:** S  
**Dependencies:** Task 0  
**Status:** Done — `website/` scaffolded on Astro 7; `check` and `build` both clean

- Scaffold Astro, the base layout, tokens, local fonts, metadata, favicon, and shared container.
- Add the responsive floating header and empty page section anchors.
- Add build/check scripts without changing the Go build.

**Acceptance**

- The production build emits static HTML.
- Fonts, logo, focus styles, and design tokens render locally.
- The header works at mobile and desktop widths.

**Verification:** `npm --prefix website run check && npm --prefix website run build`.

### Task 2 — Ship the complete above-the-fold slice

**Size:** M  
**Dependencies:** Task 1  
**Status:** Done — hero, proof rail, product-stage frame, and the Dashboard scene

- Build the hero, CTA states, proof rail, and product-stage frame.
- Implement one complete Dashboard demo using real Flowbench vocabulary and sample data.
- Add responsive and reduced-motion behavior.

**Acceptance**

- The page communicates the one-flow promise without scrolling.
- Both CTAs work and the dashboard remains legible at 375 px.
- No Visitors-branded asset or copied claim appears.

**Verification:** visual snapshots at 375 and 1440 px; keyboard and no-JS smoke test.

**Checkpoint A:** review the hero message, visual fidelity, and conversion hierarchy before the
rest of the long page is built.

### Task 3 — Complete the product-stage interaction

**Size:** M  
**Dependencies:** Task 2  
**Status:** Done — four scenes, accessible tabs, fast facts; visual verification still outstanding

- Add Flame graph, Waterfall, and Live run demo scenes.
- Implement accessible tabs and mobile control.
- Add the three fast-fact statements below the stage.

**Acceptance**

- Arrow keys and pointer input switch scenes.
- Scene changes are interruptible and honor reduced motion.
- Each scene answers one distinct product question.

**Verification:** end-to-end tab tests plus visual snapshots for every scene.

### Task 4 — Build features and workflow

**Size:** M  
**Dependencies:** Task 2  
**Status:** Done — lead feature plus three cards, and Author → Run → Inspect

- Add the four differentiated feature stories and their explanatory diagrams.
- Add Author → Run → Inspect with truthful YAML, CLI, and results examples.
- Link supporting claims to repository docs where useful.

**Acceptance**

- A visitor can explain the product’s four differentiators after scanning the section.
- Code samples match the current DSL and CLI.
- Diagrams remain understandable without animation.

**Verification:** compare snippets against quickstart/reference docs; mobile visual review.

**Checkpoint B:** review the whole narrative from hero through workflow; cut repetition before
adding objection-handling sections.

### Task 5 — Complete the conversion tail

**Size:** M  
**Dependencies:** Tasks 3 and 4  
**Status:** Done — decision table, install card, FAQ, final CTA, and footer with the oversized mark

- Add the decision table, install card, FAQ, final CTA, and footer.
- Add the oversized Flowbench mark treatment.
- Wire every internal and external link.

**Acceptance**

- Install methods reflect supported release paths.
- FAQ answers are sourced from existing docs.
- The page has a clear second conversion opportunity near the bottom.

**Verification:** link check, keyboard pass, 320–1920 px responsive pass.

### Task 6 — Polish, accessibility, and motion review

**Size:** M  
**Dependencies:** Task 5

- Tune spacing, typography, state feedback, and section transitions as one coherent system.
- Audit semantics, contrast, focus order, tab behavior, reduced motion, and touch targets.
- Remove any motion or ornament that does not explain, connect, or acknowledge.

**Acceptance**

- Automated accessibility scan has no serious findings.
- Full page works by keyboard and with JavaScript disabled.
- Reduced motion removes travel and loops without hiding state changes.

**Verification:** accessibility audit, manual keyboard pass, reduced-motion snapshots.

### Task 7 — Performance, metadata, and deployment

**Size:** M  
**Dependencies:** Task 6

- Add canonical metadata, OG image, sitemap/robots handling, and release-safe titles.
- Enforce bundle and image budgets.
- Add CI for site check/build/test and the chosen static deployment.

**Acceptance**

- All four Lighthouse categories meet the stated target in the agreed profile.
- Production URLs and social previews are correct.
- A push through the deployment path produces the expected public page.

**Verification:** production Lighthouse run, deploy smoke test, existing repository checks.

**Checkpoint C:** final visual and copy review on the deployed preview before making it canonical.

## Not doing in the first release

- A hosted interactive Flowbench sandbox — it conflicts with the local-first v1 product.
- Invented customer logos or testimonials — there is no evidence for them in the repository.
- Named competitor tables without sourced, date-stamped verification.
- A blog, docs migration, login, account, pricing, or newsletter system.
- A React runtime, animation library, charting library, or Tailwind dependency.
- Autoplay carousels, cursor effects, 3D scenes, scroll hijacking, or heavy video.
- Reusing Visitors screenshots, background art, copy, or source code.

## Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| “Almost direct copy” becomes visually derivative | Match structure and quality while deriving identity from Flowbench’s existing report system |
| The long page repeats the same one-flow claim | Give every section one distinct question and cut copy at Checkpoint B |
| Product demos drift from the real UI | Use existing report vocabulary/tokens and verify examples against current docs |
| Benchmark numbers look universal | Label the reference hardware and link the methodology |
| Added web tooling burdens a Go repository | Keep it isolated under `website/`, static-only, and independently scripted |
| Product mockups become illegible on mobile | Design dedicated simplified mobile scenes instead of scaling desktop canvases |
| Motion undermines perceived performance | Prefer CSS transform/opacity, stop off-screen work, and enforce reduced motion |

## Definition of done

The landing page is complete when:

1. a new visitor understands “author once, change the profile, inspect the evidence” without
   reading repository documentation first;
2. the page closely matches the reference’s information rhythm and finish while looking
   unmistakably like Flowbench;
3. every visible product claim is supported by the repository or linked documentation;
4. the entire primary journey works at mobile and desktop widths, with keyboard navigation,
   reduced motion, and JavaScript disabled;
5. the static production build passes the agreed accessibility, performance, visual, and link
   checks;
6. the primary CTA reaches the GitHub repository and the secondary CTA reaches the published
   documentation, from every place they appear.
