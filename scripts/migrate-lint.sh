#!/usr/bin/env bash
# advisory / migrate-lint — run Atlas's own migration analyzers over db/migrations-sqlite/.
#
# Issue #131. Migration safety was checked only by this repository's bespoke gates: MIG001 (DDL in a
# goose Down block), MIG002 (backtick identifiers), MIG003 (SHIPPED.lock frozen-migration check),
# TestMigrate_FreshInstall_MatchesFingerprint and the populated-upgrade suite. Those encode rules
# Atlas has never heard of — forward-only migrations, a shipped-migration history file, append-only
# triggers surviving a table rebuild. What they do NOT encode is the general destructive- and
# backward-incompatible-change analysis that Atlas, already the authoring tool here, ships with.
#
# This is ADDITIVE. Nothing above is replaced, weakened or made conditional on this script, and
# TestMigrationGates_AtlasLint_IsAdditive in test/repo asserts that in code rather than in a comment.
#
# ADVISORY BY CONSTRUCTION, not via `continue-on-error` — .github/workflows/ci.yml bans that string
# and TestCIWorkflow_NoContinueOnError asserts its absence, because it is the quiet way to keep a
# gate in the checks list while removing its teeth. In the default MODE=advise this script prints
# every diagnostic, emits a `::warning::` so it is visible in the PR's checks annotations, and exits
# 0. MODE=enforce exits non-zero on a diagnostic instead. Both paths exist today and both are tested;
# promoting CI from one to the other is a one-word change at the call site, tracked in #136.
#
# Advisory does NOT mean "cannot fail". A gate that exits 0 because it never ran is worse than no
# gate — the whole reason `make govulncheck` hard-fails when the binary is missing. So the two
# outcomes are kept apart: a DIAGNOSTIC is advisory, and a BROKEN INVOCATION (atlas absent, config
# unreadable, analysis errored) is a hard failure in both modes.
#
#   MODE=advise|enforce         advise (default) warns on a diagnostic; enforce fails on one
#   DKP_MIGRATE_LINT_BASE       git ref to analyse against (default origin/main)
#   DKP_REPO_ROOT               repository root, the same contract the other gate scripts honour
#
# There is no test-only environment variable and no branch below that exists for the tests, which is
# the rule scripts/licence-gate.sh is held to. test/repo drives this script the way CI does: a real
# tree in t.TempDir() carrying the repository's own atlas.hcl, reached through DKP_REPO_ROOT.
#
# `atlas migrate lint` is ~30 ms over this repository's migration set, needs no network, and reads
# no live database — it replays the directory into the per-invocation in-memory dev database that
# atlas.hcl declares. That is what makes it cheap enough to sit inside `make lint`.

set -euo pipefail

cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

MODE="${MODE:-advise}"
base="${DKP_MIGRATE_LINT_BASE:-origin/main}"

die() {
    printf '\033[31m  %s\033[0m\n' "$*" >&2
    exit 1
}

# Hard requirement, never soft-skipped — the gen-db.sh rule. An advisory gate that exits 0 because
# the analyser was absent reports "no diagnostics" about migrations it never read, which is a
# strictly worse signal than an absent job.
atlas=$(command -v atlas 2>/dev/null || true)
[ -n "$atlas" ] || die "atlas is not installed — run make setup"

# WHICH migrations to analyse.
#
# `--git-base` is the right selection and not merely the convenient one: a shipped migration is
# frozen (MIG003), so a diagnostic on one cannot be acted upon by whoever trips over it, and a gate
# that reports permanently unfixable findings is a gate people learn to skim past. Analysing exactly
# what the branch ADDS keeps every reported diagnostic actionable by its author.
#
# The fallback matters on a shallow clone and in a detached checkout, where the base ref is simply
# absent. It is PRINTED rather than silent, because "analysed a different set than you think" is the
# failure mode a quiet fallback creates. CI does not take this path: the job checks out with
# fetch-depth: 0 and TestCI_MigrationLintStep_FetchesFullHistory pins that.
selection=()
selection_desc=""

if git rev-parse --verify --quiet "$base" >/dev/null 2>&1; then
    selection=(--git-base "$base")
    selection_desc="every migration added versus $base"
else
    printf '  \033[33mmigrate-lint: %s is not available in this checkout\033[0m\n' "$base"
    printf '  Falling back to the latest migration only. A shallow clone or a detached checkout is\n'
    printf '  the usual cause; CI checks out with fetch-depth: 0 and does not take this path.\n'
    selection=(--latest 1)
    selection_desc="the latest migration only (fallback: $base is unavailable)"
fi

printf '  migrate-lint: analysing %s\n' "$selection_desc"

# Run twice, and the duplication is the point.
#
# The JSON pass is the DISCRIMINATOR. `atlas migrate lint` exits 1 when it finds a diagnostic AND
# when it could not run at all, and those two need opposite treatments here. Whether stdout is a
# JSON array is what separates "Atlas produced a report" from "Atlas never got that far"; the
# report's CONTENTS then separate a verdict from a migration it could not analyse, further down.
# Parsing rather than grepping Atlas's prose keeps the discriminator off its wording, which is
# exactly the trap scripts/gen-db.sh documents about `atlas schema diff`.
#
# stdout and stderr are captured SEPARATELY rather than folded together with 2>&1. Atlas writes
# advice to stderr on some paths — the community-build banner is the one seen here — and a banner
# landing in front of the JSON would make the `[]` comparison below miss, silently turning "no
# migrations to analyse" into a parse of something else. stderr is kept only to be reprinted when
# the invocation fails, which is the one time it is worth reading.
#
# The text pass is what a human reads: it names the analyzer, the position and the suggested fix.
stderr_file=$(mktemp)
# shellcheck disable=SC2064  # expand stderr_file now: the trap must survive the variable going away
trap "rm -f '$stderr_file'" EXIT

set +e
report=$("$atlas" migrate lint --env sqlite "${selection[@]}" --format '{{ json .Files }}' 2>"$stderr_file")
json_status=$?
set -e

if ! printf '%s' "$report" | grep -qE '^\[' ; then
    cat "$stderr_file" >&2
    printf '%s\n' "$report" >&2
    die "atlas migrate lint could not complete (exit $json_status).
  This is NOT a migration diagnostic — the analysis itself failed, so nothing was checked.
  Common causes: an unreadable atlas.hcl, an env other than 'sqlite', or a dev database Atlas
  could not open."
fi

# No diagnostics: every analysed file came back without a Reports array. An empty `[]` is the
# ordinary "this branch adds no migrations" case and is reported as such rather than as a pass,
# because a gate that prints the same thing whether it examined four files or zero is a gate nobody
# can tell has gone blind.
if [ "$report" = "[]" ]; then
    printf '  \033[32mmigrate-lint: no migrations to analyse\033[0m — nothing added versus %s\n' "$base"
    exit 0
fi

# ORDER MATTERS BELOW, and getting it backwards is a silent hole rather than a visible bug.
#
# Every analysed file carries an `Error` string when something went wrong with it, and a `Reports`
# array when an analyzer had something to say — but a DIAGNOSTIC sets BOTH (a destructive change
# comes back as `"Reports":[…],"Error":"destructive changes detected"`), while a migration Atlas
# could not even execute sets `Error` ALONE:
#
#   {"Name":"000005_x.sql","Error":"executing statement: …: no such column: \"label\""}
#
# So `Reports` is what separates them, and it has to be tested FIRST. Testing `Error` first would
# route every ordinary diagnostic down the hard-fail path; testing only `Reports` — which this
# script did until the negative fixture in test/repo caught it — reports a migration that does not
# apply AT ALL as "no diagnostics", which is the green-check-that-checked-nothing this whole file
# exists to avoid.
has_reports=false
has_error=false
printf '%s' "$report" | grep -q '"Reports"' && has_reports=true
printf '%s' "$report" | grep -q '"Error"' && has_error=true

if [ "$has_reports" = false ] && [ "$has_error" = true ]; then
    printf '%s\n' "$report" >&2
    die "atlas migrate lint could not analyse a migration.
  Atlas replays the directory into a scratch database to compute what each migration changes, and
  one of them did not execute — so it produced no verdict at all rather than a clean one. That is
  a hard failure in BOTH modes: advisory covers a verdict about a migration, never a migration
  that was never analysed.
  Fix the statement (make test-migrations' fresh-install case will be failing on it too), then
  re-run."
fi

if [ "$has_reports" = false ]; then
    printf '  \033[32mmigrate-lint: no diagnostics\033[0m — Atlas analysed %s\n' "$selection_desc"
    exit 0
fi

# Diagnostics found. Print the human-readable form; its own exit status is deliberately discarded
# because the JSON pass above has already established that the analysis ran, and this second
# invocation exists only for its output.
printf '\n'
"$atlas" migrate lint --env sqlite "${selection[@]}" || true
printf '\n'

if [ "$MODE" = "enforce" ]; then
    die "atlas migrate lint reported a diagnostic (MODE=enforce)"
fi

# `::warning::` is a GitHub Actions annotation: it surfaces on the PR's Files-changed tab rather
# than only in a log nobody opens. Outside Actions it is a harmless line of text.
echo "::warning title=atlas migrate lint::Migration diagnostics found. ADVISORY today (issue #131) — this does not block the merge. Read them: a destructive or data-dependent change that is fine on a fresh install is the bug class that costs a guild ten years of DKP. Promotion to a gate is tracked in #136."
printf '  \033[33mmigrate-lint: advisory\033[0m — diagnostics above did NOT fail the build (MODE=%s)\n' "$MODE"
exit 0
