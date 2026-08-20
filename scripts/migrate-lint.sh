#!/usr/bin/env bash
# migrate-lint — run Atlas's own migration analyzers over db/migrations-sqlite/, and fail on one.
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
# A GATE SINCE #136, having landed advisory-first under #131. Advisory-first was the way in: SQLite's
# 12-step table rebuild is where Atlas's analyzers are least predictable, and a new linter that blocks
# merges from day one gets disabled rather than tuned. The three things #136 asked for before the flip
# are now answered, each by a test in test/repo/migrate_lint_test.go rather than by this paragraph:
#
#   1. A 12-STEP REBUILD HAS BEEN OBSERVED UNDER THE LINTER. Atlas reports NO diagnostic on the
#      rebuild `make migration` emits for a CHECK change — the shape every catalogue edit produces.
#      A rebuild that genuinely drops a column still reports DS103, which is the two-release
#      destructive rule and correct. TestMigrateLint_TwelveStepRebuild_IsNotADiagnostic pins the
#      first half, so a future Atlas that started flagging the pattern says so here instead of going
#      red on somebody's unrelated schema change.
#   2. THE HAND-APPEND ALLOWLIST IS UNAFFECTED. Atlas community cannot see triggers at all, so it
#      neither asks for the re-created append-only triggers .claude/rules/migrations.md case 1
#      requires nor contradicts them. It has no opinion about a statement it cannot parse into a
#      schema change.
#   3. THE WAIVER IS ATLAS'S OWN `-- atlas:nolint <analyzer>` DIRECTIVE, in the migration, on the
#      line above the statement the diagnostic fires on — the same in-tree, in-the-diff idiom as
#      `// dkp:enum-literal`. It is paired with the `-- dkp:destructive-approved: #<issue>` line the
#      two-release destructive rule already requires (docs/design/06-cicd-and-release.md §8), which
#      is what makes a silenced analyzer reviewable rather than merely silent.
#      TestMigrateLint_NolintDirective_WaivesTheDiagnostic is its fixture.
#
# ENFORCING BY CONSTRUCTION, not via a required-check list — and the mode it is NOT is
# `continue-on-error`, which .github/workflows/ci.yml bans and TestCIWorkflow_NoContinueOnError
# asserts the absence of, because it is the quiet way to keep a gate in the checks list while
# removing its teeth. MODE=enforce is the default here and is what `make lint-migrations` passes, so
# `make check` on a laptop and `test / migrations` in CI reach the same verdict about the same
# branch. MODE=advise still prints every diagnostic, emits a `::warning::` and exits 0 — it is how to
# look at a migration set without being blocked by it, not how CI runs.
#
# A gate that exits 0 because it never ran is worse than no gate — the whole reason
# `make govulncheck` hard-fails when the binary is missing. So the two outcomes are kept apart in
# BOTH modes: a DIAGNOSTIC is a verdict, and a BROKEN INVOCATION (atlas absent, config unreadable,
# analysis errored) is a hard failure whatever MODE says.
#
#   MODE=enforce|advise         enforce (default) fails on a diagnostic; advise warns and exits 0
#   DKP_MIGRATE_LINT_BASE       git ref to analyse against (default origin/main)
#   DKP_REPO_ROOT               repository root, the same contract the other gate scripts honour
#   DKP_ATLAS                   path to the PINNED atlas; the Makefile passes it, PATH is the fallback
#
# There is no test-only environment variable and no branch below that exists for the tests, which is
# the rule every gate here is held to. test/repo drives this script the way CI does: a real tree in
# t.TempDir() carrying the repository's own atlas.hcl, reached through DKP_REPO_ROOT.
#
# `atlas migrate lint` is ~30 ms over this repository's migration set, needs no network, and reads
# no live database — it replays the directory into the per-invocation in-memory dev database that
# atlas.hcl declares. That is what makes it cheap enough to sit inside `make lint`.

set -euo pipefail

cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

# ENFORCE IS THE DEFAULT, so an invocation that forgets to say fails closed. The Makefile passes it
# explicitly as well, which is belt and braces on purpose: a reader of `make lint-migrations` can see
# which mode CI runs without opening this file, and neither copy alone can quietly downgrade the gate.
MODE="${MODE:-enforce}"
base="${DKP_MIGRATE_LINT_BASE:-origin/main}"

die() {
    printf '\033[31m  %s\033[0m\n' "$*" >&2
    exit 1
}

# Hard requirement, never soft-skipped — the gen-db.sh rule. A gate that exits 0 because the
# analyser was absent reports "no diagnostics" about migrations it never read, which is a strictly
# worse signal than an absent job.
#
# THE PINNED BINARY WINS OVER PATH, and for this one tool that is not a preference (issue #254).
# `make setup` installs the checksum-verified community build into GOTOOLS_BIN, and the Makefile
# APPENDS that directory to PATH so "a deliberately chosen system tool still wins" — the right rule
# for every other tool here and the wrong one for this one, because the atlas it lets win may be the
# build that cannot run this gate at all. DKP_ATLAS carries the pinned path from the Makefile, which
# is the single place GOTOOLS_BIN is derived; PATH stays the fallback for a direct invocation.
if [ -n "${DKP_ATLAS:-}" ] && [ -x "${DKP_ATLAS}" ]; then
    atlas="$DKP_ATLAS"
else
    atlas=$(command -v atlas 2>/dev/null || true)
fi
[ -n "$atlas" ] || die "atlas is not installed — run make setup"

# THE EDITION IS PART OF THE PIN. This check is the whole of issue #254.
#
# `atlas migrate lint` is Atlas Pro-only from v0.38 in the OFFICIAL build: it aborts with "available
# only to Atlas Pro users", asks for `atlas login`, and analyses nothing. The COMMUNITY build — the
# artefact scripts/install-atlas.sh pins and verifies against scripts/atlas.sha256 — carries the
# same analyzers with no account, no login and no network, which Atlas's own abort message concedes:
# "the command and existing code remain open source and available in the Community Edition".
#
# So the two builds differ by one word of `atlas version` and by whether this gate can run, and a
# contributor gets the wrong one by following Atlas's OWN advice: the community build prints
# "try installing the official version as a troubleshooting step: curl -sSf https://atlasgo.sh | sh"
# after ANY error, including a migrate-lint failure. That is how the pin gets replaced by the one
# binary that cannot check a migration.
#
# Checking up front rather than reading the abort afterwards is what makes the message useful: an
# unlicensed atlas otherwise lands in the guess-list below — an unreadable atlas.hcl, a dev database
# that would not open — and cost the reporter of #254 exactly that wrong diagnosis.
atlas_version=$("$atlas" version 2>/dev/null | head -n 1 || true)

case "$atlas_version" in
    *community*) ;;
    *)
        die "atlas is not the community build this repository pins (issue #254).
  $atlas
  reports: ${atlas_version:-(no version output)}
  \`atlas migrate lint\` is Pro-only from v0.38 in the OFFICIAL build: it aborts asking you to run
  \`atlas login\` and analyses nothing, so this gate cannot run — not a diagnostic, no verdict at
  all. The community build runs the same analyzers offline, which is why scripts/install-atlas.sh
  pins atlas-community-<os>-<arch>-<version> and verifies it against scripts/atlas.sha256.
  Run \`make install-atlas\` to put the pinned build back, and do NOT follow Atlas's own error
  banner suggesting \`curl -sSf https://atlasgo.sh | sh\` — that installs the official build and is
  how this gate gets broken."
        ;;
esac

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
#
# NAME WHAT THE REF POINTS AT, not just what it is called. `origin/main` on a checkout that has not
# fetched this week selects a different set of migrations than the same ref on a fresh one, so the
# same command reports a different scope on two machines and neither one looks wrong: the reporter
# of #254 spent a diagnosis on a stale base that made an already-shipped migration look new. The
# short sha and its commit date are the two facts that make two runs comparable, and they cost one
# `git log`. Fetching here instead would be worse — a gate inside `make check` that reaches the
# network fails on a train, and moving the base under the contributor is not this script's call.
selection=()
selection_desc=""
base_desc=""

if git rev-parse --verify --quiet "$base" >/dev/null 2>&1; then
    selection=(--git-base "$base")
    base_desc="$base ($(git rev-parse --short "$base"), committed $(git log -1 --format=%cs "$base"))"
    selection_desc="every migration added versus $base_desc"
else
    printf '  \033[33mmigrate-lint: %s is not available in this checkout\033[0m\n' "$base"
    printf '  Falling back to the latest migration only. A shallow clone or a detached checkout is\n'
    printf '  the usual cause; CI checks out with fetch-depth: 0 and does not take this path.\n'
    selection=(--latest 1)
    base_desc="$base (not available in this checkout)"
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

if ! printf '%s' "$report" | grep -qE '^\['; then
    cat "$stderr_file" >&2
    printf '%s\n' "$report" >&2
    die "atlas migrate lint could not complete (exit $json_status).
  This is NOT a migration diagnostic — the analysis itself failed, so nothing was checked.
  Common causes: an unreadable atlas.hcl, an env other than 'sqlite', or a dev database Atlas
  could not open. A licence gate is NOT among them: the edition check above already established
  that this atlas is the community build, whose migrate lint needs no account (issue #254).
  If Atlas printed a banner above suggesting \`curl -sSf https://atlasgo.sh | sh\`, IGNORE IT. The
  community build prints that after any error, and the official build it installs is the one whose
  \`migrate lint\` is Pro-only — following it replaces a gate that failed with a gate that cannot
  run."
fi

# No diagnostics: every analysed file came back without a Reports array. An empty `[]` is the
# ordinary "this branch adds no migrations" case and is reported as such rather than as a pass,
# because a gate that prints the same thing whether it examined four files or zero is a gate nobody
# can tell has gone blind.
if [ "$report" = "[]" ]; then
    printf '  \033[32mmigrate-lint: no migrations to analyse\033[0m — nothing added versus %s\n' "$base_desc"
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
  a hard failure in BOTH modes: MODE=advise downgrades a verdict about a migration, never a
  migration that was never analysed.
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
    die "atlas migrate lint reported a diagnostic (MODE=enforce)
  A destructive or data-dependent change that is fine on a fresh install is the bug class that
  costs a guild ten years of DKP, so this blocks the merge (issue #136).
  Fix the migration by changing db/schema.hcl and regenerating it — never by hand-editing the SQL.
  If the change IS intended and reviewed, waive the analyzer where the reviewer will see it: put
    -- dkp:destructive-approved: #<issue>
    -- atlas:nolint <analyzer>
  on the lines above the statement the diagnostic names, then re-run \`make gen\` so atlas.sum
  matches. The issue must confirm the previous minor already stopped writing to that object —
  release N deprecates, release N+1 drops (docs/design/06-cicd-and-release.md §8)."
fi

# `::warning::` is a GitHub Actions annotation: it surfaces on the PR's Files-changed tab rather
# than only in a log nobody opens. Outside Actions it is a harmless line of text. This path is not
# how CI runs the gate — MODE=advise is for looking at a migration set without being blocked by it.
echo "::warning title=atlas migrate lint::Migration diagnostics found. MODE=advise, so this run did not block. CI and \`make check\` run MODE=enforce (issue #136) and will fail on these: a destructive or data-dependent change that is fine on a fresh install is the bug class that costs a guild ten years of DKP."
printf '  \033[33mmigrate-lint: advisory\033[0m — diagnostics above did NOT fail this run (MODE=%s), but MODE=enforce is what CI runs\n' "$MODE"
exit 0
