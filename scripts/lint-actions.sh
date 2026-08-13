#!/usr/bin/env bash
# The GitHub Actions workflow gate (issue #121) — `make lint-actions`.
#
# WHY A WORKFLOW LINTER EARNS ITS PLACE HERE. Most of the phase-0 CI tail this repository spent
# weeks on was STATIC workflow error, not logic: #82 (a PR leaving draft never re-ran the required
# jobs, because `types:` replaces the default event list rather than extending it), #94 (web and
# design gates conditioned on a filter their inputs never select), #101 (`merge_group` jobs on a
# repository with no merge queue, permanently skipped and counted as success) and #113 (a release
# smoke pulling GHCR with no `docker login`). actionlint reads the expression grammar, the
# `needs:` graph, the event/`types:` catalogue and the action inputs, so that class fails in 200 ms
# instead of on somebody's merge.
#
# BOTH TOOLS ARE REQUIRED, AND THAT IS THE LOAD-BEARING PART. actionlint pipes every `run:` block
# through shellcheck when it can find one, and SILENTLY DISABLES that rule when it cannot — so on a
# machine without shellcheck this gate still exits 0 while checking strictly less than it says it
# does. That is the self-concealing omission .github/workflows/ci.yml's header warns about, in
# linter form, so a missing shellcheck is a hard failure here rather than a quiet downgrade. Both
# integrations are passed EXPLICITLY for the same reason: `-pyflakes=` disables the python
# integration deterministically instead of leaving the gate's coverage to whether a runner image
# happens to ship pyflakes.
#
# Thin glue around a real CLI, deliberately kept as bash (issue #125's first bucket): everything
# below is tool discovery, file enumeration and one exec.
set -euo pipefail

# DKP_REPO_ROOT lets test/repo point this script at a fabricated tree in t.TempDir(), which is the
# only way a negative fixture can hold a deliberately broken workflow: one committed under
# .github/workflows/ would fail this project's own CI. `make lint-actions` strips the variable with
# `env -u` for the reason every other gate target does.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() {
    printf '\033[31m  %s\033[0m\n' "$*" >&2
    exit 1
}

command -v actionlint >/dev/null 2>&1 \
    || die "actionlint is not installed — run make setup
  It is pinned as ACTIONLINT_VERSION in the Makefile and installed by .github/actions/setup-toolchain."

command -v shellcheck >/dev/null 2>&1 \
    || die "shellcheck is not installed — run make setup
  actionlint checks every workflow 'run:' block with shellcheck and silently SKIPS that rule when
  the binary is absent, so a gate without it reports green over less than it claims to cover."

# Enumerate rather than letting actionlint discover the directory itself. A gate that finds its own
# inputs cannot tell "no workflows changed" from "no workflows found", and this project has already
# paid for one scanner that read a moved lockfile as a clean result (see ci.yml's `security / osv`).
workflows=()
for f in .github/workflows/*.yml .github/workflows/*.yaml; do
    [ -f "$f" ] && workflows+=("$f")
done

# NO VACUOUS PASS. An empty list is a broken invocation — a wrong DKP_REPO_ROOT, a renamed
# directory — and must never read as "every workflow is fine".
[ ${#workflows[@]} -gt 0 ] \
    || die "no workflow files under .github/workflows/ — nothing was linted.
  A gate that checked nothing must not report success."

actionlint -no-color -oneline -shellcheck=shellcheck -pyflakes= "${workflows[@]}" \
    || die "actionlint failed — see the diagnostics above.
  Each line is <file>:<line>:<col>: <message> [<rule>]. Rules:
  https://github.com/rhysd/actionlint/blob/main/docs/checks.md"

printf '  \033[32mworkflow lint passed\033[0m — %d workflow(s), actionlint + shellcheck on every run: block\n' \
    "${#workflows[@]}"
