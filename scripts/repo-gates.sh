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
# DKP_REPO_ROOT lets a test point the gates at a tree other than this checkout. It exists so the
# gates can be *tested rather than trusted*: a test writes a deliberately tainted tree into
# t.TempDir() — an unpinned action, a stray sql.Open — and requires this script to exit non-zero.
# Such a fixture cannot live inside the repo, because the real `make lint-repo` would find it and
# fail the project's own CI. Unset (the CI and local default), behaviour is exactly as before.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

fail=0
note() { printf '  \033[33m%s\033[0m %s\n' "$1" "$2"; }
# violation <id> <description> <hits-as-one-newline-separated-string>
violation() {
    printf '\033[31mFAIL\033[0m [%s] %s\n' "$1" "$2"
    printf '%s\n' "$3" | sed 's/^/  /'
    fail=1
}
# strip_comments — drop whole-line comments so a gate never fires on prose describing itself.
#
# The pattern must skip the `path:line:` prefix that `grep -rn` prints. The original was anchored
# at `^[0-9]+:`, which never matched that prefix, so this function silently stripped nothing and
# every gate was quietly firing on its own documentation. Nothing had gone red only because no
# comment happened to contain a banned token yet — the first one to do so would have looked like a
# real violation. A helper that does nothing is worse than an absent one: the call sites read as
# though the case is handled.
strip_comments() { grep -vE '^[^:]*:[0-9]+:[[:space:]]*#' || true; }

# strip_go_comments — the same, for Go's '//'.
strip_go_comments() { grep -vE '^[^:]*:[0-9]+:[[:space:]]*//' || true; }

# has <dir> — true when the tree exists and contains at least one file
has() { [ -d "$1" ] && [ -n "$(find "$1" -type f -print -quit 2>/dev/null)" ]; }

# gate <id> <description> <tree> <extension-glob> <extended-regex> [allowlist-regex]
#
# Whole-line comments are dropped in BOTH syntaxes before matching, so a rule never fires on the
# prose that documents it. The AGPL firewall below deliberately does not do this — see its note.
gate() {
    local id="$1" desc="$2" tree="$3" glob="$4" re="$5" allow="${6:-}"
    has "$tree" || { note "skip" "[$id] $tree does not exist yet"; return 0; }
    local hits
    hits=$(grep -rnE --include="$glob" "$re" "$tree" 2>/dev/null | strip_comments | strip_go_comments || true)
    [ -n "$allow" ] && hits=$(printf '%s\n' "$hits" | grep -vE "$allow" || true)
    if [ -n "$hits" ]; then
        violation "$id" "$desc" "$(printf '%s\n' "$hits" | head -20)"
    fi
}

echo "repo gates"

# --- Law 2: *sql.DB is held only by internal/store -------------------------------------------
# cmd/ is gated as well as internal/: `sql.Open` in a Cobra command is the same violation, and it
# is where a "just for the migrate subcommand" handle would be reached for first.
for tree in internal cmd; do
    # sql.OpenDB, not only sql.Open. Interposing anything on connections — the statement counter in
    # internal/store does exactly this — requires OpenDB, so a gate that watched only sql.Open
    # would be blind to the very call the escape hatch needs.
    gate SQL001 "sql.Open/sql.OpenDB outside internal/store" \
        "$tree" '*.go' '\bsql\.Open(DB)?\(' '^internal/store/'

    # Receiver-independent. The previous form matched only receivers literally named db or DB, so
    # renaming the variable to `conn` walked straight through it.
    #
    # `\(([^)]|$)` excludes a ZERO-ARGUMENT .Query() — net/url's Values accessor, which appears all
    # over internal/api and cannot be a database call, since every real one takes at least the SQL
    # string. The `|$` arm keeps a call whose arguments wrap to the next line matched.
    #
    # The exclusion lives in the PATTERN, not in the allowlist, and that distinction is the whole
    # rule. An allowlist is applied to the entire `path:line:text` line, so exempting `.Query()`
    # there would drop any line that merely MENTIONED it — including
    #   conn.QueryContext(ctx, "SELECT ..."+r.URL.Query().Get("q"))
    # which is the single most natural shape a law-2 violation takes, and even
    #   db.ExecContext(ctx, del) // filters come from r.URL.Query()
    # Do not "simplify" this back into the allowlist.
    gate SQL002 ".Query/.Exec outside internal/store" \
        "$tree" '*.go' '\.(Query|QueryRow|Exec)(Context)?\(([^)]|$)' '^internal/store/'
done

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
# The same ban over Go, because SQL reaches SQLite as a Go string literal just as readily as from a
# .sql file — and db/ does not exist yet, so until PR 3 the .sql gate above skips vacuously and
# this is the only one of the pair that runs.
for tree in internal cmd; do
    gate MONEY002 "SQLite total() returns a float — use sum()" \
        "$tree" '*.go' '\btotal\s*\('
done
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

# Atlas emits `dkp_meta` — MySQL-style backtick quoting, which SQLite accepts as a compatibility
# extension. sqlc's SQLite parser does not, and it fails SILENTLY: the schema file is accepted, no
# table is parsed out of it, and the error surfaces as `relation "x" does not exist` against the
# query file, which is the one file that was correct. scripts/new-migration.sh rewrites them to
# double quotes at generation time; this gate is what catches a migration that got here another
# way, because "make gen produced an empty sqlitegen package" is not a thing anyone notices.
gate MIG002 "backtick-quoted identifier in a migration (sqlc parses no table and says so about the query)" \
    db/migrations-sqlite '*.sql' '`'

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
#
# This gate does NOT strip comments, and that is the one place where the difference matters.
# Everywhere else a banned token inside a comment is prose about the rule; here it is the thing
# itself. Transcribing EQdkp's PHP into a Go comment is exactly as much of a licence violation as
# transcribing it into code, and "I only pasted it as a reference" is precisely how it happens.
AGPL_ALLOW='^(internal/importer/legacy_names\.go|internal/api/compat/|docs/|\.claude/|\.github/|scripts/repo-gates\.sh|AGENTS\.md|README\.md|ROADMAP\.md|CONTRIBUTING\.md)'
for tree in internal web cmd db; do
    has "$tree" || continue
    hits=$(grep -rnE '\b(pdh_|gen_class|plus_exchange|__multidkp2event)' "$tree" 2>/dev/null \
        | grep -vE "$AGPL_ALLOW" || true)
    [ -n "$hits" ] && violation AGPL001 "EQdkp Plus identifier outside the allowlisted files" "$hits"
done

# --- AGPL002: EQdkp config keys are not DKP column names --------------------------------------
# A narrower rule than AGPL001, and a different failure. AGPL001 catches distinctive EQdkp
# identifiers (`pdh_`, `gen_class`) that could only arrive by transcription. These are ordinary
# names that arrive by a subtler route: someone reads docs/design/05-migration.md's list of EQdkp
# `<prefix>config` keys — which is there because the importer must read them — and writes one into
# DKP's own schema instead of DKP's name for the same concept.
#
# That is not hypothetical. docs/design/02-api-design.md's `/guild` row shipped `inactive_period`
# and `auto_set_active` for exactly this reason, and `auto_set_active` is the OPPOSITE control from
# DKP's `auto_set_inactive` — a bot written from the published contract would have set the wrong
# value. Some keys in that same row had been correctly renamed (`dkp_name` -> `points_label`,
# `guildtag` -> `tag`), which is what made the survivors invisible.
#
# SCOPE IS db/ ONLY, AND THE DOCUMENTATION HALF IS DELIBERATELY NOT GATED. A grep over the design
# documents cannot tell a leak from a lesson: docs/design/01-domain-model.md names `show_twinks` at
# :572 and :2870 to explain why DKP rejects that design, and the correction notes accompanying this
# rule quote the banned names in order to document them. Both are correct writing a grep would
# reject, and a gate that is usually wrong is a gate people route around. The wire half is covered
# where it is unambiguous — SPEC008 in scripts/verify-spec.py reads the generated OpenAPI document,
# which contains field names and no prose.
#
# `hide_inactive` and `timezone` are in EQdkp's list too and are ALSO DKP's own column names: the
# concepts coincide and the words are ordinary English. They are not banned. Every name below is one
# DKP does not use, so a hit is always a transcription and never a collision.
if has db; then
    hits=$(grep -rnE '\b(inactive_period|auto_set_active|round_activate|round_precision|dkp_name|guildtag|servername|show_twinks|detail_twink|special_members|default_game|enable_leaderboard)\b' db 2>/dev/null \
        | strip_comments | strip_go_comments || true)
    [ -n "$hits" ] && violation AGPL002 \
        "EQdkp Plus config key used as a DKP schema name — use DKP's own name (canonical §15, §16)" \
        "$hits"
fi

if [ "$fail" -ne 0 ]; then
    printf '\n\033[31mrepo gates failed\033[0m — see the rule ids above.\n'
    printf 'These are structural rules, not style. Do not disable one to land a change (AGENTS.md).\n'
    exit 1
fi

printf '  \033[32mrepo gates passed\033[0m\n'
