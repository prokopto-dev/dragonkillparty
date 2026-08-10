---
paths: ["web/src/styles/**", "web/src/components/**"]
description: Nocturne — the token layer, the five details that make it read as Nocturne, and what is banned in a component.
---

# Design system

The shipped look is **Nocturne**, dark-only. The normative document is
[`docs/design/09-frontend-and-design-system.md`](../../docs/design/09-frontend-and-design-system.md);
canonical §17 is the tie-breaker. The screens are in `docs/design/mockups/` — **read the screen
before you write it.** Copying a worked example beats recalling an intent.

## Non-negotiable

- **Plain CSS with custom properties.** One token sheet (`web/src/styles/tokens.css`) plus co-located
  component CSS. No Tailwind, no CSS-in-JS, no second styling system.
- **No raw hex and no raw `px` outside the token layer.** Lint fails on both. A value the scale does
  not carry gets a named rung in `tokens.css`, not an inline literal — the mockups' ~400 one-off
  pixel values are the thing being converted, not the thing being copied.
- **One palette.** Dark only at 1.0. No `prefers-color-scheme`, no light overrides, no second accent
  (`--color-accent-2` is a mono-scheme stand-in; treat it as the same role).
- **A guild themes by overriding token values, never by adding CSS.** No free-form stylesheet, no
  per-file template override. That engine is why nobody could upgrade EQdkp.
- **Status is never carried by hue alone.** Every status also carries an icon and a word.

## The five details that decide whether it reads as Nocturne

1. **Hairlines fade to transparent over 48px at each end** — freestanding rules, `thead`, and every
   `tbody` row. Painted as a *row-level* background gradient with transparent cell borders holding
   the layout; a per-cell border cannot fade across a row. This is the most identifiable thing in the
   system. **One sanctioned exception** (§4, issue #34): a sticky header sticks the `<th>`, not the
   `<tr>` that paints the rule, so a scrolling table moves the rule onto the `<th>` and gives up the
   end-fade — scoped through `.virtual-table`, so an ordinary `.table` keeps it. A *second* place the
   hairline stops fading is a design decision, not an implementation detail.
2. **Buttons are outlined, never filled.** Primary is a 1px accent border on transparent, `tint(12)`
   hover, `tint(22)` active.
3. **Elevation is a hairline ring plus ambient darkness.** `--shadow-sm` has no blur — it *is* a
   ring. Never stack them.
4. **No transitions.** Nocturne has no motion tokens and no `transition` property anywhere. Hover
   states snap. If a library adds `transition: all .2s`, turn it off.
5. **Focus is always** `2px solid var(--color-accent)`; the offset is `2px` by default, `0` on
   `.input` and `-2px` on `.seg-opt`. Never the browser default.

## The two helpers

```css
soft(p) → color-mix(in srgb, var(--color-text)   <p>%, transparent)   /* muted text, hairlines */
tint(p) → color-mix(in srgb, var(--color-accent) <p>%, transparent)   /* selected, active */
```

They account for most of the colour surface area — 325 uses across the mockups: 266 `soft()` at
3–75%, and 59 `tint()` at 6–26%. Implement them in the token layer; never inline the `color-mix` in a
component.

## Density

`comfortable` / `compact` retunes **spacing only**. The type scale is fixed. A density mode that
changes font sizes changes what a table means.

## Components

One file per component, which is what makes this a parallel lane. Reproduce the system's class names
(`btn`, `card`, `table`, `tag`, `seg`, `field`, `input`, `radio`, `dialog`) rather than inventing
parallel ones.

**Not in the system, because no mockup uses one:** toasts, skeletons, accordions, tree tables, date
pickers, drawers. Feedback is inline, loading is a suspense boundary, there is no snackbar. Adding
one is a design change — raise it, do not introduce it in a feature PR.

## Stop and ask if

- A screen needs a component family that is not in `09-frontend-and-design-system.md` §5.
- You want a colour that is not a token, or a token that is not in canonical §17.
- The mockup and the design document disagree — the document wins, and the disagreement is a bug
  worth reporting.
