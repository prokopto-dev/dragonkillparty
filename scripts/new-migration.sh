#!/usr/bin/env bash
# Create a migration: `make migration NAME=add_bid_hold`.
#
# Atlas authors the SQL from db/schema.hcl. This script exists because three things Atlas does are
# wrong for this project, and each of them is wrong in a way that is invisible until it costs a
# guild their data:
#
#   1. Atlas names files by timestamp. Canonical conventions §16 fixes the name as
#      `NNNNNN_snake_case.sql`, zero-padded and sequential, and migrations are APPEND-ONLY — a
#      renumber makes an applied version disagree with the file that produced it, and the next
#      upgrade is undefined.
#
#   2. Atlas writes a real `-- +goose Down` block containing DDL. This project is forward-only
#      (docs/design/06-cicd-and-release.md §8) and recovery is the pre-migration snapshot, never a
#      down migration. Worse, the generated Down is not even self-consistent: a 12-step rebuild
#      emits `DROP TABLE new_<table>` for a table the Up block already renamed away. Gate MIG001 in
#      scripts/repo-gates.sh fails DDL in a Down block, and it is right to.
#
#   3. Atlas cannot express triggers at all in the community edition, and a 12-step table rebuild
#      DROPs the old table without recreating any trigger attached to it — silently, with no
#      warning. That is recorded as item V6 in docs/development/verify-before-phase-0.md. This
#      script cannot fix it; the fresh-install fingerprint test is what catches it, because the
#      fingerprint covers `sqlite_schema` rows of every type, triggers included.
#
# Read the generated SQL before committing it. `.claude/skills/add-migration/SKILL.md` step 4 has
# the four questions to ask of it.

set -euo pipefail

# DKP_REPO_ROOT lets a test drive this against a fabricated tree in t.TempDir() — the same
# mechanism scripts/repo-gates.sh uses and for the same reason. The negative cases here (a name
# that is not snake_case, a name that already exists) must be proven to fail, and a fixture that
# proves it cannot live in this checkout. `make migration` strips it with `env -u`.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() { printf '\033[31m  %s\033[0m\n' "$*" >&2; exit 1; }

name=${NAME:-}
[ -n "$name" ] || die "NAME is required: make migration NAME=add_bid_hold"

# snake_case, lowercase, starting with a letter. Rejects CamelCase, kebab-case, spaces, leading
# digits, trailing or doubled underscores. The name becomes a filename that is append-only and
# therefore permanent, so it is worth being strict about once.
printf '%s' "$name" | grep -qE '^[a-z][a-z0-9]*(_[a-z0-9]+)*$' \
    || die "NAME must be snake_case (^[a-z][a-z0-9]*(_[a-z0-9]+)*\$), got: $name"

dir=db/migrations-sqlite
mkdir -p "$dir"

# Refuse a name already used. Two migrations sharing a name make `git log` on a filename
# ambiguous and make an operator reading a failure message unable to tell which one failed.
existing_same_name=$(find "$dir" -maxdepth 1 -name "*_$name.sql" -print 2>/dev/null)
[ -z "$existing_same_name" ] || die "a migration named '$name' already exists:
$(printf '%s\n' "$existing_same_name" | sed 's/^/    /')
  Migrations are append-only. Pick a new name; never rename or renumber a migration."

# The next sequence number: one past the highest that exists. Not a count of files — a deleted or
# never-committed file must not cause a number to be reused.
highest=$(find "$dir" -maxdepth 1 -name '[0-9]*_*.sql' -exec basename {} \; 2>/dev/null \
    | sed -E 's/^0*([0-9]+)_.*/\1/' | sort -n | tail -1)
next=$(printf '%06d' $(( ${highest:-0} + 1 )))

# Belt and braces. If this fires, the sequence above is wrong and generating anyway would
# overwrite a migration that has possibly already run on somebody's database.
clash=$(find "$dir" -maxdepth 1 -name "${next}_*.sql" -print 2>/dev/null)
[ -z "$clash" ] || die "refusing to renumber: ${next}_ is already taken by
$(printf '%s\n' "$clash" | sed 's/^/    /')"

atlas=$(command -v atlas 2>/dev/null || true)
[ -n "$atlas" ] || die "atlas is not installed — run make setup"

before=$(find "$dir" -maxdepth 1 -name '*.sql' | sort)

"$atlas" migrate diff --env sqlite "$name" >/dev/null

after=$(find "$dir" -maxdepth 1 -name '*.sql' | sort)
created=$(comm -13 <(printf '%s\n' "$before") <(printf '%s\n' "$after"))

if [ -z "$created" ]; then
    printf '  db/schema.hcl and %s already agree — no migration to write.\n' "$dir"
    exit 0
fi

# More than one file means Atlas split the change, and the renumbering below would need to order
# them. It has never happened for a SQLite diff; if it does, a human should look rather than have
# this script guess at the order.
[ "$(printf '%s\n' "$created" | wc -l)" -eq 1 ] \
    || die "atlas wrote more than one file; renumber them by hand and re-run 'atlas migrate hash --env sqlite':
$(printf '%s\n' "$created" | sed 's/^/    /')"

final="$dir/${next}_${name}.sql"
mv "$created" "$final"

# Two rewrites in one pass.
#
# FIRST, backtick-quoted identifiers become double-quoted ones. Atlas emits `dkp_meta` for SQLite,
# which SQLite accepts as a MySQL compatibility extension — but sqlc's SQLite parser does not, and
# its failure mode is the worst kind: it does not reject the schema file, it silently parses no
# table out of it and then reports `relation "dkp_meta" does not exist` against the QUERY, pointing
# at the one file that is correct. Double quotes are the SQL standard identifier quote and are what
# a SQLite file should have contained in the first place.
#
# The pattern requires the quoted text to be a bare identifier, so a backtick inside a longer
# string literal — a CHECK expression, a DEFAULT — is left alone. Gate MIG002 in
# scripts/repo-gates.sh is the backstop: it fails any migration that still contains one.
#
# SECOND, everything from `-- +goose Down` onwards is replaced. Recovery in this project is the
# snapshot taken immediately before the migration ran, not a reverse migration.
#
# The rewrite is deterministic, which is what keeps `make gen` honest: regenerating this file from
# the same db/schema.hcl produces these bytes again, so `verify-generated`'s
# `git diff --exit-code` stays a real assertion rather than permanent noise.
#
# No "GENERATED — do not edit" banner is prepended, deliberately. Three mechanisms already carry
# that status and all three are enforced rather than advisory: .claude/hooks/guard-protected-paths.sh
# hard-denies any edit under db/migrations-*/ and names db/schema.hcl as the source, AGENTS.md
# lists the directory as generated, and CI's codegen-drift job fails on any diff. A prose banner on
# top of that would only lengthen the one artefact whose review value is that the SQL is short
# enough to read in full.
# The one input the rewrite below would silently corrupt: a backtick INSIDE a single-quoted string
# literal, wrapping something identifier-shaped. `CHECK (a <> '`abc`')` would become
# `CHECK (a <> '"abc"')` — still valid SQL, still applies cleanly, and now means something different,
# with nothing in the diff to suggest the generator did it. Refuse instead. This has never fired; it
# costs four lines and the alternative is a class of bug that only shows up as wrong data.
if grep -qE "'[^']*\`[^']*'" "$final"; then
    printf '\033[31m  %s contains a backtick inside a string literal.\033[0m\n' "$final" >&2
    printf '  The backtick-to-double-quote rewrite would change what that literal MEANS, so this\n' >&2
    printf '  script is refusing rather than guessing. Fix the expression in db/schema.hcl to avoid\n' >&2
    printf '  the backtick, or rewrite the identifiers in this file by hand and re-run\n' >&2
    printf '  `atlas migrate hash --env sqlite`.\n' >&2
    exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
awk '/^-- \+goose Down$/ { exit } { print }' "$final" \
    | sed -E 's/`([A-Za-z_][A-Za-z0-9_]*)`/"\1"/g' > "$tmp"
cat >> "$tmp" <<'DOWN'
-- +goose Down
-- Forward-only. This project ships no down migrations, ever: a down migration is code that runs
-- exactly once, in an emergency, on data that cannot be reproduced, written months earlier by
-- someone who never tested it against your database. Recovery is restoring the snapshot taken
-- immediately before this migration ran:
--
--     /data/backups/pre-<version>-<timestamp>.db.zst
--
-- The statement below aborts if goose is ever asked to run this block. Note that SQLite discards
-- RAISE()'s message outside a trigger body and reports "RAISE() may only be used within a
-- trigger-program" instead, so the path above — not the string below — is what an operator can
-- actually read.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
DOWN
mv "$tmp" "$final"
chmod 0644 "$final"

# The rename and the rewrite both invalidate atlas.sum. Plain `hash`, never `hash --force`: the
# forced variant re-hashes a directory whose contents changed under it, which is the mechanism that
# would let an edit to an already-applied migration pass unnoticed.
"$atlas" migrate hash --env sqlite

printf '\033[32m  wrote %s\033[0m\n' "$final"
printf '  Read it before committing. On SQLite a column change becomes a 12-step table rebuild,\n'
printf '  and a rebuild drops every trigger attached to the old table without saying so.\n'
