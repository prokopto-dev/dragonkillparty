# UI mockups — the Nocturne surfaces

Five HTML mockups covering roughly 55 screens, plus the design system they are built on. They are
**design artefacts, not a frontend**: no build step, no npm, no framework. They exist so an
implementer can read a screen off disk instead of re-deriving it from prose.

They came from the `EQDKPPlus UI mockups` Claude Design project
(`10c4c49d-170f-4b50-ab82-b2488e979ac2`), built against this repository's own docs. Where the docs
were explicit the mockups follow them; where the docs were silent the mockups **invented** something,
and every invention is catalogued in [`../11-ui-backend-contract.md`](../11-ui-backend-contract.md)
with a verdict.

## What these are normative for, and what they are not

| Normative | Not normative |
|---|---|
| The **design system** — every token value in `nocturne/styles.css`. See [`../09-frontend-and-design-system.md`](../09-frontend-and-design-system.md), which is the document you implement against. | The mock data. Names, balances, dates and counts are fixtures. |
| **Information architecture** — the nav groups, the screen set, what sits on which surface. | Exact pixel values in inline `style=` attributes. The mockups hard-code ~400 raw px values the token scale does not carry; the design doc extends the scale deliberately instead. |
| **Interaction semantics** — preview-then-commit, step-up gates, three-state tick cells, the three-way key cycle. | Any price scale predating tiered bidding. Main-tier bids land in single or low double digits; a three-figure main-tier bid on a screen is stale. |
| **Copy**, where it states a rule. The explanatory callouts carry most of the product's reasoning. | The `seq` values printed across pools. `seq` is **per pool** (canonical §4); a single instance-wide `seq` marker is a known mockup error. |

Nothing in `web/src` reads these files at runtime and they are never served. **Tests do assert
against them**, though — the design system half of the table above is enforced, not merely
documented:

- `test/repo/design_tokens_test.go` diffs the shipped `.table` rules against `nocturne/styles.css`
  with `var()` resolved on both sides (`TestDesignSystem_TableCSS_ResolvesToTheSourceSheet`), and
  holds the scrolling variant to the same sheet bar an explicit list
  (`TestDesignSystem_VirtualTable_DivergesOnlyWhereSanctioned`). The sanctioned-divergence list lives
  in that file.
- `test/repo/web_fonts_subset_test.go` scans these files for OpenType features, so a
  `font-variant-numeric` the drawn screen uses cannot be dropped by the Latin font subset.

So the screens, the mock data and the ~400 inline pixel values are a **snapshot**; the design
system's values are a **contract with a test behind it**. Refreshing `nocturne/styles.css` is
therefore not a docs-only change — expect `test/repo` to have an opinion, and read the failure
message rather than editing the vendored file to satisfy it.

That cuts both ways, and the second direction is the one that surprises people: when the mockup is
wrong — `.card-meta` painted at ≈4.25:1, under the AA floor (issue #58) — the fix is to diverge the
*shipped* sheet and record why, precisely BECAUSE the vendored file is byte-exact and fingerprinted.
A divergence belongs in [`../09-frontend-and-design-system.md`](../09-frontend-and-design-system.md)
(§4 for the sticky header, §2 for the `.card-meta` contrast rung) and in the shipped sheet's own
comment. Never in the vendored file.

When a screen and [`../10-ui-decisions.md`](../10-ui-decisions.md) disagree, the decisions document
wins — and when either disagrees with `docs/design/00-canonical-conventions.md`, the canonical
conventions win and the conflict is a bug worth reporting.

## Files

All vendored byte-exact from a project export. Sizes are a fingerprint — if one changes, the mockup
changed.

| File | Surface | Bytes |
|---|---|---|
| `admin-console.dc.html` | Officer console — 37 screens, 3 modals, three nav layouts | 330,511 |
| `guild-portal.dc.html` | Member portal, including the two phone views | 170,598 |
| `public-site.dc.html` | Guest landing, claim flow, application form | 50,701 |
| `my-characters.dc.html` | Characters, zone keys, raid eligibility, main swaps | 30,651 |
| `first-run.dc.html` | Setup wizard — server-rendered, pre-SPA | 17,322 |
| `nocturne/styles.css` | The design system — tokens plus component classes | 13,029 |
| `nocturne/readme.md` | The system's own usage guide and direction | 8,307 |
| `nocturne/_adherence.oxlintrc.json` | The system's lint config (raw hex, raw px, non-Inter fonts) | 4,195 |

### Refreshing them

Export the project from claude.ai and copy the files in under the names above — the export names them
with spaces (`Admin Console.dc.html`), the repo uses kebab-case. Do not fetch them through the design
tool's file read: it caps at 256 KiB, which silently truncates `admin-console.dc.html`, and routing
the rest through a model to retype them is lossy for files whose whole value is being byte-exact.

Refreshing them changes no product code, but two different things check the result. The build gates
are MOCK001 and MOCK002 below, plus MOCK004 for the `noindex` a fresh export does not carry. And a
refresh that touches `nocturne/styles.css` also has to clear the fidelity tests named above — the
shipped `.table` is diffed against it — so budget for that rather than expecting a docs-only diff.

## The harness — our own, not the design tool's

The `.dc.html` files are templates, not finished pages: `{{ path }}` bindings, `<sc-for>` /
`<sc-if>` blocks, a `<helmet>` of head content, and a `class Component extends DCLogic` at the
bottom supplying the data. Something has to render them.

The design tool's own renderer is third-party and carries no licence, and `NOTICE` says no
third-party source is vendored here — so it is not. `harness/mockup-runtime.js` implements the same
template contract from scratch in ~250 lines of dependency-free DOM code, and
`harness/ios-frame.js` replaces the iPhone frame the two phone views import. Between them they also
drop the tool's React, ReactDOM and Babel CDN loads: **every one of the 1,228 bindings in these
files is a plain dotted path or a literal**, so the runtime resolves them by walking objects. There
is no `eval`, no `new Function`, and no expression evaluator to escape from.

That property is load-bearing, so it is a build gate. `internal/mockup` refuses to build if a
refreshed export introduces a binding the resolver cannot walk (`MOCK001`) or if the tool's runtime
filenames reappear (`MOCK002`).

## Publishing

```bash
make mockup-site
```

Assembles `_site/` and is what `.github/workflows/pages.yml` deploys. The vendored files are never
modified; [`internal/mockup`](../../../internal/mockup) does the work on copies — repointing the
runtime and stylesheet, letting the authored logic run as an ordinary script, and injecting the
**MOCKUP — not a live instance** banner that every published page carries. `MOCK001`–`MOCK004` and
every transform below are driven against negative fixtures by
[`test/repo/mockup_gates_test.go`](../../../test/repo/mockup_gates_test.go).

One transform there is worth knowing about. The HTML parser *foster-parents* unknown elements out of
a `<table>`, so a `<sc-for>` wrapping `<tr>`s is hoisted above the table — and arrives there
**empty**, because the rows stay behind in the table. The runtime repeats a directive's children, so
a directive with none renders nothing, which is what silently emptied 37 of these tables. The build
lifts each directive onto the element it repeats (`<tr data-sc-for="…" data-sc-as="…">`), which is
valid HTML anywhere, then asserts no `<sc-*>` element survives inside table context — and finally
hands the finished page to a real HTML5 tree builder and refuses it if any of the mockups' own
elements came back with fewer children than the markup gave them. That last check is what catches the
same failure arriving through `<x-import>` or `<helmet>`.

`<x-import>` has an attribute form of its own for that case — `data-sc-import="Name"` plus one
`data-sc-prop-*` per prop — and a single-child `<x-import>` **inside table context** is lifted onto
its child the same way a directive is. Only there: everywhere else the element form is what the
mockups are authored in and what gets published, unchanged. `<helmet>` has no attribute form, since
the runtime hoists its contents into `<head>` by tag name.

**The fix for a refusal is always in the harness, never in the vendored file.** These files are
byte-exact against the export and are not edited to work around a build failure. A block the build
refuses is one with no attribute form to lift onto, so the change is a new form in
[`harness/mockup-runtime.js`](harness/mockup-runtime.js) and a case beside the others in
[`internal/mockup/publish.go`](../../../internal/mockup/publish.go)'s `liftOnto`. The failure message
says so, with both file names in it.

### Nothing here is indexable

Every published page carries `<meta name="robots" content="noindex">`, and `MOCK004` fails the build
if one does not. These are fabricated guild rosters, balances and bids for a product that has not
shipped; a search result for one would read as a live instance, and a search snippet does not show
the banner that says otherwise.

It has to be the meta tag on each page, which is why it is worth a gate rather than a convention.
`noindex` does not propagate from `index.html` to the pages it links to. A `robots.txt` would not be
read at all — Pages serves this repo as a *project* site under `/dragonkillparty/`, and crawlers only
fetch `robots.txt` from the origin root, which belongs to a different repository. And Pages does not
let us set an `X-Robots-Tag` header. The meta tag is the only mechanism available.

Published pages fetch the Phosphor icon font from unpkg at runtime. That is a CDN request made by a
static design-reference page, not a vendored dependency — the repo's pinning rules cover CI actions
and the dependency graph, neither of which this touches.

Pages must be enabled once by hand: **Settings → Pages → Build and deployment → Source: GitHub
Actions.**

## Deliberately not vendored

`_ds_bundle.js` (a no-op namespace stub), `support.js`, `ios-frame.jsx` and the `.thumbnail` — the
design tool's harness, replaced as above. `nocturne/` also lists `theme.json`,
`foundations/*.html`, `components/*.html` and `templates/` in its own file table; those are the
design system's demo pages. The one file that matters is `styles.css`, and
[`../09-frontend-and-design-system.md`](../09-frontend-and-design-system.md) transcribes every token
out of it so the doc stands alone.

## Reading a screen

Each `.dc.html` is one component: static markup with `{{ }}` bindings, `<sc-for>` / `<sc-if>` control
flow, and a `DCLogic` class at the bottom holding the mock data and handlers. To find a screen, search
for its `isX` guard — `<sc-if value="{{ isBids }}">` — and read the block. The `DCLogic` class at the
bottom tells you which fields the screen expects; treat those as *shape* hints, not as the API
contract. The API contract is [`../11-ui-backend-contract.md`](../11-ui-backend-contract.md).

Most interactions are visual only. Drag handles, uploaders, checkboxes, the calendar and the widget
grid render their affordances without implementing them — the mockups settle *what* an interaction
looks like and means, never *how* it is wired.

---

Made by **Prokopton** · P99 Green · member of **Legacy of Fire** — carried in the footer of every
surface, and into the shipped product. See [`../10-ui-decisions.md`](../10-ui-decisions.md) §14.
