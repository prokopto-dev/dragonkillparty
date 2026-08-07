# 0015 — Nocturne, dark-only, plain CSS and design tokens

**Status:** accepted · 2026-08-07
**Deciders:** Courtney (owner)
**Supersedes:** nothing. **Related:** [0005](0005-api-first-no-bff.md) (the SPA is a client),
[0012](0012-english-only-at-1-0.md) (no hardcoded strings), [0014](0014-full-portal-parity-in-scope.md)
(the theme editor is Phase 8 scope).

## Context

The repository had no visual-design decision of any kind. `.claude/rules/web.md` fixed the router,
the query layer and the API client, and said nothing about CSS. `ROADMAP.md` promised "theme" in
Phase 3 and a "theme editor with design tokens" in Phase 8 without saying what the tokens were.

Five UI mockups then landed covering ~55 screens, built on a design system called **Nocturne**: a
near-neutral blue-grey dark ground, Inter at weight 500, 8px radii, an accent used as a line rather
than a flood, a 0.70× spacing scale, and rules that fade to transparent over 48px at each end. It
ships **one palette** — there is no light theme, no `prefers-color-scheme` block, and no
success/warning/danger tokens; status is expressed by borrowing the accent ramp.

Three questions had to be answered before any component could be written, because retrofitting any
of them costs a rewrite of every screen.

## Decision

**1. Styling is plain CSS with custom properties.** One token sheet (`web/src/styles/tokens.css`)
plus co-located component CSS. No Tailwind, no CSS-in-JS, no second styling system. Accessibility
primitives are hand-built or come from TanStack, already a dependency.

**2. Nocturne is the one shipped theme, and it is dark only at 1.0.** A guild themes by overriding
token *values*, gated by a contrast validator at ≥3:1 against `--color-bg`. Light mode is deferred to
1.1.

**3. Semantic status colours are ours, added to the system.** `--color-success`, `--color-warning`
and `--color-danger`, derived in OKLCH at the ramps' own lightness steps. Status is never carried by
hue alone — every status also carries an icon and a word.

The token values are transcribed into
[`../design/09-frontend-and-design-system.md`](../design/09-frontend-and-design-system.md), which is
normative; canonical §17 is the tie-breaker.

## Alternatives

**Tailwind v4 with the tokens as `@theme`.** Fast to write and it tree-shakes well. Rejected because
Nocturne is ~50 tokens and a dozen flat classes — small enough that a utility framework is mostly
overhead — and because it creates a second source of truth for token names that the contrast
validator and the `x-omelette` token manifest would both have to agree with. The mockups' fractional
type sizes (10.5 / 13.5 / 16.5px) and ~400 one-off pixel values also fight the utility model rather
than fitting it.

**CSS Modules.** Viable and dependency-free under Vite. Rejected as ceremony: the system's classes
are already flat and global by design (`.btn`, `.card`, `.table`), and scoping them per component
fights the thing that makes a design system legible.

**A headless component library (Radix, Ark).** Would buy dialog/tabs/menu/combobox focus management
cheaply. Rejected for 1.0 because the component families these screens actually depend on — tick
strip, custody timeline, tier ladder, request stepper, side-by-side diff — are in no library, so the
dependency covers perhaps four of twenty families while costing ~30 KB against a 250 KB budget. Worth
revisiting if the a11y gate proves expensive to hold by hand.

**Authoring a light theme at 1.0.** Rejected because no screen has been drawn against one. It means
designing a second system rather than inverting a palette, and doubling every contrast check with
nothing to check against. A dark-only product is a real limitation and it is on the deferred table
where it can be argued with.

**Keeping Nocturne's convention of expressing status through the accent ramp.** Rejected because it
means "amber accent means warning" survives exactly until a guild re-themes the accent, at which
point thirty screens quietly change meaning.

## Consequences

- **Good:** the smallest possible bundle contribution from styling, no build-time styling
  dependency, and the token sheet is directly comparable to the vendored `styles.css` — a test
  asserts the shipped token names match the design document.
- **Good:** "a guild themes by overriding values, never by adding CSS" is enforceable, which is the
  specific thing that made EQdkp Plus impossible to upgrade. ADR-0014's theme editor becomes a
  bounded feature rather than a template engine.
- **Bad:** every accessibility primitive is ours to get right, and the a11y gate (zero serious or
  critical on primary routes) is a real obligation with no library behind it.
- **Bad:** no light mode at 1.0 excludes people who need one. Named in the deferred table rather
  than left to be discovered.
- **Neutral:** the status ramps are invented here, so they are ours to maintain. They are five steps
  in three hues and generated the same way the existing ramps were.
