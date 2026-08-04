#!/usr/bin/env bash
# Architectural grep gates and the licence firewall.
#
# Each rule has an id, and a failure prints file:line plus the rule that fired. These are the
# fences AGENTS.md's "four laws" and the AGPL firewall rely on — they are cheap, they run on every
# PR, and they are deliberately dumb: a grep an agent cannot argue with.
#
# Rules whose target tree does not exist yet pass vacuously. That is correct, not a hole: the rule
# is installed before the code it gates, which is the whole point (ROADMAP sequencing doctrine #1).

set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
note() { printf '  \033[33m%s\033[0m %s\n' "$1" "$2"; }
# violation <id> <description> <hits-as-one-newline-separated-string>
violation() {
    printf '\033[31mFAIL\033[0m [%s] %s\n' "$1" "$2"
    printf '%s\n' "$3" | sed 's/^/  /'
    fail=1
}
# strip_comments — drop whole-line comments so a gate never fires on prose describing itself
strip_comments() { grep -vE '^\s*[0-9]+:\s*#' || true; }

# has <dir> — true when the tree exists and contains at least one file
has() { [ -d "$1" ] && [ -n "$(find "$1" -type f -print -quit 2>/dev/null)" ]; }

# gate <id> <description> <tree> <extension-glob> <extended-regex> [allowlist-regex]
gate() {
    local id="$1" desc="$2" tree="$3" glob="$4" re="$5" allow="${6:-}"
    has "$tree" || { note "skip" "[$id] $tree does not exist yet"; return 0; }
    local hits
    hits=$(grep -rnE --include="$glob" "$re" "$tree" 2>/dev/null | strip_comments || true)
    [ -n "$allow" ] && hits=$(printf '%s\n' "$hits" | grep -vE "$allow" || true)
    if [ -n "$hits" ]; then
        violation "$id" "$desc" "$(printf '%s\n' "$hits" | head -20)"
    fi
}

echo "repo gates"

# --- Law 2: *sql.DB is held only by internal/store -------------------------------------------
gate SQL001 "sql.Open outside internal/store" \
    internal '*.go' '\bsql\.Open\(' '^internal/store/'
gate SQL002 "db.Query/db.Exec outside internal/store" \
    internal '*.go' '\b(db|DB)\.(Query|Exec)(Context)?\(' '^internal/store/'

# --- Law 3: internal/strategy is pure --------------------------------------------------------
gate PURE001 "internal/strategy imports internal/store" \
    internal/strategy '*.go' 'internal/store'
gate PURE002 "math/rand in internal/strategy (use the injected seeded Rng)" \
    internal/strategy '*.go' '"math/rand"'
gate CLOCK001 "time.Now outside internal/clock (use the injected Clock)" \
    internal '*.go' '\btime\.Now\(' '^internal/clock/'

# --- Money is integer centipoints ------------------------------------------------------------
for tree in internal/ledger internal/strategy; do
    gate MONEY001 "float type in $tree" "$tree" '*.go' '\b(float32|float64)\b'
done
gate MONEY002 "SQLite total() returns a float — use sum()" \
    db '*.sql' '\btotal\s*\('
gate MONEY003 "REAL/NUMERIC/DECIMAL column type" \
    db '*.sql' '\b(REAL|NUMERIC|DECIMAL)\b'

# --- Law 4: the SPA calls only the generated client -------------------------------------------
gate WEB001 "raw fetch/XMLHttpRequest outside web/src/api" \
    web/src '*.ts*' '\b(fetch|XMLHttpRequest)\s*\(' '^web/src/api/'
gate WEB002 "dangerouslySetInnerHTML" \
    web/src '*.tsx' 'dangerouslySetInnerHTML'

# --- Migrations are forward-only --------------------------------------------------------------
gate MIG001 "DDL inside a goose Down block (migrations are forward-only)" \
    db/migrations-sqlite '*.sql' '^\s*(DROP|ALTER)\b'

# --- The golden-file rewrite fence ------------------------------------------------------------
# Only `run:` lines count. A comment explaining the fence is not a breach of it.
if has .github/workflows; then
    hits=$(grep -rnE '^\s*(-?\s*(run|cmd):|\s{4,})[^#]*(\s|^)-{1,2}update\b' .github/workflows 2>/dev/null \
        | strip_comments \
        | grep -viE 'allow_?update_?branch|update-branch|dependabot|renovate|apt-get|actions/' || true)
    [ -n "$hits" ] && violation GOLD001 "'-update' in a CI command rewrites golden files" "$hits"
fi

# --- Supply chain: actions pinned to a SHA ----------------------------------------------------
if has .github; then
    # Any `uses:` that is not a local ./ action must carry a 40-char SHA. The action name itself
    # contains '/', so the exclusion has to be anchored on the value, not on a character class.
    hits=$(grep -rnE '^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*[^[:space:]]+' .github 2>/dev/null \
        | strip_comments \
        | grep -vE 'uses:[[:space:]]*\./' \
        | grep -vE '@[0-9a-f]{40}' || true)
    [ -n "$hits" ] && violation PIN001 "action not pinned to a 40-char commit SHA" "$hits"
fi

# --- The AGPL firewall ------------------------------------------------------------------------
# EQdkp Plus is AGPL-3.0; its game modules are CC BY-NC-SA. Reading a user's database at runtime is
# fine. Transcribing their PHP is a licence violation — and it is exactly what a helpful agent does
# when the task is "match EQdkp's behaviour".
AGPL_ALLOW='^(internal/importer/legacy_names\.go|internal/api/compat/|docs/|\.claude/|\.github/|scripts/repo-gates\.sh|AGENTS\.md|README\.md|ROADMAP\.md|CONTRIBUTING\.md)'
for tree in internal web cmd db; do
    has "$tree" || continue
    hits=$(grep -rnE '\b(pdh_|gen_class|plus_exchange|__multidkp2event)' "$tree" 2>/dev/null | strip_comments \
        | grep -vE "$AGPL_ALLOW" || true)
    [ -n "$hits" ] && violation AGPL001 "EQdkp Plus identifier outside the allowlisted files" "$hits"
done

if [ "$fail" -ne 0 ]; then
    printf '\n\033[31mrepo gates failed\033[0m — see the rule ids above.\n'
    printf 'These are structural rules, not style. Do not disable one to land a change (AGENTS.md).\n'
    exit 1
fi

printf '  \033[32mrepo gates passed\033[0m\n'
