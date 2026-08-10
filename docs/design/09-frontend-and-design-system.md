# Frontend and design system

**Status:** normative. **Audience:** anyone writing `web/src`.
**Normative tie-breaker:** [`00-canonical-conventions.md`](00-canonical-conventions.md) §17.

The shipped look is **Nocturne**. This document is what you implement against; the artefact it was
transcribed from is [`mockups/nocturne/styles.css`](mockups/nocturne/styles.css), and the screens
that use it are the five surfaces in [`mockups/`](mockups/). Read the screen before you write it —
copying a worked example is more reliable than recalling an intent.

[`.claude/rules/web.md`](../../.claude/rules/web.md) owns the *client* contract — the generated API
client, TanStack Query, the bundle budget. This document owns everything visual.

---

## 1. The decisions, up front

| Decision | Why the alternative was rejected |
|---|---|
| **Plain CSS with custom properties.** One token sheet plus co-located component CSS. | Nocturne is ~50 tokens and a dozen flat classes. A utility framework would add a build dependency and a second source of truth for token names, and the mockups' fractional sizes (10.5 / 13.5 / 16.5px) and ~400 one-off values fight the utility model rather than fitting it. |
| **Dark only at 1.0.** | Nocturne ships one palette. Authoring a light counterpart means designing a second system nobody has drawn and doubling every contrast check, against no drawn screen. Deferred to 1.1. |
| **Semantic status colours are ours.** | Nocturne has no success/warning/danger; it expresses status by borrowing the accent ramp. Thirty-plus screens carry warn and error states, and "amber accent means warning" is a convention that survives exactly until someone re-themes the accent. |
| **Accessibility primitives are hand-built.** | The component families here — tick strip, custody timeline, tier ladder, request stepper — are not in any headless library, and the four that are (dialog, tabs, menu, combobox) do not justify a dependency plus ~30 KB against a 250 KB budget. TanStack, already a dependency, covers the virtual list. |

---

## 2. Tokens

Transcribed verbatim from `mockups/nocturne/styles.css`. **These values are the contract** — a test
asserts the shipped sheet still matches this table.

### Roles

| Token | Value |
|---|---|
| `--color-bg` | `#161826` |
| `--color-surface` | `#232532` |
| `--color-text` | `#e9e9ed` |
| `--color-accent` | `#9184d9` |
| `--color-accent-2` | `#a7a1db` |
| `--color-divider` | `color-mix(in srgb, #e9e9ed 16%, transparent)` |

`--color-divider` hard-codes the text hex rather than referencing `--color-text`. Reproduce it
literally; re-deriving it changes the value when a guild themes the text colour.

`--color-accent-2` is a machine-derived stand-in. Nocturne is a **mono scheme** — treat accent-2 as
the same role and do not introduce a second accent voice.

### Ramps — 100 → 900, generated in OKLCH on one shared lightness scale

| Step | `--color-neutral-*` | `--color-accent-*` | `--color-accent-2-*` |
|---|---|---|---|
| 100 | `#f3f5fe` | `#f5f4ff` | `#f5f4ff` |
| 200 | `#e4e7f5` | `#e7e5fe` | `#e7e5fe` |
| 300 | `#cfd3e5` | `#d2cefd` | `#d2cefd` |
| 400 | `#b2b6ca` | `#b5abfc` | `#b5afe8` |
| 500 | `#9397ab` | `#968ae0` | `#9690c9` |
| 600 | `#75798c` | `#796cbf` | `#7972a9` |
| 700 | `#595d6c` | `#5d5294` | `#5c5783` |
| 800 | `#3f424d` | `#423a6a` | `#423e5d` |
| 900 | `#292b31` | `#2b2741` | `#2b293a` |

Step *N* of any ramp has the same visual weight as step *N* of any other. On this ground: **700–900**
for tinted fills, hovers and subtle borders; **500** as the role base; **100–300** for text sitting
on those tints and for pressed states.

`--color-accent` (`#9184d9`) is **not** `--color-accent-500` (`#968ae0`). Both exist; do not collapse
them.

### Section — deck scale only

`--color-section: #262a60` · `--color-section-glow: #353b80` · `--color-section-ghost: #4c5397`

Saturated grounds at page scale, sanctioned in exactly two places: the public site's full-bleed stat
band, and the site-wide quake banner. Nowhere else.

### Type, space, radius, elevation

| Token | Value | | Token | Value |
|---|---|---|---|---|
| `--font-heading` | `"Inter", system-ui, sans-serif` | | `--radius-sm` | `4px` |
| `--font-heading-weight` | `500` | | `--radius-md` | `8px` |
| `--font-body` | `"Inter", system-ui, sans-serif` | | `--radius-lg` | `14px` |
| `--space-1` | `2.8px` | | `--shadow-sm` | `0 0 0 1px #3f424d` |
| `--space-2` | `5.6px` | | `--shadow-md` | `0 0 0 1px #595d6c, 0 6px 18px rgba(0,0,0,.55)` |
| `--space-3` | `8.4px` | | `--shadow-lg` | `0 0 0 1px #9397ab, 0 16px 40px rgba(0,0,0,.65)` |
| `--space-4` | `11.2px` | | | |
| `--space-6` | `16.8px` | | | |
| `--space-8` | `22.4px` | | | |

The base unit is 4px × **0.70** — this system is dense on purpose. There is no `--space-5` or
`--space-7`; the gaps are in the source and are not an omission to fix.

**There are no motion tokens and no `transition` anywhere in Nocturne.** Hover states snap. If your
component library adds `transition: all .2s` by default, turn it off — the feel is wrong with it.

### Status colours — ours, added here

Nocturne ships none. These are derived in OKLCH at the ramps' own lightness steps so they sit in the
same family, at a chroma close to the accent's so they read as status rather than decoration.

| Step | `--color-success-*` (H 150) | `--color-warning-*` (H 75) | `--color-danger-*` (H 25) |
|---|---|---|---|
| 300 | `oklch(0.862 0.088 150)` | `oklch(0.862 0.093 75)` | `oklch(0.862 0.079 25)` |
| 400 | `oklch(0.776 0.112 150)` | `oklch(0.776 0.119 75)` | `oklch(0.776 0.104 25)` |
| 700 | `oklch(0.487 0.096 150)` | `oklch(0.487 0.101 75)` | `oklch(0.487 0.108 25)` |
| 800 | `oklch(0.383 0.074 150)` | `oklch(0.383 0.078 75)` | `oklch(0.383 0.086 25)` |
| 900 | `oklch(0.283 0.052 150)` | `oklch(0.283 0.055 75)` | `oklch(0.283 0.061 25)` |

Only the five steps the screens actually use. Use `-800`/`-900` as a tag or callout ground, `-300`
as text on it, `-400` for an icon, `-700` for a border. **Never use hue alone to carry meaning** —
every status also carries an icon and a word, because a red pill and a green pill are the same pill
to a large minority of officers.

### The two helpers

```css
--soft: color-mix(in srgb, var(--color-text)   <p>%, transparent);  /* muted text, hairlines, fills */
--tint: color-mix(in srgb, var(--color-accent) <p>%, transparent);  /* selected, active, highlight  */
```

The mockups call these `soft(p)` and `tint(p)` and use them 325 times — 266 `soft()` at 3–75%, and
59 `tint()` at 6–26%. Implement them as real helpers in the token layer. They account for most of the colour
surface area and are the single biggest lever on whether a screen looks like Nocturne.

---

## 3. Type

Inter at 400 (body) and 500 (headings). Loaded **self-hosted**, not through the Google Fonts
`@import` the source sheet uses — the binary serves the SPA offline and a render-blocking third-party
request contradicts that.

### How it is loaded

Two faces — a Latin subset of upstream Inter 4.1 (SIL OFL 1.1) — committed under
`web/src/assets/fonts/` beside their licence text and a provenance table; `web/src/styles/fonts.css`
declares the `@font-face` pair at `font-display: swap` and `base.css` imports it before `tokens.css`,
so `--font-heading` and `--font-body` resolve to real faces rather than to the `system-ui` tail of
their stacks. Vite content-hashes both files into `web/dist`, `internal/ui` embeds them and serves
them `immutable` for a year, and the SPA makes **no** request off its own origin for type.

Three things hold that shape, because prose does not:

- `WEB003` in `scripts/repo-gates.sh` fails on any **off-origin URL** under `web/src` or in
  `web/index.html` — not a list of asset-bearing shapes, because an enumeration is only as complete
  as whoever wrote it, and `<script src>`, `<img src="//…">` and the quoted `@import "https://…"`
  each break the offline contract exactly as much as the Google Fonts line this system declines to
  transcribe. The generated client under `web/src/api/` is the one exemption.
- `test/repo/web_fonts_test.go` requires every declared face to resolve to a committed file, every
  committed face to be declared, and the OFL text to be recorded in `NOTICE` and
  `THIRD_PARTY_NOTICES.txt`. The licence gate cannot: it reads the Go module graph and a font is
  not a module.
- Only 400 and 500 are vendored, which makes "do not bolden a heading past 500" below a fact about
  the shipped bytes rather than a request.

The faces are a **Latin subset** — 36 KB for the pair rather than 225 KB, and a binary this
repository generated rather than one it can point at. That trade is only acceptable with the
provenance intact, so the subsetting is a committed, pinned, byte-reproducible step
(`scripts/subset-fonts.sh`, `make verify-fonts`) rather than something someone ran on a laptop: the
same bytes come out across three Python versions, two `fonttools` versions, two `brotli` versions and
three architectures, and `web/src/assets/fonts/README.md` carries the input hashes, the exact flags
and the evidence. The range is the Google Fonts `latin` range plus seven symbols the mockups render
(`→ ← ≥ ≤ ≠ ∞ ⌘`), and `tnum` is retained explicitly — pyftsubset's default feature list drops it,
and every `font-variant-numeric: tabular-nums` column in this document would stop aligning.
Fonts are assets, so none of it counts against `web/bundle-budget.json`, which measures entry JS.

| Element | Size | Notes |
|---|---|---|
| `h1` | 42px | all headings: `line-height: 1.12`, `letter-spacing: -0.015em`, weight 500 |
| `h2` | 32px | |
| `h3` | 25px | |
| `h4` | 20px | |
| `h5` | 16px | |
| `h6` | 13px | plus `letter-spacing: .08em`, uppercase |
| body | 15px / 1.55 | weight 400 |

**Density moves spacing, never sizes.** Do not bolden a heading past 500 — hierarchy here is size
and space, not weight.

The mockups use fractional sizes deliberately — 10.5, 11.5, 12.5, 13.5, 15.5, 16.5px all appear.
Rounding them to a clean scale visibly changes the density. Carry them.

### The scale needs extending, and that is a decision, not a drift

The mockups need spacing and radius rungs the token set does not carry: radii **7, 9, 10, 11, 999**
and roughly twenty spacing values between 8 and 70px. Extend the scale explicitly in
`tokens.css` — `--radius-xs: 7px`, `--radius-pill: 999px`, and a `--space-*` continuation — rather
than scattering one-off pixel values through components. A raw `px` in a component is a lint failure;
a named rung is a decision.

---

## 4. The five details that make it read as Nocturne

Get these wrong and the result is a generic dark theme.

1. **Hairlines fade to transparent over 48px at each end.** Freestanding rules, `thead` and every
   `tbody` row. Painted as a row-level background gradient with transparent cell borders holding the
   layout — a per-cell border cannot fade across the row:

   ```css
   .table tbody tr {
     background: linear-gradient(to right, transparent,
       var(--rule) 48px, var(--rule) calc(100% - 48px), transparent) no-repeat bottom / 100% 1px;
   }
   ```

   Box outlines, in-control separators and short accent marks stay solid. This is the single most
   identifiable thing in the system.
2. **Buttons are outlined, never filled.** The primary action is a 1px accent border on transparent,
   with a `tint(12)` hover and `tint(22)` active. A filled primary button reads as a different
   product.
3. **Elevation is a hairline ring plus ambient darkness**, not a drop shadow. `--shadow-sm` has no
   blur at all — it is a ring. Never stack them.
4. **The accent is a line and a glow, never a flood.** Chroma stays low outside it. The only
   sanctioned saturated grounds are the public stat band and the quake banner.
5. **Focus is always** `2px solid var(--color-accent)`. The offset is `2px` by default, `0` on
   `.input` and `-2px` on `.seg-opt`, so the ring hugs a bordered control instead of floating off it.
   Never the browser default.

---

## 5. Components

The system's own classes — reproduce these names: `.btn` (`-primary`, `-secondary`, `-ghost`,
`-icon`, `-block`) · `.tag` (`-accent`, `-neutral`, `-outline`) · `.card` (`-kicker`, `-title`,
`-body`, `-meta`) · `.elev-sm/md/lg` · `.field` + `label` + `.input` · `.radio` + `.dot` · `.seg` +
`.seg-opt` · `.table` · `.dialog-backdrop` + `.dialog` · `.hr` (present, discouraged — this system
prefers whitespace) · `.text-muted`.

`.seg-opt` uses `:has(input:checked)`. Either keep that browser floor or restructure with a state
class; do not silently drop the segmented control's keyboard semantics to avoid it.

**Application component families**, which no library provides and every screen depends on:

| Family | Load-bearing detail |
|---|---|
| **Tick strip** | Three states — committed (accent fill), retroactive (**dashed** neutral border), voided (transparent, struck). All three are ordinary and all three stay on the record. |
| **Stat tile** | Uppercase label / large tabular value / muted sub. Always in fours. |
| **Bar / meter row** | Attendance colour-grades: ≥85% accent, ≥65% accent-700, below neutral-700. |
| **Statement row** | Reversed rows render struck through and muted, never hidden. |
| **Tier ladder** | Ordered, numbered, with the rule text per tier. Order is semantic. |
| **Bid lot card** | Countdown renders `closes_at − server_time + local_elapsed`. Never the client clock. |
| **Request stepper** | Four stages, two-sided: an officer marks handover, the member confirms receipt. |
| **Custody timeline** | A segmented bar over an hour ruler, with gaps drawn as gaps. |
| **Filter chip** | `.tag-outline` with a removable `×`. Server-side filters, always. |
| **Master–detail** | Disputes and policies. Left list, right pane. |
| **Side-by-side diff** | Person merge. Two panels, matched rows, target highlighted. |
| **Drag handle** | `ph-dots-six-vertical`, on seven reorderable surfaces. |

Icons are **Phosphor**, regular weight, sized and coloured inline. Self-host the subset used; do not
ship the whole set.

**Not present anywhere in the mockups, and therefore not in the system:** toasts, skeletons,
accordions, tree tables, date pickers, drawers. Feedback is inline; loading is handled by
suspense boundaries; there is no snackbar. Adding one is a design change, not an implementation
detail.

---

## 6. Information architecture

**Officer console.** A two-level appliance model: an icon rail of seven groups — Overview, Raids,
Points, Loot, Roster, Portal, System — driving a section sidebar. Selecting a group navigates to its
first item.

Three layouts ship behind one control, because the right answer depends on screen width and officer
habit:

| Mode | Rail | Sidebar | Section tabs | Top groups |
|---|---|---|---|---|
| `rail` *(default)* | ● | ● | | |
| `hybrid` | ● | | ● | |
| `top` | | | ● | ● |

Density (`comfortable` / `compact`) is a separate axis and retunes the spacing token only.

**Member portal.** A flat tab bar over fifteen surfaces, with the member's balance in the header
chrome. The home screen is a 12-column widget grid with an explicit Customise mode; the saved layout
is a **`table-view` API resource, not a `localStorage` blob**, so a bot reads standings in the
columns the officers see.

**Mobile.** Two screens are designed for the phone — balance, and whether the tick counted — because
those are the two things a member checks *during* a raid. Everything else degrades to the responsive
view. Hit targets stay at 44px.

**Global.** ⌘K search on every officer screen. A site-wide quake banner. Both banners
(quake, draft week) render above `<main>` on every portal route.

---

## 7. Attribution

Every surface carries, in its footer or the admin sidebar's instance card:

> Made by **Prokopton** · P99 Green · member of **Legacy of Fire**

This is a credit to the guild that supported the project, not decoration. Placement may move; the
line does not disappear. See [`10-ui-decisions.md`](10-ui-decisions.md) §14.
