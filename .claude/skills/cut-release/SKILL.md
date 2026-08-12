---
name: cut-release
description: Prepare and verify a Dragon Kill Party release. Use when the release-please Release PR is ready to review, when preparing an RC, or when verifying that a published release's images, binaries, attestations and reference database all landed. Every release is an upgrade a volunteer officer must perform.
argument-hint: "[stable | rc | verify <version>]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(gh *), Bash(git log *), Bash(git diff *), Bash(git tag -l *), Bash(oasdiff *), Bash(docker pull *), Bash(cosign *), Bash(make check), Bash(make shipped-lock-seal), Bash(make release-shipped-lock)
---

# Cut a release

Releases are **"merge the Release PR"**. release-please maintains it; a human merges it; a GitHub App
creates the tag; `release.yml` does the rest.

**You do not tag, push, publish or deploy.** Your job is to make the Release PR correct and to prove
the published artefacts are complete. Both are checklist work where a missed row ships a broken
upgrade to people with no ops skills.

---

## Steps

### 1. Confirm the trunk is releasable

- [ ] `main` is green on `ci-required`. (There is no merge queue to drain first — issue #101 deleted
      the configuration for one that was never turned on.)
- [ ] No open PR carries `!breaking-api` that was meant for this release.
- [ ] `nightly-verify.yml`'s last run is green, including `upgrade-ladder`, `image / arm64-cross` and
      the OS matrix (`integration-windows`, `integration-macos`). `dkp.exe` is a first-class channel;
      SQLite file locking and `t.TempDir()` cleanup differ on Windows. `image / arm64-cross` is the
      only arm64 image build outside the release train itself (issue #108), and release.yml's image
      matrix is `fail-fast` — a red one there stops the release after the tag already exists.
- [ ] **The GHCR packages that already exist are public.** A package is **private on first publish**
      and stays that way until the owner changes it by hand, so this row is load-bearing on the first
      release and after any package is republished under a new name. The release train authenticates
      (issue #113), so a private **product image** passes every gate and fails the officer running the
      README's `docker pull`; a private `dkp-fixtures` fails the other way, in `test / importer`,
      which pulls anonymously so a fork PR can.

      ```bash
      for p in dragonkillparty dkp-fixtures; do
        printf '%-16s %s\n' "$p" "$(gh api /users/prokopto-dev/packages/container/$p --jq .visibility)"
      done   # want: public, public
      ```

      Both exist before any tag does — `edge.yml` pushes the image on every merge to `main`, and
      `fixtures.yml` pushes the fixtures the importer matrix needs — so a `404` here is not a
      first-release condition, it means that workflow has never run successfully. A `private` means
      fix it before the tag: GitHub → the repository → Packages → the package → Package settings →
      Change visibility → Public.

      **`dkp-refdb` is deliberately not in that loop.** It does not exist until `release.yml`'s
      `refdb / publish` job creates it, so on a first release it *must* 404 here; its visibility is
      step 8's job, after the package exists. `docs/design/06-cicd-and-release.md` §7 records why each
      one must be anonymously pullable.

### 2. Check the version bump against the SemVer policy

The app's version and the API's version are **different contracts**. A breaking API change does not
bump the app's major — `/api/v1` is additive-only forever, and a breaking need mints `/api/v2` inside
an app *minor*.

App SemVer governs four operator-facing surfaces: **deployment** (env vars, CLI flags, port, `/data`
path, platforms), **data** (`/data` layout, backup archive format), **API** (the set of mounted API
versions plus the compat shim), and **upgrade** (`docker pull && docker restart` works unattended
from any earlier 1.x).

| Bump | Trigger |
|---|---|
| **MAJOR** | An operator must take a manual action, or a supported deployment stops working after pull-and-restart. Removing or renaming an env var or CLI flag without ≥1 minor of aliasing + `WARN` + a `dkp doctor` notice; changing a default in a way that changes behaviour; dropping a published platform; unmounting `/api/v1` or the compat shim; requiring a new external dependency; a migration that cannot be preceded by an automatic snapshot; raising the oldest directly-upgradable version; a backup format a new `dkp restore` cannot read. |
| **MINOR** | New features, new API operations, optional fields, enum values, new env vars **with working defaults**, new strategies, automatic lossless migrations, a new `/api/vN` mount, announced deprecations. |
| **PATCH** | Bug fixes, security fixes, dependency bumps, performance, docs — **and a migration, if it is additive only.** The `test / migrations` protected-table row invariants enforce that mechanically. |

If release-please's computed bump disagrees with this table, the PR *title* of an earlier merge was
wrong. Fix the changelog entry and say so; do not silently override the bump.

### 3. Seal the shipped-migration manifest

Every migration in the tree ships with this tag, so every one of them must be recorded as shipped
**before** the tag exists:

```bash
make shipped-lock-seal      # appends a `filename sha256` row for each migration not yet listed
make release-shipped-lock   # the assertion release.yml's `prepare` job will make
```

Commit the `db/migrations-sqlite/SHIPPED.lock` diff **in the Release PR**. It is sealed here rather
than by CI because nothing pushes to `main`, and a record written by the job that consumes it is not
a record.

From that commit on, those files are frozen: `MIG003` in `make lint-repo` fails on every PR that
edits or deletes one, and on any PR that rewrites a row rather than appending — it compares the
manifest against its merge-base version. A migration that ships without a row is not "unrecorded" —
it is a file everyone's tooling will happily let the next contributor edit, on databases that have
already run it.

This is the one PR where `SHIPPED.lock` legitimately changes, so it is the one PR where the diff on
it deserves reading line by line. Appended rows only; anything else is a rewrite the gate will
reject on every subsequent PR.

If `make release-shipped-lock` fails at tag time, the release stops in `prepare`, before any image,
binary or moving tag exists. That is the gate working; seal the manifest and re-tag.

### 4. Assemble the Upgrade Notes block

The GitHub Release body is **assembled, not copied**. `scripts/upgrade-notes.sh` produces it; verify
each section is present and true:

| Section | Check |
|---|---|
| **Migrations in this release** | Names, a flag on any that rewrite a table (SQLite's 12-step rebuild), and a duration estimated against the largest refdb in CI |
| **API changes** | `oasdiff changelog` between the two tags' specs, in plain language |
| **Configuration changes** | Diffed from `docs/reference/configuration.md`, itself generated from the `Config` struct and diff-gated |
| **Breaking changes** | Every `BREAKING CHANGE:` trailer, verbatim |
| **Action required** | Either "None; `docker pull … && docker restart dkp`" or an explicit numbered list |
| **Verify this release** | The exact digest, the `cosign verify` command, the `gh attestation verify` command |

A table-rewriting migration with no duration estimate is the single most common cause of "the upgrade
hung" support threads. Do not ship one unlabelled.

### 5. Verify the API delta by hand

```bash
oasdiff changelog <(git show "v${PREV}:openapi/openapi.json") openapi/openapi.json
```

Read it as a bot author would. `oasdiff` calls a rename-by-addition "additive"; a human does not.
Anything that semantically renames a concept belongs in **Breaking changes**, whatever the tool says.

### 6. Hand off the merge

Say explicitly that a human merges the Release PR. Direct pushes to `main` are forbidden for
everyone, tags are created by the release App (a tag made with the default `GITHUB_TOKEN` does **not**
trigger workflows, which is why the App exists), and `git push`/`git tag` are outside your remit.

### 7. For an RC, additionally

| Requirement | Value |
|---|---|
| Branch | `release-1.5`, with `"prerelease": true, "prerelease-type": "rc"` |
| Freeze | Feature-frozen. **Migrations are final** — an RC that changes migration shape strands every tester. |
| Upgrade | N-1 path tested before announcement |
| Release | GitHub Release marked pre-release |
| Announcement | Discord, with an explicit call for testers |

`edge` has **no** guarantees at all: an edge build may apply a migration a later edge build changes
shape on, and the binary refuses to downgrade, so an edge tracker can be stranded. Say that in three
places, every time.

### 8. Verify the published release

After `release.yml` runs, check all of these landed:

- [ ] Multi-arch image: `linux/amd64`, `linux/arm64`, `linux/arm/v7`.
- [ ] Image ≤ 30 MB compressed and ≤ +5% versus the last release (`advisory / image-size`).
- [ ] goreleaser binaries for `{linux,darwin,windows} × {amd64,arm64}`, plus `.deb`/`.rpm` and the
      Homebrew tap.
- [ ] cosign signature, SBOM, and provenance attestation — and the README verification snippet's
      certificate identity still matches the workflow file **at the tag ref**.
- [ ] SDKs published in lockstep: `@dragonkillparty/sdk@<version>` and `dkp-client==<version>` are by
      construction generated from this server's spec.
- [ ] **`ghcr.io/prokopto-dev/dkp-refdb:<version>` exists** — and, **on the release that created the
      package**, that it is public. This is the one visibility check that cannot happen before the
      tag, because `refdb / publish` is what creates the package:

      ```bash
      gh api /users/prokopto-dev/packages/container/dkp-refdb --jq .visibility   # want: public
      ```

      `private` here does not fail the release, and will not fail the next one either — every
      `release.yml` job that touches the registry logs in, `smoke` included. It fails
      `nightly-verify.yml`'s `upgrade-ladder`, which does not, and it fails anyone reproducing an
      upgrade from a published refdb by hand. Fix it the same way: Packages → `dkp-refdb` → Package
      settings → Change visibility → Public.

### 9. Confirm the smoke gate held

Moving tags advance **only after** smoke passes. `release.yml` publishes the immutable `:1.5.0`, then
smoke pulls it on amd64 **and** arm64 runners and runs first boot + `dkp doctor` + `/readyz` + the
SPA the image serves + an upgrade from the previous release's refdb. Only then do `:1.5`, `:1` and
`:latest` move.

The SPA line is there because "it boots" was once the whole gate and an image that never ran the Vite
build boots perfectly — it just serves "web UI not yet built into this binary" (issue #55). The check
reads the bytes, because `internal/ui` answers 200 for every path either way.

If smoke failed: the immutable tag stays for forensics, the moving tags must **not** have moved, the
GitHub Release stays a draft, and an issue is filed. Verify that is what happened rather than
assuming.

### 10. Check the refdb ladder has no hole

A release that fails after the image step but before the `refdb` job leaves a gap nobody notices
until an upgrade breaks months later.

`nightly-verify.yml`'s `upgrade-ladder` enumerates GitHub Releases and **fails if any released minor
lacks a refdb artifact** — confirm it is green, do not just confirm the artifact you expected exists.

### 11. Post-release

- [ ] `demo.dragonkillparty.org` picks up the new build on its nightly reset.
- [ ] The in-app update check serves the new version on the `stable` channel (and only there).
- [ ] `docs/api-changelog.md` and `CHANGELOG.md` agree with the Release body.

---

## Stop and ask if

- **Anything in the release would require a manual operator step.** That is a MAJOR bump and a
  product decision, not a release-mechanics decision.
- **A migration in this release rewrites a table** and you cannot state its duration on the largest
  refdb.
- **The `upgrade-ladder` is red for any published minor.** The upgrade promise is "any 1.x upgrades
  directly to any later 1.y". Do not ship on top of a broken ladder.
- **Smoke failed but the moving tags moved anyway.** That is an incident: nobody running `:1` should
  ever see a build that failed to boot.
- **You are being asked to tag, push, publish or deploy.** You do not do those. Say so and hand off.
