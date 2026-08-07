#!/usr/bin/env bash
# The database half of `make gen`: the migration directory's checksum, the schema/migration sync
# assertion, and sqlc.
#
# What this does NOT do is write a migration. `make gen` must be safe to run at any time and must
# produce the same tree every time — a generator that silently invented a migration whenever
# db/schema.hcl had moved would create numbered, append-only, permanent files as a side effect of
# a command people run reflexively. Writing migrations is `make migration NAME=<snake_case>`, which
# is deliberate, named, and refuses to renumber.
#
# So an out-of-sync schema is reported here as an ERROR telling you which command to run.

set -euo pipefail

# Same DKP_REPO_ROOT contract as the gate scripts; `make gen` strips it with `env -u`.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() { printf '\033[31m  %s\033[0m\n' "$*" >&2; exit 1; }

# Both tools are hard requirements, never soft-skipped. A `make gen` that exits 0 because a
# generator was missing reports a clean tree it never inspected, and the next `verify-generated`
# in CI is the first thing to notice — by which point the stale artefact is already committed.
atlas=$(command -v atlas 2>/dev/null || true)
[ -n "$atlas" ] || die "atlas is not installed — run make setup"
sqlc=$(command -v sqlc 2>/dev/null || true)
[ -n "$sqlc" ] || die "sqlc is not installed — run make setup"

dir=db/migrations-sqlite

# 1. Re-hash the committed migration directory.
#
# This is what makes a hand-edited migration visible: atlas.sum is derived from the file contents,
# so editing a migration changes the sum, and `verify-generated`'s `git diff --exit-code` then
# fails on atlas.sum even though the edit itself was to a file nobody diffs closely.
if [ -n "$(find "$dir" -maxdepth 1 -name '*.sql' -print -quit 2>/dev/null)" ]; then
    "$atlas" migrate hash --env sqlite
fi

# 2. Assert db/schema.hcl and the migration directory describe the same schema.
#
# Done against a COPY of the directory, because the only Atlas command that computes this answer
# reliably is `migrate diff`, and `migrate diff` writes a file when it finds a difference. Against
# a copy in a temp directory, that written file is the signal and the real tree is untouched.
#
# `atlas schema diff` would avoid the copy, but it exits 0 whether or not it found changes, so the
# check would rest entirely on parsing its stdout — and a future release changing "Schemas are
# synced, no changes to be made." to any other wording would turn this gate green forever.
if [ -n "$(find "$dir" -maxdepth 1 -name '*.sql' -print -quit 2>/dev/null)" ]; then
    probe=$(mktemp -d)
    trap 'rm -rf "$probe"' EXIT
    cp "$dir"/. "$probe"/ -R 2>/dev/null || cp -R "$dir"/ "$probe"/

    before=$(find "$probe" -name '*.sql' | wc -l)
    "$atlas" migrate diff --env sqlite --dir "file://$probe" gen_sync_probe >/dev/null
    after=$(find "$probe" -name '*.sql' | wc -l)

    if [ "$before" -ne "$after" ]; then
        pending=$(find "$probe" -name '*gen_sync_probe.sql' -exec cat {} \;)
        die "db/schema.hcl has changes with no migration.

  Run:  make migration NAME=<snake_case>

  The migration Atlas would write:
$(printf '%s\n' "$pending" | sed 's/^/    /')"
    fi
fi

# 3. sqlc, over db/queries/*.sql.
"$sqlc" generate

printf '  \033[32mdatabase artefacts regenerated\033[0m — migrations, atlas.sum, sqlc\n'
