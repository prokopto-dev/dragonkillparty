# CI/CD and release engineering

**Status:** design. **Audience:** maintainer, contributor, agent.
**Normative tie-breaker:** `docs/design/00-canonical-conventions.md`.

Every claim below that is an assumption about people rather than a decision about software is marked
**[assumption]**. Every rule that has an enforcement mechanism names it; a rule without one is a wish.

---

## 1. Repository strategy — one repository

`dragonkillparty/dragonkillparty` holds the binary, the SPA, the docs, the generated SDKs and the
fixture definitions. Three things live outside it and only three.

The decision is not a monorepo preference. **Four of the five architectural CI gates are
cross-language invariants**, and each one degrades into a version-skew problem the moment the repo
splits:

| Gate | Spans | What splitting costs |
|---|---|---|
| Spec drift — `openapi.json` regenerated from Go, diff-gated | `internal/api` → `openapi/` | Becomes "repo B is one commit behind repo A". The gate can no longer fail the PR that caused it. |
| SDK regeneration diff | Go structs → `clients/ts`, `clients/python` | An `operationId` rename silently renames a public SDK method in a repo nobody opened. |
| Authorization matrix — every operation × every principal | `internal/api` + `internal/authz` + the integration suite | Requires two repos' HEADs to run together. Nobody will. |
| Traffic conformance — routes the SPA actually calls ⊆ the spec | `web/` + `openapi/` | Same. |
| Migration ladder | `db/migrations-sqlite/` + published refdb artifacts | Works either way, but the fixtures need the schema. |

The agent argument reinforces it. `internal/api/EXAMPLE_ENDPOINT.md` is a **seven-file change in one
commit** — schema, query, sqlc, handler, spec, client, test. An agent cannot land that atomically
across four repositories; it would either leave the repo red between PRs or invent version
negotiation nobody asked for. One repo means one `AGENTS.md`, one `make check`, one `go.mod`, one
lockfile, and one diff a reviewer can hold in their head.

**Docs must be in-repo** because the binary serves them from `embed.FS` at `/docs`. Splitting them
forces a build-time fetch, which breaks the offline build that `FROM scratch` exists to protect.

**Large binary fixtures do not justify a split and do not justify git-lfs.** LFS bandwidth is charged
to the repo owner and bites forks hardest — precisely the contributor you want to keep. Instead:
`test/fixtures/eqdkp/<version>/` holds a Dockerfile and seed scripts (small, reviewable, diffable),
and `.github/workflows/fixtures.yml` publishes the built MariaDB data directories to GHCR as
**public OCI artifacts** that fork PRs pull anonymously.

**The three exceptions.**

| Repo | Why it is separate |
|---|---|
| `dkp-p99-seed` | Different legal posture — P99 wiki content with no declared licence, Darkpaw IP. Core must never take a build- or test-time dependency on it, and `lint / repo` greps for any import or fetch of it from `internal/` or `web/`. |
| `homebrew-dkp` | Homebrew's tap layout mandates a dedicated repo. Written by goreleaser with a scoped token; humans never touch it. |
| Upstream template PRs (Coolify, Unraid CA) | Somebody else's repo by definition. `deploy/coolify/` and `deploy/unraid/` hold the canonical source; a nightly job diffs the upstream copy and files an issue on drift. |

**Rejected:** a `dkp-api-spec` repo (the spec is a build output, not a source), a `dkp-clients` repo
(generated, must move in lockstep), a `dkp-docs` repo (embedded in the binary).

---

## 2. Branching

Trunk-based, short-lived branches, squash-merge, no `develop` branch. Gitflow's `develop` exists to
stage a release cut on a calendar; this project releases when the release-please Release PR is
merged, and its staging is the `:edge` channel built from `main`. A `develop` branch would be a
second thing to keep green with no consumer.

The one long-lived branch that is justified: **`release-1.x` maintenance branches, created lazily** —
only when a security fix must ship for 1.x after 2.0 exists. Created from a label, never
pre-emptively.

### Branch protection on `main`

| Setting | Value |
|---|---|
| Pull request required | Yes, 1 approving review, stale approvals dismissed on push |
| CODEOWNERS review | Required on owned paths |
| Conversation resolution | Required |
| Required status checks | **Exactly two: `ci-required` and `DCO`.** Not fifteen. |
| Linear history | Required; merge commits and rebase-merge disabled at the repo level |
| Bypass, including administrators | **Not allowed** |
| Force push, deletion | Blocked |
| Tag protection | `v*`, creatable only by the release App |

"Do not allow bypassing, including administrators" matters more here than on a typical project. An
agent operating with a maintainer's token is exactly the actor that "admins can bypass" was not
designed for.

**Direct pushes to `main` are forbidden for everyone.** The two cases usually carved out are handled
by automation instead: the release commit arrives as a normal, normally-reviewed Release PR, and tags
are created by the GitHub App rather than a human `git push --tags`. An emergency is `git revert` in
a PR with an `expedite` label, which bypasses review through a ruleset exemption for the revert App
only — a mechanism, not a judgement call.

**Conventional commits are enforced on the PR title only.** Squash-merge makes the PR title the
commit subject, which is what release-please parses. Enforcing them on every individual commit
punishes contributors for messy WIP and is the most common reason a first PR gets abandoned
**[assumption]**. `BREAKING CHANGE:` goes in the PR body, which becomes the squash commit body.

**DCO, not a CLA.** Mechanism: the DCO GitHub App as a required check, a local `prepare-commit-msg`
hook adding `Signed-off-by`, and a one-line remediation in `CONTRIBUTING.md`. Accept the consequence
knowingly: **the licence can never be changed without contacting every contributor.** If a commercial
edition is ever conceivable, that decision must be made before the first external PR, not after.

### Merge queue

Turn it on from day one. The justification is **not** throughput — this repo will see 2 to 10 merges
a day. It is that the merge queue is the only place to run an expensive check **exactly once per
merge** instead of on every push to every PR. Three jobs run only in `merge_group`: the arm64 image
build, the upgrade test against the actual latest released reference database, and the full
cross-browser e2e run. That two-tier split is what lets PR CI stay under six minutes without
weakening the merge gate.

Settings: minimum 1, maximum 5 entries per batch, 5-minute maximum wait, "only merge non-failing pull
requests".

---

## 3. Workflow inventory

```
.github/
  workflows/
    ci.yml               pull_request | merge_group | push:main   the main gate
    release.yml          push: tags 'v*'                          the release train
    nightly-verify.yml   schedule 03:17 UTC | dispatch            the expensive truth
    fixtures.yml         workflow_dispatch                        EQdkp fixture OCI artifacts
    release-please.yml   push:main                                maintains the Release PR
    edge.yml             push:main after ci                       :edge image + demo refresh
    codeql.yml           weekly + push:main                       Go + JS static analysis
    scorecard.yml        weekly                                   OpenSSF Scorecard
    stale.yml            weekly                                   issue and PR hygiene
    ghcr-prune.yml       weekly                                   delete pr-* and stale edge-* tags
    docs-publish.yml     push:main paths docs/**                  GitHub Pages
    regen-bot.yml        push: renovate/**                        runs `make gen`, commits result
    pr-title-lint.yml    pull_request_target (title only)
    backport.yml         pull_request closed + backport/1.x label
    ci-budget.yml        weekly                                   enforces the wall-clock SLO
  actions/
    setup-toolchain/     composite: Go + pnpm + every cache — ONE place
    build-dkp/           composite: web build -> go build -> upload artifact
    seed-refdb/          composite: boot binary, seed, VACUUM INTO, zstd
  CODEOWNERS  renovate.json5  labeler.yml  size-budget.json
  PULL_REQUEST_TEMPLATE.md  ISSUE_TEMPLATE/
```

The four files that exist today — `ci.yml`, `release.yml`, `nightly-verify.yml`, `fixtures.yml` — are
**skeletons**: real YAML with the final job graph, triggers, concurrency and gate logic, where every
step shells out to a `make` target that is still a `notyet` stub. Each carries a header naming the
roadmap phase that fills it in.

`pr-title-lint.yml` is the **only** workflow using `pull_request_target`, and it must never check out
PR code. `pull_request_target` plus `actions/checkout` of `head.sha` is the canonical OSS repo
compromise, and an agent asked to "make this work on fork PRs" will reach for it.

### The make-target contract

`AGENTS.md` says a command CI needs must exist as a Makefile target. The workflow skeletons
deliberately name targets that do not exist yet. **That is a debt with a due date, not an
exception:** Phase 0 PR 1 adds every target below to the Makefile, and `make verify-commands` —
which already exists — is what makes the debt impossible to forget.

The `AGENTS.md` canonical-commands table stays at its current size. It lists the commands a
**contributor** is told to run; the targets below are invoked by CI or by those commands, and adding
one does not add a row. A target that a contributor is expected to type does need both, in the same
change.

| Existing | New in Phase 0 PR 1 |
|---|---|
| `vet` `test-unit` `test` `test-importer` `build` `docker` `verify-generated` | `lint-repo` `lint-go` `lint-web` `verify-spec` `test-property` `test-golden` `test-migrations` `test-authz` `test-e2e` `api-breaking` `api-changelog-comment` `budget-bundle` `docs-build` `docs-links` `smoke-local` `test-upgrade` `verify-action-pins` |
| | Release: `release-version` `release-image` `release-manifest` `release-sign` `release-sbom` `release-refdb` `release-smoke` `release-promote` `release-promote-rc` `release-notes` `release-failure-issue` |
| | Nightly: `verify-postgres` `soak-jobs` `upgrade-ladder-enumerate` `test-upgrade-ladder` `nightly-report` |
| | Fixtures: `fixture-gate` `fixture-build` `fixture-seed` `fixture-capture` `fixture-verify` `fixture-publish` `fixture-manifest` |

---

## 4. The gate inventory

Check names are `group / name` so the PR checks list reads as a hierarchy. Budgets are wall clock on
a warm cache, `ubuntu-24.04`, 4 vCPU **[assumption — measured after Phase 0, not before]**.

| Job | Runs when | Budget | Status |
|---|---|---|---|
| `changes` | always, unskippable | 8 s | required |
| `lint / repo` — grep gates + licence firewall | always | 15 s | required |
| `lint / go` — gofumpt, golangci-lint | go changed | 75 s | required |
| `lint / web` — eslint, prettier | web changed | 40 s | required |
| `typecheck` — `make vet`: build, vet, staticcheck, tsc | always | 75 s | required |
| `gen / codegen-drift` — `make gen`, then a clean tree | always | 80 s | required |
| `gen / spec-drift` — spec properties codegen cannot see | api/go changed | 30 s | required |
| `test / unit` | go changed | 60 s | required |
| `test / property` — `testing/quick`, base seed printed | go changed | 60 s | required |
| `test / coverage-floor` — `internal/ledger` + `internal/strategy` ≥ 95% | go changed | 60 s | required |
| `test / golden` — plus a non-decreasing fixture count | go changed | 45 s | required |
| `test / integration` — real SQLite, real triggers, goleak | go/db changed | 130 s | required |
| `test / migrations` — fresh install, N-1, row invariants, auto-restore | db changed \| merge_group | 110 s | required |
| `test / authz-matrix` — every operation × every principal | api/go changed | 60 s | required |
| `api / breaking-change` — oasdiff + sticky changelog comment | api changed | 45 s | required |
| `build / binary` — uploads the artifact everything reuses | always | 165 s | required |
| `build / image` — amd64, `--load`, first-boot smoke | non-draft | 60 s | required |
| `test / e2e` — Playwright, sharded ×2, `--retries=0` | non-draft | 150 s | required |
| `test / importer` — MariaDB fixtures via testcontainers | importer changed \| merge_group | 210 s | required |
| `budget / bundle` — SPA initial route ≤ 250 KB gz | web changed | 10 s | required |
| `docs / build` — embed.FS link resolution, `dkp:exec` blocks | docs/go changed | 40 s | required |
| `mq / image-arm64` | merge_group | 70 s | required in queue |
| `mq / upgrade-from-latest-release` | merge_group | 90 s | required in queue |
| `ci-required` | always | 5 s | **the only check in branch protection** |

**Required versus advisory, as a rule rather than a list:** a check is required if and only if its
failure is (a) deterministic given the PR's diff and (b) actionable by the PR author. Image CVE
scanning fails both — a new CVE in `ca-certificates` appears on the world's schedule, not the
contributor's — so it runs nightly and files an issue. The same reasoning keeps external link
checking out of PR CI entirely.

Bundle size is required *despite* sounding advisory, because unlike a CVE feed the number is fully
determined by the diff and fully actionable. Breaching it legitimately is a one-line edit to
`.github/size-budget.json`, reviewed in the diff by a human — which turns a size regression into a
decision instead of a drift.

**The corollary that stops this list metastasising: any advisory check nobody has acted on in 90 days
gets deleted.** `ci-budget.yml` reports advisory-check action rates alongside the wall-clock numbers.

### The wall-clock budget

> A non-draft PR reaches `ci-required` green in **≤ 6 min p50, ≤ 10 min p95**, measured
> push-to-green. A draft PR reaches lint + unit green in **≤ 90 s**. The merge queue adds **≤ 6 min**.
> **No single job may exceed 5 minutes** — one that does is sharded or moved to nightly.

The critical path is `changes → build / binary (165 s) → test / e2e (150 s)` ≈ 5.5 minutes.
Everything else fits underneath it. This is why `build / binary` uploads an artifact instead of every
job rebuilding: a second Go + Vite build would put e2e at nine minutes on its own.

`ci-budget.yml` queries the Actions API weekly over the last 200 `ci-required` conclusions and files
an issue when the target is breached. A performance budget nobody measures is a wish. The first
response to a breach is "which job did we add, and does it earn 30 seconds on every PR forever, or
does it belong in nightly?"

### Concurrency, caching, matrices

```yaml
concurrency:
  group: ci-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}
```

Cancel-on-superseded is the biggest single minute saver in an agent-driven repo, where six pushes in
four minutes is normal. **Never cancel `push:main`, `merge_group` or tags** — a cancelled merge-queue
run wedges the queue and a cancelled release is a half-published release.

Caches, budgeted against the 10 GB per-repository limit (LRU eviction; cache thrash makes CI
*slower*, which is the failure mode people miss):

| Cache | Key | Budget |
|---|---|---|
| Go modules + build cache | `go.sum` + Go version | ~1.5 GB |
| golangci-lint | `.golangci.yml` + `go.sum` | ~250 MB |
| pnpm store | `pnpm-lock.yaml` | ~400 MB |
| Playwright browsers | resolved Playwright version | ~1 GB |
| Docker layers | `type=gha` | ~1 GB |

Docker layer caching has one rule that is routinely got wrong: **`cache-to` only on `push:main`;
`cache-from` everywhere.** Actions caches are branch-scoped with read-through to the default branch,
so a PR branch's writes are invisible to every other PR and are evicted almost immediately —
`cache-to` on a PR burns quota for nothing while evicting the caches that *are* shared. `mode=max` is
also wrong: the final stage is `FROM scratch` with three COPYs, so all the value is in the Node and
Go builder stages.

The matrix is deliberately anaemic: one Go version (pinned in `go.mod` with `GOTOOLCHAIN=local`, so a
runner image bump cannot silently change compilers), one Node version, one OS. The only PR-time
matrix is `e2e.shard: [1,2]`. The OS matrix that *is* needed lives in nightly: `dkp.exe` is a
first-class distribution channel and `modernc.org/sqlite` file locking, path separators and
`t.TempDir()` cleanup all behave differently on Windows.

### Free-tier minutes: five levers, the first worth more than the other four combined

1. **Make the repository public on day one.** Standard runners are free with no minute cap on public
   repos, which the Apache-2.0 choice already implies. What then actually binds is not minutes: it is
   20 concurrent jobs on Free, the 10 GB cache, and contributor patience.
2. **Cross-compile, never QEMU.** See §7.
3. **Draft PRs run lint and unit only.** Agents open drafts and mark ready when green locally.
4. **`push:` restricted to `main` and tags.** Running on both `pull_request` and `push` for every
   branch doubles the bill for zero information.
5. **Every scheduled workflow guards on the repository owner.** Otherwise every fork runs the nightly
   suite forever, which is rude and becomes a support burden the first time a forker's bill surprises
   them.

### Fork PRs

`ci.yml` uses `pull_request`, never `pull_request_target`, so untrusted code never sees a secret.
`permissions: contents: read` at workflow level, elevated per job with the minimum scope.
`build / image` builds but pushes only when the head repo is this repo. Fixture and refdb OCI
artifacts are **public packages** so anonymous pulls work from forks — without that, `test / importer`
is red for every external contributor, and the importer is the whole migration story.

### The aggregate gate, and the hole it opens

Path-filtered jobs plus required status checks is the classic way to wedge a repo: a skipped job never
reports and the PR waits forever. `ci-required` with `if: always()` over `needs.*.result` fixes that,
but opens a second hole — `skipped` must count as success, so a job accidentally skipped by a wrong
`if:` passes silently.

Three mitigations, all of which assert the **shape** of the run rather than only its colour:

1. `changes` has no `if:` and cannot be skipped. It is the fixed point everything else is measured
   against.
2. Jobs that are unconditional (`changes`, `lint-repo`, `typecheck`, `codegen-drift`, `build-binary`)
   are asserted to be exactly `success`, not "success or skipped".
3. When `changes.outputs.deep == 'true'`, the deep jobs (`build-image`, `test-e2e`) are asserted
   **not** to be in state `skipped`.

---

## 5. SemVer for a self-hosted app and its API

**The app's version and the API's version are different contracts, and a breaking API change does not
bump the app's major.** `/api/v1` is additive-only forever; a breaking change mints `/api/v2`, which
ships in an app **minor**, with v1 alive for at least 18 months carrying `Deprecation` and `Sunset`
headers and a per-operation metric so an admin can see whether anything still calls it.

App SemVer governs four operator-facing surfaces:

1. **Deployment** — env var names, CLI subcommands and flags, default port, `/data` volume path,
   entrypoint, supported platforms, required external services (currently none).
2. **Data** — `/data` layout, backup archive format, `dkp restore` reading older archives.
3. **API** — the *set of mounted API versions*, plus the EQdkp compat shim.
4. **Upgrade** — "`docker pull && docker restart` works, unattended, from any earlier 1.x."

**MAJOR** — an operator must take a manual action, or a supported deployment stops working after
`docker pull && docker restart`. Exhaustively: removing or renaming an env var or CLI flag without at
least one minor of aliasing plus a `WARN` log plus a `dkp doctor` notice; changing a default in a way
that changes behaviour; dropping a published platform; unmounting `/api/v1` or removing the compat
shim; requiring a new external dependency; a migration that cannot be preceded by an automatic
snapshot or that needs a documented manual step; raising the oldest directly-upgradable version above
the current major's `x.0.0`; changing the backup archive format so a new `dkp restore` cannot read old
archives.

**MINOR** — new features; new API operations, optional fields, enum values; new env vars with working
defaults; new strategies; automatic lossless migrations; a new `/api/vN` mount; deprecations
*announced*.

**PATCH** — bug fixes, security fixes, dependency bumps, performance, docs — and, importantly, **a
patch may contain a migration**, because some bugs cannot be fixed without one. A patch migration must
be additive only, enforced by the protected-table row invariants in `test / migrations` rather than by
review.

**Support window, as a promise CI verifies:** *any 1.x upgrades directly to any later 1.y.* Migrations
are forward-only and cumulative, so goose replays. Per-PR covers N-1; the merge queue covers the real
latest release; the nightly upgrade ladder covers **every previously released minor**. Downgrades are
refused at boot with a message naming the correct image tag and the exact snapshot path.

### The `!breaking-api` protocol

`oasdiff breaking` runs against `origin/main`'s committed `openapi.json`. A breaking delta fails
unless the PR carries the `!breaking-api` label **and** modifies `docs/api-changelog.md`. The label is
author-settable, so it is not a security control — it is a "did you notice" control, and CODEOWNERS on
`docs/api-changelog.md` converts it into a review control. Separately, `oasdiff changelog` is posted
as a sticky PR comment on every spec-changing PR, so reviewers read the API delta in English rather
than JSON.

---

## 6. Release automation

### Tool: release-please, in manifest mode

| Rejected | The reason that actually decides it |
|---|---|
| semantic-release | Publishes on every merge to `main`. For a library that is fine; here **every release is an upgrade an operator must perform**, and fourteen releases in a week trains guild officers to ignore the update banner. No human checkpoint, no batching. |
| changesets | Requires a `.changeset/*.md` per contributor. npm-shaped, weak Go support, and — decisively in an agent-heavy repo — it adds a metadata file agents forget, then a bot comments, then the agent writes a wrong one. It solves independent-versioning problems this repo does not have. |

release-please parses conventional commits from squashed PR titles, maintains a **Release PR** a human
merges when they choose, batches N merges into one release, natively understands the `go` release type
alongside `node` and `python` for the SDKs, and supports **linked versions** across all three.

Linked versions are not cosmetic: `@dragonkillparty/sdk@1.4.2` is by construction the client generated
from server 1.4.2's spec. That deletes an entire category of support question and makes the SDK
version a diagnostic signal in a support bundle.

**The App, and the footgun it exists to avoid.** A tag created with the default `GITHUB_TOKEN` **does
not trigger workflows**, so `release.yml` would silently never run. A GitHub App with `contents:
write`, `pull_requests: write` and `issues: write` mints a short-lived token instead. A classic PAT is
the wrong answer: it belongs to a human who may leave the guild, and it cannot be scoped to one
repository with a short life.

### Channels

| Channel | Tags | Trigger | Guarantees |
|---|---|---|---|
| `edge` | `:edge`, `:edge-<sha7>` | every green `main` | **None.** An `edge` build may apply a migration a *later* `edge` build changes the shape of, and the binary refuses to downgrade — so an operator tracking `edge` can be stranded. Say this in three places. No GitHub Release. |
| `rc` | `:1.5.0-rc.1`, `:next` | tag on a `release-1.5` branch | Feature-frozen. **Migrations final.** N-1 upgrade tested. Marked pre-release. |
| `stable` | `:1.5.0`, `:1.5`, `:1`, `:latest` | merge the Release PR | The full §5 contract. |

The in-app update checker exposes a channel setting: `stable` by default, `rc` opt-in, `edge` requires
typing the word `edge` into a confirmation field.

### The release body is assembled, not copied

`make release-notes` emits, on top of release-please's changelog:

- **Migrations in this release** — names, a flag on any that rewrite a table (SQLite's 12-step
  rebuild), and a duration measured against the largest refdb in CI.
- **API changes** — `oasdiff changelog` between the two tags' specs, in plain language.
- **Configuration changes** — diffed from the generated `docs/reference/configuration.md`.
- **Breaking changes** — every `BREAKING CHANGE:` trailer, verbatim.
- **Action required** — either "None; `docker pull … && docker restart dkp`" or a numbered list.
- **Verify this release** — the exact digest, the `cosign verify` command, the `gh attestation
  verify` command.

---

## 7. Container publishing

**Registry: GHCR only.** Not Docker Hub: anonymous pull rate limits will bite users and the resulting
support tickets are unfalsifiable from our side. GHCR is free for public packages with no pull limits,
and it is already where the fixture and refdb artifacts live.

### Multi-arch by cross-compilation, joined with `imagetools` — never QEMU

Every other stack in the comparison builds multi-arch images inside a QEMU-emulated container.
Emulated builds run roughly 10–25× slower than native, turning a two-minute image stage into 20–50
minutes, which is why so many projects either drop arm64 or stop releasing often. **That cost is a
property of interpreted and JIT-heavy toolchains, not of multi-arch.**

Go cross-compiles. `GOOS=linux GOARCH=arm64 go build` runs at native speed on the same amd64 runner,
`modernc.org/sqlite` is pure Go so CGO stays off, and the final image is `FROM scratch` with three
COPY layers. So: build every architecture natively on amd64, push one image per architecture, and
join them with `docker buildx imagetools create`, which writes a manifest list and executes no
instruction of the target architecture. Whole multi-arch stage: **about 90 seconds**, mostly Vite.

arm64 *runners* are used for exactly one job in the entire project — the post-publish smoke test,
which must exercise the real binary on real hardware. Building is emulation-free; verifying is not
allowed to be.

Platforms: `linux/amd64`, `linux/arm64`, `linux/arm/v7`. The last exists for older Raspberry Pis,
which are over-represented in this audience **[assumption — named in
`docs/development/verify-before-phase-0.md`]**; a pure-Go binary makes a third architecture cost one
more `go build`.

### Tags

| Tag | Points at | Advanced when |
|---|---|---|
| `:1.5.0` | immutable digest | at publish, always |
| `:1.5` | latest patch of 1.5 | **after smoke** |
| `:1` | latest 1.x — the documented default | **after smoke** |
| `:latest` | latest stable, any major | **after smoke** |
| `:next` | latest RC | at RC publish |
| `:edge`, `:edge-<sha7>` | latest green main | every main merge |
| `:1.5.0-debug` | alpine variant with a shell | at publish |
| `:pr-1234` | maintainer-authored PR heads only | per push, pruned at 14 days |

Documentation pins `:1` everywhere. `:latest` is discouraged in the README with a one-line reason: it
will cross a major and change the deployment contract.

### The rule that makes the release safe

**Moving tags advance only after the smoke job passes.** `release.yml` publishes the immutable tag,
then `smoke` pulls that **digest** on both amd64 and arm64 runners and runs first boot, `/readyz`,
`dkp doctor`, and an upgrade from the previous release's refdb. Only then does `imagetools create`
advance `:1.5`, `:1` and `:latest`.

If smoke fails: the immutable tag stays (for forensics), the moving tags never move, the GitHub
Release stays a draft, and an issue is filed. **Nobody running `:1` ever sees a build that failed to
boot.**

### Signing and attestation — both, because they serve different verifiers

```
cosign sign --yes ghcr.io/<org>/dkp@${DIGEST}          # keyless: GH OIDC -> Fulcio -> Rekor
actions/attest-build-provenance  subject-digest=...    # SLSA v1, `gh attestation verify`
actions/attest-sbom              sbom-path=sbom.spdx.json
```

`permissions: { id-token: write, packages: write, attestations: write, contents: write }` on the
release jobs only.

The verification snippet in the README must use the right identity regexp, which is the part people
get wrong: **the certificate identity is the workflow file at the tag ref**, and it changes if the job
is refactored into a reusable workflow.

```
cosign verify ghcr.io/<org>/dkp:1.5.0 \
  --certificate-identity-regexp '^https://github\.com/<org>/dragonkillparty/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

`release-smoke` runs those exact commands against the published digest, so a wrong README snippet is
discovered in CI rather than by a user.

### Size budgets — `.github/size-budget.json`

| Artifact | Hard cap | Growth gate |
|---|---|---|
| `dkp` binary, linux/amd64, embedded SPA + tzdata | 45 MB uncompressed | +3% vs last release |
| `:X.Y.Z` scratch image | 30 MB compressed / 65 MB uncompressed | +5% |
| `:X.Y.Z-debug` alpine image | 45 MB compressed | +5% |
| SPA initial route | 250 KB gzipped | +5% |

---

## 8. Database migration policy in CI

**Forward-only. No `down` migrations ship, ever.**

This reverses the default instinct, and the justification is specific to this audience: a `down`
migration is code that runs exactly once, in an emergency, on data that cannot be reproduced, written
months earlier by someone who never tested it against the real shape. A restore from a snapshot taken
200 ms before the migration is *always* correct and requires no code. `-- +goose Down` blocks contain
`SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst')`,
and `lint / repo` fails any migration whose Down block contains DDL.

**Migrate on boot, default `true`.** The alternative — a required manual `dkp migrate` — guarantees
that some fraction of volunteer operators `docker pull`, restart, and get a container that refuses to
start with an error they will not understand at 1 a.m. after a raid. The risk of automatic migration
is real and is addressed by making it *safe*, not by making it *manual*:

```
1. read dkp_meta.schema_version
2. version > this binary's max  -> REFUSE TO START, naming the correct image tag
                                   and the exact snapshot path
3. pending && DKP_AUTO_MIGRATE  -> a. VACUUM INTO /data/backups/pre-<ver>-<ts>.db, zstd
                                   b. goose Up per migration, PRAGMA integrity_check after each
                                   c. on failure: AUTO-RESTORE the snapshot, exit 1, print the
                                      failing migration and the rollback command
4. audit-log applied version, actor="boot", binary version, duration
5. serve
```

`DKP_AUTO_MIGRATE=false` exists for operators who want a maintenance window; in that mode `/readyz`
returns 503 with `{"check":"migrations","state":"pending","command":"dkp migrate"}` and the SPA
renders a banner with the exact command.

### Six layers between an upgrade and lost guild data, five of them mechanical

1. **Snapshot before migrate, with auto-restore.** An integration test deliberately corrupts a
   migration, boots, and asserts the process exits non-zero *and* the database is byte-identical to
   the snapshot.
2. **Protected-table row invariants.** `test / migrations` records `count(*)` plus a column-level
   digest for `ledger_batch`, `ledger_entry`, `raid`, `tick`, `attendance`, `award`, `adjustment`,
   `person`, `character` and `item` before and after every migration, and **fails on any decrease**.
   This is the fence that matters most on SQLite, because Atlas's 12-step `ALTER TABLE` rebuild is
   exactly where a mistyped column list silently drops a column's data — and exactly the kind of
   change an agent produces confidently.
3. **The two-release destructive rule.** A migration containing `DROP TABLE`, `DROP COLUMN` or
   `DELETE FROM` (outside `import_staging`) fails CI unless it carries
   `-- dkp:destructive-approved: #<issue>` and the referenced issue confirms the previous *minor*
   already stopped writing to that object. Release N deprecates; release N+1 drops. `lint / repo`
   enforces the marker; a human enforces the semantics.
4. **Trigger survival.** A test asserts the append-only `BEFORE UPDATE OR DELETE` triggers on
   `ledger_batch` and `ledger_entry` still fire **after the full migration set**, not merely after the
   migration that created them. Table rebuilds drop triggers; this is how you find out.
5. **The reference-database ladder.** Every release publishes `ghcr.io/<org>/dkp-refdb:<version>` — a
   deterministic seeded database, a few MB, produced by booting the just-released binary. Then:
   `test / migrations` uses N-1 per PR, `mq / upgrade-from-latest-release` uses the real latest tag in
   the queue, and nightly's `upgrade-ladder` iterates **every published minor to HEAD**. That converts
   "any 1.x upgrades to any later 1.y" from a promise into a nightly-verified property for about ten
   two-minute jobs.
6. **Backups on by default**, and mechanised rather than repeated: nightly `VACUUM INTO` + zstd, 14
   dailies and 8 weeklies, `dkp doctor` reporting `backup_age_seconds`, the migration snapshot taken
   whether or not the operator remembered, and the admin upgrade banner showing the most recent
   backup's timestamp next to "Update available". Documentation says "take a backup before upgrading"
   exactly once; the software makes it true whether they read it or not.

**The ladder rots silently if you let it.** A release that fails after the image step but before
`refdb` leaves a hole nobody notices until an upgrade breaks. So `upgrade-ladder-enumerate` enumerates
GitHub **Releases** and fails if any released minor lacks a refdb artifact, rather than iterating
whatever happens to exist.

**Postgres, post-1.0.** Nightly `verify-postgres` builds with the CI-only tag, asserts
`var _ Queries = (*pggen.Queries)(nil)` compiles, applies `db/migrations-postgres/`, and asserts
`atlas schema inspect` on both dialects normalises to the same committed logical fingerprint. Ninety
nightly seconds that keep the post-1.0 door open at zero PR-time cost.

---

## 9. Dependencies and the volunteer maintenance budget

### Renovate, not Dependabot

Only one of the two is configured — `.github/renovate.json5`. Running both produces duplicate PRs and
duplicate reviewer load, and the whole point is to reduce reviewer load.

| | Dependabot | Renovate |
|---|---|---|
| Grouping across ecosystems | No | Yes — one PR for "frontend tooling" |
| Scheduling window | Coarse | `* 2-7 * * 1` — one batch, Monday early |
| Concurrency limit | No | `prConcurrentLimit: 3` |
| Backlog absorber | No | The dependency dashboard: one issue listing everything pending |
| Per-package human approval | No | `dependencyDashboardApproval` on the high-risk list |
| Action digest pinning + maintenance | Partial | `helpers:pinGitHubActionDigests` |

The dashboard is the budget knob. It absorbs the backlog so the PR list stays short enough that people
still open it. **Dependabot security alerts stay enabled** — they are free and open no PRs unless
asked; Renovate raises the actual security PRs via `osvVulnerabilityAlerts`.

Three choices worth defending: `rangeStrategy: "pin"` everywhere, because this is a shipped
application and a caret range means two operators building from source get different binaries;
`dependencyDashboardApproval` on River, `riversqlite`, `modernc.org/sqlite`, goose, Huma, Go, Node and
React, which is the mechanism implementing "pin River exactly" for an early-preview job driver; and
json5 rather than json, so every rule carries its reason where an agent reading the file will see it.

Renovate's hosted app cannot run `postUpgradeTasks`, so a sqlc or Huma bump that changes generated
output arrives permanently red on `gen / codegen-drift`. `regen-bot.yml` handles it: on push to
`renovate/**`, `if: github.actor == 'renovate[bot]'`, run `make gen`, commit with the App token.

### Stale policy

**Bug reports are never auto-closed.** Closing a real bug because a volunteer was busy is a
community-destroying move that is invisible until people stop reporting. What *is* auto-closed is the
class where silence genuinely means resolved or abandoned:

| Label | Warn | Close | Reason |
|---|---|---|---|
| `needs-info` | 14 d | +7 d | `not_planned` — "comment to reopen, no ceremony" |
| `question` / `support` | 30 d | +7 d | `completed`, linked to Discussions |
| `cannot-reproduce` | 21 d | +14 d | `not_planned` |
| `bug` `enhancement` `parity` `parser-bug` | — | **never** | labelled `dormant` at 180 d, listed in the monthly triage issue |
| PRs with `changes-requested` and no author activity | 30 d | +30 d | `not_planned` — "reopen any time" |

**The labels this table is keyed on are code.** They exist because `.github/labels.yml` declares
them and `make labels-sync ARGS=--apply` pushed that file at repo settings — not because somebody
typed them into the settings page once. The reason is that the drift is invisible from GitHub's
side: an issue form may declare any label it likes and GitHub **silently drops** the ones that do
not exist, so for a while every one of the five forms applied nothing at all and this policy was
keyed on four labels nobody had created ([#43](https://github.com/prokopto-dev/dragonkillparty/issues/43)).
`test/repo/labels_test.go` fails `make check` when a form declares a label the manifest does not
carry, which is the half that runs on every PR with no network. The sync script never deletes: a
label removed from settings is removed from every issue that carried it, and that is not
recoverable from a manifest.

`parser-bug` issues are filed by the in-app "report this line as a parser bug" button with a redacted
line pre-filled. They are **free golden-file test cases** and must never be closed unread; the triage
step is literally "add the line to `test/golden/`, watch it fail, fix it".

### The budget

**Target: ≤ 4 hours per week of total maintainer time** across CI, dependencies, releases and triage
**[assumption — this is a target chosen to force trade-offs, not a measured figure]**.

| Activity | Budget | Mechanism that makes it hold |
|---|---|---|
| Dependency review | 20 min | Weekly window, 3 concurrent PRs, automerge on low risk, dashboard approval on high risk |
| Issue triage | 30 min | One slot, driven by `is:open label:needs-triage` — every form applies that label and the triage step is removing it. Templates force version, deployment mode and `dkp doctor` output up front |
| PR review | 90 min | Squash-only, one approval, narrow CODEOWNERS. The architectural gates mean a reviewer checks *intent*, not conformance |
| Release | 15 min × ~2/month | Merge the Release PR, read the generated upgrade notes |
| CI maintenance | 30 min | One composite action for toolchain setup, workflow LOC capped at ~600, action SHAs maintained by Renovate |
| Flaky-test debt | 0 min steady state | See below |
| Community, docs, incident slack | 60 min | — |

**Zero-tolerance flake policy**, because this is the line that decides whether the budget holds at
month 18. CI retries are **banned** — `--retries=0` in Playwright, no retry actions, no
`go test -count`. A test that fails twice in seven days on `main` is *quarantined*: moved behind
`//go:build flaky` or `test.describe.fixme`, an issue auto-filed with both failure logs, and added to
nightly's `flaky-quarantine` job which runs the set 20× and reports a rate. Retrying converts a
four-hour debugging cost into an unbounded recurring one — and, specific to this project, **a retried
test is a test an agent learns to trust when it should not.**

**The deletion rule, which keeps the budget from ratcheting:** *any CI check whose failure a maintainer
cannot diagnose in 60 seconds is deleted.* Every required job prints, on failure, the exact command to
reproduce it locally. `gen / codegen-drift` prints `run 'make gen' and commit`; `lint / repo` prints
the offending `file:line` and the rule id; `test / migrations` prints which table lost rows and in
which migration. This is a testable property, and nightly's `error-quality` job greps every job's
failure path for a reproduce command.

---

## 10. Observability for self-hosts

The framing that decides every choice here: **you have no access to production, ever.** Every decision
is really a decision about what a guild officer can copy-paste into a public GitHub issue at midnight
without leaking their guild's roster.

**Structured logs.** `log/slog`, JSON to stdout by default, human-readable when stdout is a TTY.
`DKP_LOG_LEVEL`, `DKP_LOG_FORMAT`. Request fields: `ts`, `level`, `msg`, `request_id` (a ULID echoed
in `X-Request-Id` and in every RFC 9457 problem body, so a user's screenshot is a grep key), `route`
— the **OpenAPI path template**, never the raw path, which keeps cardinality bounded and keeps ULIDs
out of logs — `method`, `status`, `dur_ms`, `principal_kind`, `token_id` (never the token),
`person_id`, `sql_count`, `bytes`.

Redaction is a `slog.ReplaceAttr` chain dropping any key matching
`(?i)password|token|secret|cookie|authorization|pepper|session`, **plus an integration test asserting
it**, plus a `lint / repo` grep banning those identifiers as `slog` attribute keys. Redaction that is
only a convention is not redaction.

**Health endpoints** follow canonical conventions §13 exactly: `/healthz` never touches the database
and is the container `HEALTHCHECK`; `/readyz` checks DB reachability, schema version, the ledger's
append-only protection, worker heartbeat, free disk and outbox lag, and returns 503 with a JSON body
naming the failing check *and its fix*.

The checks are an **ordered ladder** and the body reports the first one that is not ready, because the
migrations-pending body below is a wire contract that a pending instance has to keep answering
whatever else is true of its database. `state` is `pending`, `failed` — the check could not be
evaluated — or `degraded`: an evaluated check with a bad answer that will keep having that answer
until a human acts. The ledger's append-only protection is the first `degraded` check
(`{"check":"ledger_append_only","state":"degraded"}`): the boot path refuses a migration that drops
one of the four triggers, but a database that *arrived* without one boots anyway, and logs it once.
Reporting it on every probe is the difference between detecting that a guild's ledger became editable
and somebody finding out.

Detail is disclosed only to loopback and RFC-1918 callers, **with one deliberate exception**: the
migrations-pending body `{"check":"migrations","state":"pending","command":"dkp migrate"}` is public.
It tells an unauthenticated caller only that the instance is mid-upgrade — which the 503 already
tells them — and the command it names is in the published documentation. The SPA renders it as a
banner for an operator who may not have shell access at that moment, and gating it behind a source
address would mean the banner is blank for exactly the person who needs it. A `/readyz` that tells
the public internet your schema version, disk state or worker lag is a reconnaissance endpoint, and
those checks are the ones the redaction is for.

> Landed in Phase 0 PR 3: the migrations check and its public body. Landed with the append-only
> readiness check (#59): the caller-based redaction, applied to every `detail` except the exception
> above, from `RemoteAddr` and never from `X-Forwarded-For` — a client-supplied header would let anyone
> unredact this endpoint. Behind a same-host reverse proxy every caller therefore looks like loopback;
> closing that needs a configured trusted-proxy list, which is filed rather than guessed at. The
> remaining checks land with the code that can fail them.

**`/metrics`** is off by default per canonical conventions §14. The metric set is small and every
entry maps to a support question people actually ask:

```
dkp_build_info{version,commit,go_version}         dkp_sse_connections
dkp_http_requests_total{route,method,status}      dkp_sse_events_dropped_total
dkp_http_request_duration_seconds{route}          dkp_outbox_lag_seconds
dkp_sql_statements_total{route}   # N+1 tripwire  dkp_jobs_total{queue,kind,state}
dkp_ledger_batches_total{pool,kind}               dkp_job_duration_seconds{kind}
dkp_ledger_verify_drift_total                     dkp_job_dead_letter
dkp_backup_age_seconds                            dkp_db_size_bytes / dkp_wal_size_bytes
dkp_api_deprecated_operation_total{operation_id}  # is anything still on v1?
dkp_compat_shim_requests_total{function}          # can we drop the shim yet?
```

Ship `deploy/grafana/dkp.json` and `deploy/prometheus/alerts.yml` with five alerts — `BackupStale`,
`LedgerVerifyDrift`, `OutboxLagging`, `NotReady`, `JobsDeadLettered`. Two hundred lines that make the
project legible to the ops-literate minority who, not coincidentally, file the best bug reports you
will ever receive.

**Telemetry is opt-in, `DKP_TELEMETRY=off` by default.** Four reasons, only one of which is "privacy
is nice":

1. **This audience.** People who self-host chose it partly to stop software phoning home, and P99
   operates in a grey-legal emulator community with an earned aversion to outbound reporting from
   anything touching the game. Default-on here is not a faux pas; it is an identity violation.
2. **Legal.** "Anonymous" install-level telemetry still carries a source IP, which is personal data
   under GDPR, and consent buried in a config default is not consent. A large share of EQdkp Plus's
   install base is German **[assumption — EQdkp Plus is German-first; the install-base split is
   unverified]**, which is the worst jurisdiction to get this wrong in.
3. **Trust asymmetry.** One front-page thread about default-on telemetry costs more adoption than
   telemetry will ever return. You are competing with a Google Sheet; the only thing you have that a
   Sheet does not is trustworthiness.
4. **It corrupts the data.** Opt-out telemetry is blocked at the firewall by exactly the sophisticated
   operators whose configurations you most want to learn from.

When enabled, once per 24 h: a UUID minted at opt-in time (rotatable and deletable, derived from
nothing), version, `GOOS/GOARCH`, container-vs-binary, bucketed scale, which strategies are
configured, which auth providers are enabled, whether the importer ran and from which EQdkp version,
and a boolean per optional feature. **Never** a guild name, domain, URL, member name, item name or DKP
value. The admin UI shows the exact JSON payload before you opt in, and `dkp telemetry show` prints
it — **the payload is the consent interface.**

Keep the **update check** rigorously separate and say so: a plain `GET` for a JSON file, no body, no
identifier beyond a `dkp/1.5.0 (linux/amd64)` User-Agent, opt-out via `DKP_UPDATE_CHECK=false`.
Conflating the two — or letting a reader think you did — is how projects get accused of lying about
telemetry they never sent.

**`dkp support-bundle`** is the primitive that makes remote support possible:
`dkp doctor --json`, build info, `/readyz` detail, config with every secret rendered as
`<set>`/`<unset>` and every URL reduced to scheme+host, schema version and the applied-migration list
with durations, per-table row **counts** (never rows), job stats and the last 50 failures (error
strings only), the last 500 log lines through the same `ReplaceAttr` chain the runtime uses, SQLite
`PRAGMA integrity_check` and WAL size, free disk, clock skew, reverse-proxy header *shapes*, the
served `openapi.json`, and the last import report's summary. Excluded: the database, any ledger row,
any character or person name, any token even hashed, TLS material, the session table.

Two properties make that trustworthy rather than merely well-intentioned. `--dry-run` prints the
manifest of what *would* be included before writing anything. And — this belongs in the **required**
suite — an integration test seeds an instance with canary strings (`CANARY-GUILD`, a canary token, a
canary character, a canary item, `canary.example`), generates a bundle at both redaction levels, and
greps every file in the zip for every canary. Any hit fails the build. That test is what converts "the
support bundle is safe to attach to a public issue" from a claim into a fact, and it is the single
sentence in the issue template that will actually get people to attach one.

---

## 11. The three things most likely to go wrong

1. **The generated-file gate becomes the most-hated check in the repo.** `gen / codegen-drift` will
   fail on roughly one in four early PRs from contributors who did not run `make gen` **[assumption]**.
   Mitigations: a local pre-commit hook runs `make gen`; the failure message is one line with one
   command; `regen-bot.yml` is extended to offer a "run `make gen` for me" reaction on any PR whose
   only failure is drift. If it still generates complaints after three months, the answer is a bot
   that pushes the regeneration — **not** weakening the gate.
2. **The refdb ladder rots silently.** Mitigated by enumerating Releases rather than artifacts (§8).
3. **The wall-clock budget erodes one 30-second job at a time.** Every individual addition is
   justified; the aggregate is a 20-minute gate and a dead contributor pipeline. `ci-budget.yml` is
   the only defence, and it works only if a breach is treated as a bug with an owner.
