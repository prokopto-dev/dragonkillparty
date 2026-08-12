# CI/CD and release engineering

**Status:** design. **Audience:** maintainer, contributor, agent.
**Normative tie-breaker:** `docs/design/00-canonical-conventions.md`.

Every claim below that is an assumption about people rather than a decision about software is marked
**[assumption]**. Every rule that has an enforcement mechanism names it; a rule without one is a wish.

---

## 1. Repository strategy — one repository

`prokopto-dev/dragonkillparty` holds the binary, the SPA, the docs, the generated SDKs and the
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
| `homebrew-tap` | Homebrew's tap layout mandates a dedicated repo. Written by goreleaser with a scoped token; humans never touch it. `.goreleaser.yaml`'s `brews[].repository` is the source of truth for its name, and `prokopto-dev/homebrew-tap` is what makes the documented `brew install prokopto-dev/tap/dkp` resolve. |
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

### Merge queue — proposed, then not adopted (issue #101)

This section used to read "turn it on from day one", on a justification that was **not** throughput —
this repo will see 2 to 10 merges a day — but that a merge queue is the only place to run an
expensive check **exactly once per merge** instead of on every push to every PR. Two jobs were
written for that tier and named in `ci-required`: `mq / image-arm64` and
`mq / upgrade-from-latest-release`. (This document also claimed a third, a full cross-browser e2e
run; that one was never written at all.)

**The queue was never turned on, and the two-tier split has been deleted rather than enabled.**
`required_merge_queue` is `null` on `main`, so `merge_group` never fired and neither job ever ran —
while `ci-required` counts `skipped` as success, so both reported satisfied on every PR. Coverage
that is documented, listed in the checks UI and never executed is worse than coverage that is absent,
because nothing distinguishes it from the real thing. The choice was between making the document
true and making the workflow honest, and the workflow won: both jobs and both
`|| github.event_name == 'merge_group'` escape hatches are gone, and
`TestCIWorkflow_NoJob_IsGatedOnMergeGroup` in `test/repo/ci_required_test.go` fails on a third one.

What that costs, stated rather than implied. arm64 is now built only by the release train —
`release.yml`'s per-arch matrix — so an arm64-only breakage surfaces while cutting a release instead
of before the merge (issue #108 proposes a nightly cross-compile). And
`upgrade-from-latest-release` has no call site at all; it was a `notyet` stub waiting on
`release-refdb` either way, so nothing runs less than it did, but the tier it was promised to occupy
in section 7 is now empty (issue #109).

`ci.yml` still declares `on: merge_group:`, and `changes.outputs.deep` still treats a queue run as a
deep run. That pair is the half with no downside: neither is a job condition, so neither can
manufacture a green skip, and if a queue is ever switched on — a repository-settings change, which no
PR review would catch — the whole workflow runs inside it rather than `ci-required` never reporting
and the queue wedging on a check that never arrives.

Adopting the queue later means re-adding the expensive tier in the same change, with the settings it
would want: minimum 1, maximum 5 entries per batch, 5-minute maximum wait, "only merge non-failing
pull requests".

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
| | Nightly: `verify-postgres` `soak-jobs` `upgrade-ladder-enumerate` `test-upgrade-ladder` `verify-image-arm64` `nightly-report` |
| | Fixtures: `fixture-gate` `fixture-build` `fixture-seed` `fixture-capture` `fixture-verify` `fixture-publish` `fixture-manifest` |

---

## 4. The gate inventory

Check names are `group / name` so the PR checks list reads as a hierarchy. Budgets are wall clock on
a warm cache, `ubuntu-24.04`, 4 vCPU **[assumption — measured after Phase 0, not before]**.

| Job | Runs when | Budget | Status |
|---|---|---|---|
| `changes` | always, unskippable | 8 s | required |
| `lint / repo` — architectural gates + licence firewall | always | 15 s | required |
| `lint / go` — gofumpt, golangci-lint | go changed | 75 s | required |
| `lint / web` — eslint, prettier | web changed | 40 s | required |
| `security / licences` — Go runtime graph + JS production graph, closed allowlist | always | 45 s | required |
| `security / govulncheck` — REACHABLE Go vulnerabilities (call-graph) | always | 40 s | required |
| `security / osv` — OSV advisories over `go.mod` **and** `web/pnpm-lock.yaml` | always | 30 s | required |
| `typecheck` — `make vet`: build, vet, staticcheck, tsc | always | 75 s | required |
| `gen / codegen-drift` — `make gen`, then a clean tree | always | 80 s | required |
| `gen / spec-drift` — spec properties codegen cannot see | api/go changed | 30 s | required |
| `test / unit` | go changed | 60 s | required |
| `test / property` — `testing/quick`, base seed printed | go changed | 60 s | required |
| `test / coverage-floor` — `internal/ledger` + `internal/strategy` ≥ 95% | go changed | 60 s | required |
| `test / golden` — plus a non-decreasing fixture count | go changed | 45 s | required |
| `test / integration` — real SQLite, real triggers, goleak | go/db changed | 130 s | required |
| `test / migrations` — fresh install, N-1, row invariants, auto-restore; plus `atlas migrate lint`, **advisory** | db changed | 110 s | required |
| `test / authz-matrix` — every operation × every principal | api/go changed | 60 s | required |
| `api / breaking-change` — oasdiff + sticky changelog comment | api changed | 45 s | required |
| `build / binary` — uploads the artifact everything reuses | always | 165 s | required |
| `build / image` — amd64, `--load`, first-boot smoke | non-draft | 60 s | required |
| `test / e2e` — Playwright, sharded ×2, `--retries=0` | non-draft | 150 s | required |
| `test / importer` — MariaDB fixtures via testcontainers | importer changed + non-draft | 210 s | required |
| `budget / bundle` — SPA initial route ≤ 250 KB gz | web changed | 10 s | required |
| `docs / build` — embed.FS link resolution, `dkp:exec` blocks | docs/go changed | 40 s | required |
| `ci-required` | always | 5 s | **the only check in branch protection** |

There is no "required in queue" tier. Two `mq / *` rows sat here until issue #101; section 2 records
why they were deleted rather than enabled, and #108 and #109 track what they claimed to cover.

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

**`atlas migrate lint` is the one advisory step inside a required job**, and it is advisory *by
construction* rather than by `continue-on-error` — which this workflow bans, and which
`TestCIWorkflow_NoContinueOnError` asserts the absence of. `scripts/migrate-lint.sh` prints Atlas's
destructive / data-dependent / backward-incompatible diagnostics, emits a `::warning::`, and exits 0
in its default `MODE=advise`; `MODE=enforce` fails instead and is already exercised by
`test/repo/migrate_lint_test.go`. Introduced advisory-first (issue #131) because SQLite's 12-step
table rebuild is where Atlas's analyzers are least predictable, and a linter that blocks merges
before it is trusted gets disabled rather than tuned. Promotion is tracked in issue #136, and it is a
one-word change at the call site.

It is **additive**. `atlas migrate lint` knows nothing about forward-only migrations (MIG001),
backtick identifiers (MIG002), the `SHIPPED.lock` frozen-migration rule (MIG003), the fresh-install
schema fingerprint, or append-only triggers surviving a table rebuild — those are this repository's
rules and its own gates keep them. `TestMigrationGates_AtlasLint_IsAdditive` states that in code, so
a later consolidation pass cannot quietly retire the bespoke half.

### The wall-clock budget

> A non-draft PR reaches `ci-required` green in **≤ 6 min p50, ≤ 10 min p95**, measured
> push-to-green. A draft PR reaches lint + unit green in **≤ 90 s**.
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
| Go modules (`$GOMODCACHE`, its own `actions/cache`) | `<os>-<arch>-gomod-<hash of go.sum>` | ~1 GB |
| Go build cache (`$GOCACHE`, rolling) | `<os>-<arch>-gobuild-<toolchain>-<job>-<sha>` | ~200 MB per job lane |
| golangci-lint | `.golangci.yml` + `go.sum` | ~250 MB |
| pnpm store | `<os>-<arch>-pnpmstore-<hash of web/pnpm-lock.yaml>` | ~400 MB |
| Playwright browsers | resolved Playwright version | ~1 GB |
| Docker layers (`type=local`, `mode=max`) | `<os>-<arch>-buildx-<hash of the Dockerfile + both lockfiles>-<sha>` | ~1 GB |

Every one of them except Playwright's is declared in `.github/actions/setup-toolchain` or in
`ci.yml`'s `build / image` block, and `TestSetupToolchain_EveryCacheStep_IsAccountedFor` keeps that
inventory closed: a cache added without a row here is a cache evicting the entries this table
budgets.

**Two classes of cache, two write policies, and the key is what tells them apart.** A cache keyed on
a lockfile hash is immutable: the entry a branch writes is re-read by that branch's every later push,
and once `main` writes it every branch reads it through, so saving from a PR is not waste. That is
the modules and the pnpm store. A cache whose key rolls per commit is the opposite: a PR's write is
hit by nothing, ever, because the next push is a different sha — it burns quota against the shared
10 GB limit while evicting the entries that *are* shared. So a rolling cache **restores everywhere
and writes only on `push:main`** (`actions/cache/restore` off main, which has no post step; the full
`actions/cache` on main, whose post step runs at job end after the work that filled it). That is the
Go build cache and the Docker layers.

**The Go build cache is a different cache from the module cache, and `setup-go` cannot give you just
one of them** (issues #153 and #157). Its `cache:` input archives `$GOMODCACHE` *and* `$GOCACHE`
together under go.sum's hash — the right key for the modules, which change only when go.sum does, and
the wrong one for the build cache, which every source edit invalidates: an immutable key never rolls
forward and is not rewritten once it exists, so each run restores the entry written the first time
that go.sum existed and recompiles everything touched since. Since there is no "modules only" switch,
`setup-toolchain` sets `cache: false` and declares both caches itself: an `actions/cache` on
`go env GOMODCACHE` keyed on go.sum, and a **rolling** `$GOCACHE` cache whose key ends in
`github.sha` so it is never hit exactly and `main` always writes a fresh entry, with `restore-keys`
falling back by prefix to the newest entry from the same job, then to the newest from any job.
Leaving `cache: true` on alongside the rolling cache stored the build cache twice, and nothing ever
read the second copy back.

The `<job>` segment buys precision — `test / integration` builds `-race` objects `test / unit`
never produces — and keeps the several Go jobs of one `main` run from racing to reserve a single
key. It costs one entry per lane: measured cold, this repository's `$GOCACHE` is ~240 MB after
compiling every test binary and ~500 MB once `-race` objects are added, so a lane is roughly 100–200
MB compressed and the steady state is one warm entry per lane. Old per-commit entries are the
least-recently-used things in the pool by construction — nothing ever restores them again — so they
are what LRU evicts first, rather than the pnpm, Playwright or Docker caches that every run touches.

A stale build-cache entry cannot make CI *wrong*, only slower: Go's cache is content-addressed by an
action ID hashing the compiler, the flags and every input, so a mismatched entry is a miss. What a
warm cache *can* do is let `go test` report a false `(cached)` for a gate whose real input a
subprocess read — which is why every `go test` recipe in the Makefile passes `-count=1` and
`test/repo/gate_cache_test.go` holds it there.

**The Docker layer cache is `type=local` for a reason that is worth writing down** (issue #119).
`type=gha` is the obvious backend and is unusable here: buildx authenticates to the Actions cache
service with `ACTIONS_RUNTIME_TOKEN`, which GitHub injects into JavaScript actions and never into a
`run:` step, so a shell invocation of `docker buildx build --cache-to type=gha` has no credentials.
Exposing the token needs a third-party action — a dependency decision, proposed in issue #163, which
also covers `scripts/release-image.sh` passing `--cache-from type=gha` from exactly such a step. So
`build / image` restores an `actions/cache` entry into `/tmp/.buildx-cache` and `make docker` reads
`BUILDX_CACHE_FROM` / `BUILDX_CACHE_TO` — variables buildx does not honour by itself, which is why
they did nothing at all until the recipe was taught to pass them through.

`mode=max`, not `mode=min`: the final stage is `FROM scratch` with three COPYs, so `min` — which
exports only the resulting image's layers — would archive the one part of this build with nothing
expensive in it, leaving the Node and Go builder stages, where all the time goes, uncached.

Its key **rolls, with a content segment in the middle**:
`<os>-<arch>-buildx-<hash of deploy/Dockerfile + both lockfiles>-<sha>`. The rolling tail is not
decoration — Actions cache entries are immutable and `actions/cache` skips its post-job save on an
exact hit, so a key without one is written the first time it exists and never refreshed: `main` would
export a fresh cache, swap it into place, and upload nothing ever again. The content segment is what
the first `restore-key` truncates to, so a fallback lands on the newest entry whose builder stages are
still reusable before it falls back to the newest entry from any. `ci-budget.yml` measures whether the
whole arrangement earns its place.

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
3. **Draft PRs run lint and unit only.** Agents open drafts and mark ready when green locally. This
   lever only works if `ready_for_review` is in `on: pull_request: types:` — without it the expensive
   jobs are not deferred, they are skipped for good (issue #82, and §4's fourth mitigation).
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
   **not** to be in state `skipped`. `test-importer` is deep *and* path-filtered, so it is asserted
   the same way whenever the importer filter also selected — its legitimate skip is "the importer
   did not change", never "this PR was a draft when the run started".

**A fourth mitigation, because three were not enough (issue #82).** `deep` reads
`github.event.pull_request.draft`, so the whole tier depends on the workflow being re-run when the PR
leaves draft. `on: pull_request:` with no `types:` defaults to `[opened, synchronize, reopened]`,
which does **not** include `ready_for_review` — so a PR opened as a draft, marked ready and merged
with no further push reached `main` with `test / e2e`, `test / importer` and `build / image` never
having run, and `ci-required` green throughout. That is the flow this document recommends, not an
unusual one, and neither fallback existed: there is no merge queue, so `merge_group` never fires, and
`push` happens after the merge. `ci.yml` therefore names all four types explicitly — declaring
`types:` replaces the default list rather than extending it, so `synchronize` has to be restated or
CI stops running on pushes. `test/repo/ci_required_test.go` pins the list and, separately, asserts
that every job gated on `deep` is named in mitigation 3.

**And the path filters are held to their own inputs.** A gate that stops running on the files it
polices is the same defect one level down: `test/repo` runs only in jobs gated on the `go` filter,
but several of its suites read `web/` and `docs/design/`, so a web-only PR skipped every one of them
and `ci-required` counted the skips as success (issue #94). Those inputs — the token and component
sheets, `/_design`, the Playwright config and axe allowlist, the font files, `NOTICE`,
`THIRD_PARTY_NOTICES.txt` and the two `.npmrc` files — are pinned to the `go` filter for the same
reason `scripts/**`, `.githooks/**` and the two worked-example documents already are.
`TestCIFilters_GoFilter_SelectsEveryTestRepoInput` names each concrete file rather than the pattern,
so the assertion survives a reformatting of the filter block and fails when a line is deleted.

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
are forward-only and cumulative, so goose replays. Per-PR covers N-1 and the nightly upgrade ladder
covers **every previously released minor**. The rung between them — the real latest release — was the
merge queue's, and has been vacant since issue #101 removed it; issue #109 is where it lands once
`release-refdb` publishes something to upgrade from. Downgrades are refused at boot with a message
naming the correct image tag and the exact snapshot path.

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

### Package visibility is a manual step, and it is the owner's

**A GHCR package is private on its first publish.** Visibility is a repository setting, not anything
in this tree, so nothing here can create it, assert it or fix it — which is exactly why it has to be
written down.

| Package | Must be | Who reads it anonymously |
|---|---|---|
| `ghcr.io/prokopto-dev/dragonkillparty` | public | every officer running the documented `docker pull …:1`; `release.yml`'s smoke job and `nightly-verify.yml` authenticate, so only the user is exposed |
| `ghcr.io/prokopto-dev/dkp-refdb` | public | `nightly-verify.yml`'s `upgrade-ladder`, and anyone reproducing an upgrade locally |
| `ghcr.io/prokopto-dev/dkp-fixtures` | public | the importer matrix **on fork PRs**, which have no token for a private package (§1) |

**Who and when: the repository owner, once per package, immediately after that package's first
successful publish** — GitHub → the repository → Packages → the package → Package settings → Change
visibility → Public, and confirm the package is linked to this repository so its permissions are
inherited rather than hand-maintained. There is no earlier moment to do it: a package does not exist
until something pushes to it.

**Logging in is not the answer to this.** Issue #113 fixed a `release.yml` smoke job that pulled the
published digest anonymously, and `TestReleaseWorkflows_EveryGHCRJob_LogsInFirst` holds every
GHCR-touching job to it. That makes *CI* correct whatever the visibility is, deliberately: it means a
private package no longer fails the release gate, so the first evidence of a private package would
otherwise be an officer's `docker pull` returning `denied` / `manifest unknown` after a green release.
The check belongs in the release checklist instead — `.claude/skills/cut-release/SKILL.md` §1 carries
the row, and it is a `gh` command, not a click:

```bash
gh api /users/prokopto-dev/packages/container/dragonkillparty --jq .visibility   # want: public
```

### Multi-arch by cross-compilation, joined with `imagetools` — never QEMU

Every other stack in the comparison builds multi-arch images inside a QEMU-emulated container.
Emulated builds run roughly 10–25× slower than native, turning a two-minute image stage into 20–50
minutes, which is why so many projects either drop arm64 or stop releasing often. **That cost is a
property of interpreted and JIT-heavy toolchains, not of multi-arch.**

Go cross-compiles. `GOOS=linux GOARCH=arm64 go build` runs at native speed on the same amd64 runner,
`modernc.org/sqlite` is pure Go so CGO stays off, and the final image is `FROM scratch` with three
COPY layers. The SPA is built once, in a Node stage on `$BUILDPLATFORM`, and staged into
`internal/ui/dist` **before** any `go build` — JavaScript is architecture-independent, so one Vite
build feeds every architecture's binary, and running node under emulation for arm64 would be QEMU by
another name. `deploy/Dockerfile` asserts what it staged (`scripts/verify-spa-dist.sh`) rather than
trusting it: a placeholder embed compiles and boots perfectly, which is how issue #55 survived a
whole phase. So: build every architecture natively on amd64, push one image per architecture, and
join them with `docker buildx imagetools create`, which writes a manifest list and executes no
instruction of the target architecture. Whole multi-arch stage: **about 90 seconds**, mostly Vite.

arm64 *runners* are used for exactly one job in the entire project — the post-publish smoke test,
which must exercise the real binary on real hardware. Building is emulation-free; verifying is not
allowed to be.

Platforms: `linux/amd64`, `linux/arm64`, `linux/arm/v7`. The last exists for older Raspberry Pis,
which are over-represented in this audience **[assumption — named in
`docs/development/verify-before-phase-0.md`]**; a pure-Go binary makes a third architecture cost one
more `go build`.

**arm64 is built before the release train, not only during it.** `nightly-verify.yml`'s
`image / arm64-cross` runs `make verify-image-arm64` — `PLATFORM=linux/arm64 make docker` on the
usual amd64 runner, then `scripts/verify-image-arch.sh`. Without it, arm64 is built in exactly one
place (release.yml's per-arch matrix, which is `fail-fast`), so a `GOARCH`-conditional build tag, a
base-image assumption or a cgo dependency arriving through a bump is discovered while cutting a
release, after the tag exists (issue #108; the merge-queue job that claimed this coverage never ran,
and #101 deleted it). The script checks two things and the second is the load-bearing one: buildx
writes the image config's architecture from `--platform`, so `docker image inspect` reports what the
build was *asked* for, while the ELF machine type of `/usr/local/bin/dkp` inside the image reports
what it *produced*. Nothing in CI boots an arm64 image — that is what would need the QEMU banned
above — so those bytes are the only evidence available that `TARGETARCH` is still reaching
`go build`. Nightly rather than per-PR by §4's rule: an arm64-only break is not deterministic given a
typical diff, and it costs a whole image build.

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
`dkp doctor`, an upgrade from the previous release's refdb, and **the SPA the image actually
serves** — `GET /` must return the built bundle, not `internal/ui`'s committed placeholder. Only
then does `imagetools create` advance `:1.5`, `:1` and `:latest`.

That last one was learned the expensive way. "It boots" was the whole of this gate until issue #55,
and an image whose build never ran Vite boots flawlessly: it passes the healthcheck, answers
`/readyz`, prints its version, and serves "web UI not yet built into this binary" to every officer
who opens it. `internal/ui` falls back to `index.html` with a 200 for any path it does not have, so
nothing that reads status codes can tell the two apart — only the bytes can (`scripts/smoke-spa.sh`).

If smoke fails: the immutable tag stays (for forensics), the moving tags never move, the GitHub
Release stays a draft, and an issue is filed. **Nobody running `:1` ever sees a build that failed to
boot.**

### Signing and attestation — both, because they serve different verifiers

```
cosign sign --yes ghcr.io/prokopto-dev/dragonkillparty@${DIGEST}   # keyless: GH OIDC -> Fulcio -> Rekor
actions/attest-build-provenance  subject-digest=...    # SLSA v1, `gh attestation verify`
actions/attest-sbom              sbom-path=sbom.spdx.json
```

`permissions: { id-token: write, packages: write, attestations: write, contents: write }` on the
release jobs only.

The verification snippet in the README must use the right identity regexp, which is the part people
get wrong: **the certificate identity is the workflow file at the tag ref**, and it changes if the job
is refactored into a reusable workflow.

```
cosign verify ghcr.io/prokopto-dev/dragonkillparty:1.5.0 \
  --certificate-identity-regexp '^https://github\.com/prokopto-dev/dragonkillparty/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

`release-smoke` runs those exact commands against the published digest, so a wrong README snippet is
discovered in CI rather than by a user. That half of the script is **opt-in**, on
`VERIFY_SUPPLY_CHAIN=1`: only a tagged release has a signature and an attestation to check, and the
`:edge` channel — which runs the same script — has neither. Opted in, a missing `cosign` or `gh` is a
failure and never a skip, so `release.yml`'s `smoke` job installs cosign and holds
`attestations: read`. Gating on tool presence instead was issue #107: `gh` is preinstalled on
GitHub-hosted runners and `cosign` is not, so the check silently skipped on the channel that needed
it and ran on the channel that could not pass it.

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
0. finish an interrupted restore, if a previous boot was killed part-way through one
1. read dkp_meta.schema_version
2. version > this binary's max  -> REFUSE TO START, naming the correct image tag
                                   and the exact snapshot path
3. append-only triggers absent  -> LOG AT ERROR AND CONTINUE. This boot did not cause it,
                                   so there is nothing to restore and nothing to name;
                                   refusing here locks an officer out over old damage
4. pending && DKP_AUTO_MIGRATE  -> a. VACUUM INTO /data/backups/pre-<ver>-<ts>.db, zstd
                                   b. record the ledger tables and triggers that exist
                                      now — the baseline (c.iv) compares against
                                   c. goose Up ONE migration, then all four checks:
                                        i.   restore PRAGMA foreign_keys = ON
                                        ii.  PRAGMA integrity_check
                                        iii. PRAGMA foreign_key_check
                                        iv.  append-only survival — no ledger table and
                                             no ledger trigger present before this
                                             migration may be absent after it
                                      repeat until nothing is pending
                                   d. on failure anywhere in (c): AUTO-RESTORE the
                                      snapshot, exit 1, print the failing migration and
                                      the rollback command
5. audit-log applied version, actor="boot", binary version, duration
6. serve
```

The four checks are four distinct failure classes and only the second is "your database is corrupt".
(i) exists because `PRAGMA foreign_keys` is connection state and a `NO TRANSACTION` migration that
forgets to re-assert it disarms every later migration in the same boot, silently, since nothing
reports a pragma. (iii) exists because `integrity_check` does not validate foreign keys, so a
rebuild that copied rows in the wrong order passes it. (iv) exists because a rebuild that forgets to
re-create a ledger table's `BEFORE UPDATE OR DELETE` triggers passes all three of the others, loses
no row, and returns a ledger whose history is editable; it compares against the pre-migration state
rather than a full catalogue, so a database that arrived already degraded is not refused an upgrade
for damage that predates the binary. The operator-facing messages are in
[Upgrade and backup](../operations/upgrade-and-backup.md#what-happens-at-boot).

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
5. **The reference-database ladder.** Every release publishes
   `ghcr.io/prokopto-dev/dkp-refdb:<version>` — a deterministic seeded database, a few MB, produced
   by booting the just-released binary. Then:
   `test / migrations` uses N-1 per PR and nightly's `upgrade-ladder` iterates **every published minor
   to HEAD** (the real-latest-tag rung between them belonged to the deleted merge queue — issue #109).
   That converts "any 1.x upgrades to any later 1.y" from a promise into a nightly-verified property
   for about ten two-minute jobs.
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

**Detail is disclosed to nobody until the operator says otherwise.** `DKP_READYZ_DETAIL` takes
`never` (the default), `local` or `always`, and nothing else grants a `detail` — not the peer address,
and certainly not a forwarded header. The reason is the deployment this document recommends: behind a
reverse proxy on the same host, `RemoteAddr` is `127.0.0.1` for every caller on the internet, so a
rule that read the peer address was disclosing to all of them on the majority of real installs. An
address cannot answer "is this caller trusted" where a proxy owns the socket, so the endpoint waits to
be told. (An authenticated, authorized caller is the other signal that could grant it. There is no
auth package before Phase 2, and `/readyz` is a probe a load balancer calls unauthenticated by design,
so it is not one today; when it exists it becomes a fourth answer here, not a loosening of these
three.)

`check` and `state` are public under every policy — monitoring has to see that something is wrong, and
the 503 says that much anyway. What an unasked-for caller does not get is *which* thing and its shape.

`local` is the pre-existing rule, kept as an opt-in for the instance that is genuinely exposed
directly (`docker run -p 8080:8080`): the peer is loopback or private space — RFC 1918 and the RFC
4193 IPv6 unique-local range, which is the same decision and not a widening of it, since `fc00::/7` is
what an IPv6-only Docker network or Kubernetes cluster assigns — **and** nothing in the request says
the peer is relaying somebody else. **Link-local (`169.254/16`, `fe80::/10`) is excluded**, because
those are reachable by anything sharing a layer-2 segment — another tenant on a cloud VLAN, a device
on the office wifi — which is not the "has shell access, or is on the network this instance is managed
from" audience the disclosure is for. CGNAT (`100.64/10`) is excluded for the same reason. Setting
`local` is the operator asserting that nothing sits in front of the listener; a layer-4 proxy, or a
layer-7 one that strips its own forwarded headers, makes that assertion false and is invisible from
inside the process.

`always` is how an operator behind a proxy gets the string back — the fail-closed default costs the
legitimate case, monitoring that really is on the guild's own network but arrives through the proxy,
and this is the documented way to pay for it. It belongs on an instance whose `/readyz` strangers
cannot reach.

There is **one deliberate exception**, and it survives every policy including the default: the
migrations-pending body `{"check":"migrations","state":"pending","command":"dkp migrate"}` is public.
It tells an unauthenticated caller only that the instance is mid-upgrade — which the 503 already
tells them — and the command it names is in the published documentation. The SPA renders it as a
banner for an operator who may not have shell access at that moment, and gating it behind a
disclosure policy would mean the banner is blank on every instance that never set the variable, which
is almost all of them. A `/readyz` that tells the public internet your schema version, disk state or
worker lag is a reconnaissance endpoint, and those checks are the ones the redaction is for.

> Landed in Phase 0 PR 3: the migrations check and its public body. Landed with the append-only
> readiness check (#59): a caller-based redaction, applied to every `detail` except the exception
> above. Replaced by the policy above in **#74**, because a caller-based rule is not a control where a
> proxy owns the socket — the peer address it read is `127.0.0.1` for the whole internet in the shape
> this document recommends. `DKP_READYZ_DETAIL` is unset on an upgrade, so an instance that relied on
> the old behaviour stops disclosing until an operator chooses `local` or `always`; that is the
> intended direction of the change.
>
> Under `local`, both halves of the old rule still apply and are **two** tests:
>
> 1. the peer is loopback or private space, from `RemoteAddr`; **and**
> 2. nothing in the request says the peer is relaying somebody else — any `X-Forwarded-*` header
>    (the whole family by prefix, not three chosen members: `X-Forwarded-Port` alone is the same
>    fact as `X-Forwarded-For`), or `Forwarded`, `X-Real-IP`, `CF-Connecting-IP`, `True-Client-IP`.
>
> The **presence of the header key** is what redacts — present-and-empty counts, since a proxy that
> forwards an empty value has still told you it is a proxy — and the contents are never read. Reading
> them would invert the control, since a client-supplied header that *grants* disclosure is a header
> anyone can forge, while one that only *withholds* it buys an attacker nothing but a shorter response.
>
> Residual gap, now scoped to `local` rather than to the default: a layer-4 proxy, or a layer-7 one
> configured to add no headers at all, is invisible to (2), so an operator who sets `local` in front of
> one discloses through it. Recovering the real client instead needs `DKP_TRUSTED_PROXIES` — already
> specified in [`install-docker.md`](../getting-started/install-docker.md), empty by default and
> ignoring forwarded headers entirely while it is — plus PROXY-protocol support on the listener. That
> is the shared resolved-client-IP helper rate limiting, the audit log's actor IP and session binding
> each need too, and it is not invented for one call site; it is filed as #98. The remaining checks
> land with the code that can fail them.

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
