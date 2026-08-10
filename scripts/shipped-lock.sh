#!/usr/bin/env bash
# shipped-lock — the append-only manifest of migrations that have already shipped.
#
# db/migrations-sqlite/SHIPPED.lock holds one `filename sha256` row per migration that has appeared
# in a tagged release. A migration in that list has already run on somebody's database, so editing
# it makes an existing install and a fresh install end up with different schemas — and "works on a
# fresh install, breaks on upgrade" is the most damaging bug class for this audience: a volunteer
# officer with ten years of guild DKP and no backup discipline. See AGENTS.md and
# .claude/rules/migrations.md > "Shipped migrations are frozen".
#
# THREE CALLERS, THREE DIFFERENT QUESTIONS
# ----------------------------------------
#   verify              Every listed file still exists and still hashes to its recorded value.
#                       Runs on EVERY PR as MIG003 in scripts/repo-gates.sh. It deliberately does
#                       NOT require completeness: a migration added on a feature branch has not
#                       shipped yet and must not be listed. Requiring it here would make the gate
#                       fire on the one change it is supposed to permit.
#
#   verify --complete   The above, PLUS every migration in the tree is listed. That is only true at
#                       a tag, where by definition everything present ships — so this is the release
#                       path (`make release-shipped-lock`, release.yml's `prepare` job). It runs
#                       before any image, binary or moving tag exists, which is the last point at
#                       which a missing row is free to fix.
#
#   seal                Append a row for every migration not yet listed. Run when preparing a
#                       release (`make shipped-lock-seal`, the cut-release skill), so the manifest
#                       is sealed IN the Release PR and reviewed by the human who merges it. CI
#                       never pushes to main, so the release job verifies rather than writes.
#                       Existing rows are never rewritten and never removed — that is what
#                       "append-only" means here, and it is the entire point of the file.
#
#   init                Create the manifest with its header and no rows. Refuses if it already
#                       exists, so it can never destroy a row. This is how the file is bootstrapped;
#                       after that, `seal` is the only writer.
#
# WHY THIS IS NOT atlas.sum
# -------------------------
# atlas.sum protects the CURRENT migration set against tampering, and `make verify-generated` fails
# when it drifts. That is a real control and a different promise: atlas.sum tracks the tree as it
# is, so regenerating it after an edit makes the edit legitimate again. SHIPPED.lock records what a
# USER'S DATABASE has already executed, which nothing in this repository is allowed to change after
# the fact. An edit plus a re-hash walks past atlas.sum and is exactly what MIG003 stops.
#
# SCOPE IS db/migrations-sqlite
# ----------------------------
# db/migrations-postgres is generated and compiled in CI only — it is never applied to a user's
# database in 1.x — so freezing it would be a promise about nobody's data. Rows are basenames, which
# is the form .claude/hooks/guard-protected-paths.sh matches against for BOTH dialect directories.

set -euo pipefail
# DKP_REPO_ROOT lets a test point this script at a tree other than this checkout, exactly as
# repo-gates.sh and install-atlas.sh do, so the gate can be tested rather than trusted: a test
# writes a lock file and a deliberately altered migration into t.TempDir() and requires a non-zero
# exit. Such a fixture cannot live in the repo, because the real `make lint-repo` would find it.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

DIR="db/migrations-sqlite"
LOCK="$DIR/SHIPPED.lock"

# The header a freshly created manifest carries. It is prose for a human who opens the file, and it
# lives here rather than in the file so `init` and any future writer cannot disagree about it.
header() {
    cat <<'EOF'
# SHIPPED.lock — migrations that have shipped in a tagged release. APPEND-ONLY.
#
# One "<filename> <sha256>" row per migration, in the order they shipped. A row here means the file
# has already run on somebody's database: it is frozen, and correcting it means writing a NEW
# migration, never editing this one. Rows are appended by "make shipped-lock-seal" when a release
# is prepared, and are never rewritten or removed.
#
# Enforced by MIG003 in scripts/repo-gates.sh on every PR (every listed hash must still match) and
# by "make release-shipped-lock" at tag time (every migration present must also be listed).
# Blank lines and lines starting with # are ignored.
EOF
}

# sha256_of <file> — lowercase hex, portably. sha256sum on Ubuntu (CI), shasum on macOS (laptops);
# picking either one alone would make this work in exactly half the places it runs. Neither present
# is a HARD failure, never a skip: a hash gate that cannot hash must not report green.
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{ print $1 }'
    else
        printf 'shipped-lock: neither sha256sum nor shasum is available; cannot verify %s\n' "$1" >&2
        exit 1
    fi
}

fail=0
problem() {
    printf '  %s\n' "$1"
    fail=1
}

# read_lock — populate `listed` (space-padded basenames) and `n_listed`, and check every row.
#
# A malformed row is a FAILURE, not a skipped line. A truncated or half-written manifest that parsed
# to zero rows would otherwise report "0 shipped migrations unchanged" and pass, which is precisely
# the vacuous green this file exists to prevent.
listed=" "
n_listed=0
read_lock() {
    local line name hash extra file got lineno=0

    while IFS= read -r line || [ -n "$line" ]; do
        lineno=$((lineno + 1))
        line="${line%$'\r'}"

        # Blank and comment lines are ignored, as the header promises. Anything else is a row, and
        # rows start in column 1 — a leading space is a sign of a hand-edit, not of a format.
        case "$line" in
        '' | '#'*) continue ;;
        *[![:space:]]*) ;;
        *) continue ;; # whitespace only
        esac

        read -r name hash extra <<<"$line"

        if [ -z "$hash" ] || [ -n "$extra" ]; then
            problem "$LOCK:$lineno: a row is exactly '<filename> <sha256>': $line"
            continue
        fi

        case "$name" in
        *[!a-z0-9_.-]* | */* | '')
            problem "$LOCK:$lineno: the filename is a plain migration basename: $name"
            continue
            ;;
        *.sql) ;;
        *)
            problem "$LOCK:$lineno: the filename must be a .sql migration: $name"
            continue
            ;;
        esac

        case "$hash" in
        *[!0-9a-f]* | '')
            problem "$LOCK:$lineno: the hash must be 64 lowercase hex characters: $hash"
            continue
            ;;
        esac

        if [ "${#hash}" -ne 64 ]; then
            problem "$LOCK:$lineno: the hash must be 64 lowercase hex characters: $hash"
            continue
        fi

        case "$listed" in
        *" $name "*)
            problem "$LOCK:$lineno: $name is listed twice; the manifest is append-only, not editable"
            continue
            ;;
        esac

        listed="$listed$name "
        n_listed=$((n_listed + 1))

        file="$DIR/$name"

        if [ ! -f "$file" ]; then
            problem "$name — DELETED. It is listed in $LOCK, so it has already run on a user's database."
            continue
        fi

        got="$(sha256_of "$file")"

        if [ "$got" != "$hash" ]; then
            problem "$name — MODIFIED. expected $hash, found $got"
        fi
    done <"$LOCK"
}

# check_append_only — the manifest at the MERGE BASE must be an exact PREFIX of the manifest now.
#
# WHY THIS EXISTS, AND WHY read_lock IS NOT ENOUGH
# ------------------------------------------------
# read_lock only proves the manifest and the working tree AGREE. That catches editing a shipped
# migration, and nothing else — because the manifest is in the same commit as the migration. Change
# both together, and the tree is self-consistent again:
#
#     edit db/migrations-sqlite/000003_ledger.sql
#     replace its row in SHIPPED.lock with the new hash     -> read_lock passes, MIG003 green
#     delete its row from SHIPPED.lock entirely             -> the file is simply unlisted, green
#
# Both are the exact failure this file exists to prevent, and neither is caught by hashing alone.
# Nor is either caught elsewhere: the Claude hook is a local editor guard, not a CI control, and the
# Release PR must legitimately modify this file, so it cannot be CODEOWNERS-frozen outright.
# The only durable answer is to compare the file against its own history.
#
# WHY A PREFIX AND NOT A SET
# --------------------------
# A set comparison ("every old row is still present somewhere") permits reordering and re-heading,
# which is a rewrite with extra steps. The file is append-only in the strict sense: whatever the
# merge base said, byte for byte, must still be the beginning of what this change says. The only
# legal edit is more bytes at the end.
#
# WHY THE MERGE BASE AND NOT origin/main
# --------------------------------------
# A branch cut before a release legitimately lacks the rows that release appended. Comparing against
# the tip of main would fail it for being behind, which is not what this rule is about, and the fix
# people would reach for is disabling the check.
#
# WHEN THE BASE CANNOT BE READ
# ----------------------------
# It SKIPS, loudly, naming why. That is not a hole being waved through: this check runs in ci.yml's
# `lint / repo` job, which carries `fetch-depth: 0`, and TestCI_LintRepoJob_FetchesFullHistory fails
# if that is ever removed. Hard-failing instead would break every shallow-checkout job that runs
# `make lint-repo` through a test, and a gate that red-lights honest jobs gets deleted.
check_append_only() {
    local base_ref="${DKP_SHIPPED_LOCK_BASE_REF:-origin/main}" merge_base current base line

    skip_base() {
        printf '  append-only history NOT checked: %s\n' "$1"
    }

    if ! command -v git >/dev/null 2>&1; then
        skip_base "git is not on PATH"
        return 0
    fi

    if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
        skip_base "this is not a git work tree"
        return 0
    fi

    if ! git rev-parse --verify --quiet "$base_ref" >/dev/null 2>&1; then
        skip_base "$base_ref is not available (shallow clone? CI needs fetch-depth: 0; locally, git fetch origin main)"
        return 0
    fi

    if ! merge_base="$(git merge-base HEAD "$base_ref" 2>/dev/null)" || [ -z "$merge_base" ]; then
        skip_base "HEAD and $base_ref have no merge base"
        return 0
    fi

    # `<rev>:./<path>` resolves relative to the current directory, so this is correct even when
    # DKP_REPO_ROOT points somewhere other than the repository root.
    if ! git cat-file -e "$merge_base:./$LOCK" 2>/dev/null; then
        printf '  %s does not exist at the merge base — this is the change that introduces it\n' "$LOCK"
        return 0
    fi

    current="$(cat "$LOCK")"
    base="$(git show "$merge_base:./$LOCK")"

    case "$current" in
    "$base"*) return 0 ;;
    esac

    problem "$LOCK was REWRITTEN, not appended to. It is the record of what has already run on a"
    problem "user's database; the only legal change is new rows at the end."

    # Name the rows that stopped saying what they said, so the failure points at a line rather than
    # at a file. A row that is gone and a row whose hash changed are both "no longer present as
    # recorded", and both mean the same thing: a shipped migration just became editable.
    while IFS= read -r line; do
        case "$line" in
        '' | '#'*) continue ;;
        esac

        grep -qxF -- "$line" "$LOCK" || problem "  no longer recorded as it was at the merge base: $line"
    done <<<"$base"
}

usage() {
    printf 'usage: shipped-lock.sh [verify [--complete] | seal | init]\n' >&2
    exit 2
}

mode="${1:-verify}"

case "$mode" in
init)
    if [ -f "$LOCK" ]; then
        printf 'shipped-lock: %s already exists; init never overwrites a manifest.\n' "$LOCK" >&2
        exit 1
    fi
    [ -d "$DIR" ] || { printf 'shipped-lock: %s does not exist\n' "$DIR" >&2; exit 1; }
    header >"$LOCK"
    printf '  wrote %s with no rows — nothing has shipped yet\n' "$LOCK"
    ;;

verify)
    complete=0
    case "${2:-}" in
    '') ;;
    --complete) complete=1 ;;
    *) usage ;;
    esac

    if [ ! -f "$LOCK" ]; then
        if [ "$complete" -eq 1 ]; then
            printf 'shipped-lock: %s does not exist. A release cannot record which migrations\n' "$LOCK" >&2
            printf 'shipped without it. Create it with: bash scripts/shipped-lock.sh init\n' >&2
            exit 1
        fi
        printf '  no %s — nothing has shipped yet\n' "$LOCK"
        exit 0
    fi

    read_lock
    check_append_only

    if [ "$complete" -eq 1 ]; then
        # Every migration present at a tag ships with that tag, so every one of them must already
        # be listed. This is the check that turns "somebody forgot to seal the manifest" into a
        # failed release instead of a silent hole in the record.
        for f in "$DIR"/*.sql; do
            [ -e "$f" ] || continue
            base="${f##*/}"
            case "$listed" in
            *" $base "*) ;;
            *) problem "$base — present in $DIR but NOT listed in $LOCK; seal it with: make shipped-lock-seal" ;;
            esac
        done
    fi

    if [ "$fail" -ne 0 ]; then
        printf '\nshipped-lock: the manifest disagrees with the migrations, or with its own history.\n' >&2
        printf 'A migration that has shipped is frozen: it has already run on a user'"'"'s database, and\n' >&2
        printf 'editing it makes their schema and a fresh install silently diverge. To change what a\n' >&2
        printf 'shipped migration created, write a NEW migration: make migration NAME=<snake_case>\n' >&2
        exit 1
    fi

    n="$n_listed"
    if [ "$n" -eq 0 ]; then
        printf '  %s lists no migrations yet — nothing has shipped\n' "$LOCK"
    else
        printf '  %s shipped migration(s) unchanged\n' "$n"
    fi
    ;;

seal)
    [ -d "$DIR" ] || { printf 'shipped-lock: %s does not exist\n' "$DIR" >&2; exit 1; }
    [ -f "$LOCK" ] || header >"$LOCK"

    # Verify BEFORE appending. Sealing on top of a tree where a listed migration was already altered
    # — or where the manifest itself was rewritten — would launder the alteration into the record,
    # which is the one thing this file must never do.
    read_lock
    check_append_only

    if [ "$fail" -ne 0 ]; then
        printf '\nshipped-lock: refusing to seal — an already-shipped migration does not match its\n' >&2
        printf 'recorded hash. Fix that first; a seal on top of it would make the tampering permanent.\n' >&2
        exit 1
    fi

    added=0
    for f in "$DIR"/*.sql; do
        [ -e "$f" ] || continue
        base="${f##*/}"
        case "$listed" in
        *" $base "*) continue ;;
        esac
        printf '%s %s\n' "$base" "$(sha256_of "$f")" >>"$LOCK"
        printf '  sealed %s\n' "$base"
        added=$((added + 1))
    done

    if [ "$added" -eq 0 ]; then
        printf '  nothing to seal — every migration in %s is already listed\n' "$DIR"
    else
        printf '  %d migration(s) appended to %s — commit this in the Release PR\n' "$added" "$LOCK"
    fi
    ;;

*)
    usage
    ;;
esac
