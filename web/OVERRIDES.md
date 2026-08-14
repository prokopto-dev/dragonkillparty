# pnpm overrides — the reviewed register

Every entry in `web/package.json`'s `pnpm.overrides` block is listed here, with why it exists and
what removes it. `test/repo/web_overrides_test.go` fails when the two disagree, so an override cannot
be added, changed or forgotten without this file changing in the same pull request.

**An override is a resolution nothing else verifies.** It forces a version onto a package's whole
subtree — including subtrees whose parent never declared support for it — and pnpm accepts that
without complaint. It is the right tool for a transitive dependency that cannot be bumped any other
way, and it rots in two directions (issue #168):

1. **Outside the parent's range.** A later, unrelated bump can move a parent to a major that wants a
   different version, and the pin keeps forcing the old one. The build may even work.
2. **Outliving its reason.** An override pinned to an exact version *prevents* a later security patch
   from being picked up. The entry added to fix a CVE becomes the thing blocking the next one, and
   `security / osv` would report the advisory with no hint that a line in `package.json` is why.

So the rule is the one `osv-scanner.toml` already lives under: **a deliberate exception with nothing
forcing a revisit becomes permanent.** Each row below names the condition that clears it. Clearing
one is: bump the parent, delete the override, run `pnpm install --frozen-lockfile=false` in `web/`,
confirm the resolved version is at or above the pin, delete the row.

## Current overrides

| Package | Pinned | Why it exists | Parent that forces it | Removed when |
|---|---|---|---|---|
| `esbuild` | `0.28.2` | GHSA-67mh-4wv8-2f99 — the dev server answers any origin's request and returns the response, so any website could read files from a developer's machine. Fixed in 0.25.0. Issue #134. | `vite` (7.3.6 declares `^0.27.0 \|\| ^0.28.0`) | Vite's declared range floors at or above 0.28.2. It does not today — 7.3.6's floor is 0.27.0, so the pin is *satisfied* by the range while still being the only thing keeping 0.27.x out. This row goes when a Vite bump makes it redundant rather than merely satisfied, and `TestWebOverrides_EveryOverride_StillForcesAResolution` is what notices. |
| `js-yaml` | `4.3.1` | GHSA-8rgc-vjgv-hhqv — quadratic CPU on a crafted document. Fixed in 4.3.1. Issue #135. | `@redocly/openapi-core` (1.34.18 depends on **exactly** `4.3.0`), reached through `openapi-typescript` | `openapi-typescript` is bumped to a release whose Redocly already depends on ≥ 4.3.1. |

`js-yaml` is deliberately **outside** its parent's declared range: forcing a patch release over an
exact pin is the standard use of an override, and 4.3.1 is a patch of the same major. It is recorded
here rather than waived silently, because "outside the parent's range" is exactly the state the first
rot mode above describes, and the difference between this row and that failure is that somebody
decided it. Its row in the next table is what says so to the gate.

## Sanctioned range exceptions

The pin normally sits **inside** every range its parents declare, and
`TestWebOverrides_EveryPin_SatisfiesItsParentsDeclaredRange` fails when it does not. Each row here
waives one `(package, parent)` pair — a break somebody decided, rather than one an unrelated bump
arrived at.

The **declared range is part of the waiver**, not a note about it. A parent that moves to a different
range is a different decision, and the row stops matching until someone re-reads it; that is what
keeps this table from becoming a list of package names nobody has revisited. The reverse holds too:
`TestWebOverrides_EveryRangeException_IsStillBroken` fails on a row whose break has healed, so a row
cannot outlive the thing it waives. Same shrink-only rule as `web/e2e/axe-allowlist.json` and
`osv-scanner.toml`. Parents are keyed by name, without a version: versions churn on every unrelated
bump and a waiver keyed to one would need re-approving by whoever happened to run `pnpm update`.

Reasons must not contain a `|`, and must cite an issue.

| Package | Parent | Parent's declared range | Why the break is deliberate |
|---|---|---|---|
| `js-yaml` | `@redocly/openapi-core` | `4.3.0` | Forcing a patch release over an exact pin is the standard use of an override: 4.3.1 is a patch of the same major, carries the GHSA-8rgc-vjgv-hhqv fix, and Redocly's exact `4.3.0` is a pin rather than a statement that 4.3.1 is incompatible. Decided in issue #135; the exception clears when `openapi-typescript` reaches a Redocly that already wants ≥ 4.3.1. |

## What does not belong here

A version bump that `package.json` can express directly. An override is for a package this workspace
does not depend on — if it appears in `dependencies` or `devDependencies`, change it there, where
pnpm will tell you when a parent disagrees.

## What the gate checks

`test/repo/web_overrides_test.go`, in `make test`. Four checks read files only:

- every override in `web/package.json` has a row here, and every row has an override — a stale row is
  as bad as a missing one, since it is the row that carries the removal condition;
- `web/pnpm-lock.yaml`'s own `overrides:` block matches `package.json`'s, so an override added
  without re-locking fails rather than being silently inert;
- every row names a parent and a removal condition, and cites an issue;
- every version the lockfile resolves for an overridden package IS the pinned version — an override
  that only partly took effect leaves a second version in the tree, which is the shape a pin quietly
  stops covering anything.

Three more read the **installed tree** (issue #186), because the lockfile records resolved versions
and not the ranges each parent declares — those live in each parent's own `package.json` under
`node_modules`:

- the pin satisfies every range its parents declare, unless the pair has a row in "Sanctioned range
  exceptions" that also records the range it breaks;
- no waiver row outlives its break;
- the override still forces something: some parent's declared range would otherwise admit a version
  *below* the pin. When none would, the override is redundant — and a redundant exact pin is not
  inert, it is what blocks the package's next patch.

These three need `pnpm install` to have run. They skip on a laptop that has not installed web
dependencies and **fail** under CI, where `test / integration` and `suite / shuffled` both install
them; locally `make check` reaches them through `web-deps`.

The redundancy check is sufficient, not necessary: it fires when redundancy is provable from the
declared ranges alone. Proving that a re-resolution *without* the override would land at or above the
pin needs the registry, and that is a network test this repository does not have.
