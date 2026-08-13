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
| `esbuild` | `0.28.2` | GHSA-67mh-4wv8-2f99 — the dev server answers any origin's request and returns the response, so any website could read files from a developer's machine. Fixed in 0.25.0. Issue #134. | `vite` (7.3.6 declares `^0.27.0 \|\| ^0.28.0`) | Vite's declared range starts at or above 0.28.2, which is already true of 7.3.6 — this row goes when a Vite bump makes the pin redundant rather than merely satisfied. |
| `js-yaml` | `4.3.1` | GHSA-8rgc-vjgv-hhqv — quadratic CPU on a crafted document. Fixed in 4.3.1. Issue #135. | `@redocly/openapi-core` (1.34.18 depends on **exactly** `4.3.0`), reached through `openapi-typescript` | `openapi-typescript` is bumped to a release whose Redocly already depends on ≥ 4.3.1. |

`js-yaml` is deliberately **outside** its parent's declared range: forcing a patch release over an
exact pin is the standard use of an override, and 4.3.1 is a patch of the same major. It is recorded
here rather than waived silently, because "outside the parent's range" is exactly the state the first
rot mode above describes, and the difference between this row and that failure is that somebody
decided it.

## What does not belong here

A version bump that `package.json` can express directly. An override is for a package this workspace
does not depend on — if it appears in `dependencies` or `devDependencies`, change it there, where
pnpm will tell you when a parent disagrees.

## What the gate checks

`test/repo/web_overrides_test.go`, offline, in `make test`:

- every override in `web/package.json` has a row here, and every row has an override — a stale row is
  as bad as a missing one, since it is the row that carries the removal condition;
- `web/pnpm-lock.yaml`'s own `overrides:` block matches `package.json`'s, so an override added
  without re-locking fails rather than being silently inert;
- every row names a parent and a removal condition, and cites an issue;
- every version the lockfile resolves for an overridden package IS the pinned version — an override
  that only partly took effect leaves a second version in the tree, which is the shape a pin quietly
  stops covering anything.

What it does **not** check is that a pin sits inside the range its parent declares. That needs the
parents' own manifests, which live in `node_modules` rather than in the lockfile, so it is a gate
with a dependency install behind it — filed separately rather than half-built here.
