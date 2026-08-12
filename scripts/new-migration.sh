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

# The real checkout, captured BEFORE the cd below and never overridden. It is where the Go module
# lives, and under test that is not the tree this script operates on.
module_root=$(cd "$(dirname "$0")/.." && pwd)

# DKP_REPO_ROOT lets a test drive this against a fabricated tree in t.TempDir() — the same
# mechanism scripts/repo-gates.sh uses and for the same reason. The negative cases here (a name
# that is not snake_case, a name that already exists) must be proven to fail, and a fixture that
# proves it cannot live in this checkout. `make migration` strips it with `env -u`.
cd "${DKP_REPO_ROOT:-$module_root}"

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

# Both tools are checked up front, before anything is generated. Discovering the second one missing
# after `atlas migrate diff` has already written a file leaves a half-authored, un-rewritten,
# un-hashed migration in the tree for the next person to interpret.
go_bin=$(command -v go 2>/dev/null || true)
[ -n "$go_bin" ] || die "go is not installed — see make setup"

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
# `tr -d` for the reason in gen-db.sh: BSD `wc` pads the count, and `-eq` tolerating the padding is
# an implementation detail rather than a promise (issue #111).
[ "$(printf '%s\n' "$created" | wc -l | tr -d '[:space:]')" -eq 1 ] \
    || die "atlas wrote more than one file; renumber them by hand and re-run 'atlas migrate hash --env sqlite':
$(printf '%s\n' "$created" | sed 's/^/    /')"

final="$dir/${next}_${name}.sql"
mv "$created" "$final"

# Two rewrites in one pass, and they live in Go: internal/migrate/migrationfmt.
#
# FIRST, backtick-quoted identifiers become double-quoted ones. Atlas emits `dkp_meta` for SQLite,
# which SQLite accepts as a MySQL compatibility extension — but sqlc's SQLite parser does not, and
# its failure mode is the worst kind: it does not reject the schema file, it silently parses no
# table out of it and then reports `relation "dkp_meta" does not exist` against the QUERY, pointing
# at the one file that is correct. Gate MIG002 in scripts/repo-gates.sh is the backstop: it fails
# any migration that still contains a backtick.
#
# SECOND, everything from `-- +goose Down` onwards is replaced. Recovery in this project is the
# snapshot taken immediately before the migration ran, not a reverse migration.
#
# AND IT REFUSES, rather than rewrite, a backtick INSIDE a single-quoted string literal wrapping
# something identifier-shaped: `CHECK (a <> '`abc`')` would become `CHECK (a <> '"abc"')` — still
# valid SQL, still applies cleanly, and now meaning something different, with nothing in the diff to
# suggest the generator did it. On that path it writes nothing at all and leaves the file exactly as
# Atlas produced it.
#
# THE REWRITE MOVED OUT OF THIS SCRIPT IN ISSUE #128, and the reason is the refusal above. It is
# string surgery on a file that is append-only and permanent from the moment it is committed — the
# one artefact here where a wrong rewrite is unrecoverable for a user — and as a sed/awk pipeline the
# only way to test it was to run this whole script.
#
# The Go version is a SCANNER rather than a pattern, and that is not tidiness: sed and grep see one
# physical line, and a SQLite string literal may span as many as it likes, so a backtick on the
# second line of a multiline DEFAULT read here as an identifier quote and was rewritten — changing
# the stored value AND removing the backtick that gate MIG002 would have caught it by. That input is
# refused now. The scanner also knows that `''` is an escaped quote rather than two literals, and
# that a comment is not SQL, which is what stops the apostrophe in the Down block's own
# "RAISE()'s message" from opening a literal that swallows the rest of the file.
#
# Every input that decides whether the refusal is right is a unit test in
# internal/migrate/migrationfmt/main_test.go — the literal that must be refused, the multiline
# literal, and the adjacent-literal shape (`DEFAULT ''`, a backticked column, `DEFAULT 'UTC'` — any
# table with two TEXT defaults) that a naive regex misreports as the same thing because its runs hop
# the boundary BETWEEN two literals. So is a test asserting every migration already committed is a
# fixed point of the Go rewrite, which is what makes this a move rather than a second implementation
# of the same intent.
#
# No "GENERATED — do not edit" banner is prepended, deliberately. Three mechanisms already carry
# that status and all three are enforced rather than advisory: .claude/hooks/guard-protected-paths.sh
# hard-denies any edit under db/migrations-*/ and names db/schema.hcl as the source, AGENTS.md
# lists the directory as generated, and CI's codegen-drift job fails on any diff. A prose banner on
# top of that would only lengthen the one artefact whose review value is that the SQL is short
# enough to read in full.
#
# `-C "$module_root"` and an absolute file argument, never a bare `go run`: this script has already
# cd'd to DKP_REPO_ROOT, which under test is a fabricated tree in t.TempDir() with no Go module in
# it — and with the module root as the working directory, a relative migration path would resolve
# against the real checkout instead of the fixture.
if ! "$go_bin" run -C "$module_root" ./internal/migrate/migrationfmt "$PWD/$final"; then
    exit 1
fi

# The rename and the rewrite both invalidate atlas.sum. Plain `hash`, never `hash --force`: the
# forced variant re-hashes a directory whose contents changed under it, which is the mechanism that
# would let an edit to an already-applied migration pass unnoticed.
"$atlas" migrate hash --env sqlite

printf '\033[32m  wrote %s\033[0m\n' "$final"
printf '  Read it before committing. On SQLite a column change becomes a 12-step table rebuild,\n'
printf '  and a rebuild drops every trigger attached to the old table without saying so.\n'
