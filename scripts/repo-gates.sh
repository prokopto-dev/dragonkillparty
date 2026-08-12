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
# Resolved BEFORE the cd below, because MIG003 delegates to a sibling script. DKP_REPO_ROOT points
# at the tree being INSPECTED, which for a negative fixture is a t.TempDir() with no scripts/ in it;
# the gate implementations always live here, next to this file.
gates_dir="$(cd "$(dirname "$0")" && pwd)"
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

    # internal/store exports raw-SQL TEST helpers — ExecForTest, ExecErrForTest, QueryForTest,
    # QueryRowForTest, TxForTest, TxHandleForTest — so tests in other packages can reach the
    # unexported pool and *sql.Tx state that a genuine mutation runs against. The raw ExecContext /
    # QueryContext they wrap lives in internal/store/testing.go, which SQL002 already allows.
    # Production code must never call them: they take a testing.TB (which cannot exist in a shipped
    # binary) precisely so misuse fails to compile, and this gate is the belt to that suspenders.
    # The allowlist exempts *_test.go call sites and the definition file; a hit anywhere else is a
    # test affordance leaking into the binary. This machine check replaces the earlier, fragile
    # convention of naming the wrapper method `Do` so the SQL002 grep could not see `h.Do(...)`.
    gate SQL003 "ForTest raw-SQL helper called outside _test.go" \
        "$tree" '*.go' '\b(ExecForTest|ExecErrForTest|QueryForTest|QueryRowForTest|TxForTest|TxHandleForTest)\b' \
        '(_test\.go:|store/testing\.go:)'
done

# --- Law 3: internal/strategy is pure --------------------------------------------------------
gate PURE001 "internal/strategy imports internal/store" \
    internal/strategy '*.go' 'internal/store'
gate PURE002 "math/rand in internal/strategy (use the injected seeded Rng)" \
    internal/strategy '*.go' '"math/rand"'
gate CLOCK001 "time.Now outside internal/clock (use the injected Clock)" \
    internal '*.go' '\btime\.Now\(' '^internal/clock/'

# CLOCK002 — the hole CLOCK001 cannot see, found in review of Phase 0 PR 10b.
#
# internal/strategy legitimately imports internal/clock, because strategy.Ctx.Clock() returns a
# clock.Clock. Nothing above stops a strategy from then writing `clock.System{}.Now()`, which reads
# the REAL wall clock: CLOCK001 greps for `time.Now(`, which that is not; the arch test's
# direct-import ban sees only `internal/clock`, which is allowed; and forbidigo's `^time\.Now$`
# resolves to a method on clock.System. A plan that depends on when it ran cannot be replayed, which
# is the entire reason the clock is injected.
#
# clock.System is the ONLY real-clock path out of that package — Clock is an interface, Fake is a
# test double, and System.Now is the one function in the repository that calls time.Now — so banning
# the identifier closes it rather than narrowing it. Scoped to internal/strategy: cmd/ wiring
# constructs a System on purpose, which is where a real clock is supposed to come from.
#
# The AST twin is TestArch_Strategy_DoesNotConstructTheRealClock, which also catches an ALIASED
# import (`import c ".../internal/clock"`) that this grep would miss.
gate CLOCK002 "clock.System in internal/strategy (the clock is injected through Ctx.Clock)" \
    internal/strategy '*.go' '\bclock\.System\b'

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

# --- WEB003: the SPA fetches no asset from a third party --------------------------------------
#
# docs/design/09-frontend-and-design-system.md §3: the type is "loaded self-hosted, not through the
# Google Fonts @import the source sheet uses — the binary serves the SPA offline and a
# render-blocking third-party request contradicts that". §5 says the same of the icon subset.
#
# This is a gate rather than a sentence in a document because the tempting line is already written
# and sitting in the repository: mockups/nocturne/styles.css opens with
# `@import url('https://fonts.googleapis.com/...')`, and the mockups are transcribed on purpose.
# Copying that one line is a single keystroke, it looks like fidelity, and nothing about the result
# looks wrong on a developer's machine — it only fails on a volunteer's LAN-only server, where the
# request hangs and the page renders in the wrong face after a three-second block.
#
# BANNED: ANY off-origin URL literal — an absolute `https?://host`, or a protocol-relative `//host`.
# Not a list of asset-bearing shapes.
#
# The first version of this rule did enumerate the shapes: `url(…)`, `@import url(…)`, `<link …>`,
# and the known CDNs by name. It was reviewed and it was wrong — `<script src="https://…">`,
# `<img src="//…">` and the perfectly valid `@import "https://…";` (the quoted spelling, no `url()`)
# all walked straight through, and every one of them makes the SPA depend on another origin exactly
# as much as the shapes that were caught. An enumeration is only ever as complete as the imagination
# of whoever wrote it, and the failure mode is silent: the gate stays green while the offline
# contract regresses.
#
# So the rule is the invariant itself. The SPA is served from one origin and reaches no other for
# anything, so a second origin's name has no business appearing in it at all — in an attribute, a
# stylesheet, a dynamic import, a Worker URL, or a form nobody has thought of yet. Deliberately
# broader than "assets": an `<a href="https://…">` would trip it too, and that is a decision worth
# making in a diff rather than a hole to leave open (a link leaks a referrer and a screen full of
# them is a design change). Nothing in the tree needs one today; the day one does, it is one
# allowlisted line here.
#
# The host must contain a dot. That is what separates `//example.com` from a trailing `//` JS comment
# or a doubled slash in a path, and it is why the rule can be this broad without being noisy.
#
# The named font and script CDNs stay in the pattern even though the generic rule subsumes them:
# they cost nothing, they name the specific mistake §3 is about, and they still fire on a bare
# `fonts.googleapis.com` written with no scheme at all.
#
# ALLOWED: web/src/api/, the generated client — the same allowlist WEB001 above uses, for a related
# reason. Those files are generated from openapi/openapi.json; a URL in them is a spec description or
# a documentation link, a `.d.ts` cannot fetch anything, and the client's own base URL comes from the
# runtime /config.json rather than a literal. If a real off-origin load ever appears there, the spec
# is the bug and hand-editing the generated file is not the fix.
#
# Comment lines are stripped, in all four spellings the scanned files use. Prose about the rule is
# not a breach of it: web/src/styles/fonts.css explains what it is NOT doing and why, and a gate
# that fires on its own documentation is a gate people route around. This is the opposite choice
# from the AGPL firewall below, which strips nothing — there, the transcription in the comment IS
# the violation; here, a URL nobody requests is a URL nobody requests.
#
# index.html is scanned as well as web/src: it is the document the browser parses first, so a
# render-blocking <link> or <script> in its <head> is the worst version of exactly this bug and the
# only place it can be written outside src.
if has web/src; then
    off_origin='(https?:)?//[a-zA-Z0-9-]+(\.[a-zA-Z0-9-]+)+'
    third_party_asset="($off_origin|fonts\.googleapis\.com|fonts\.gstatic\.com|fonts\.bunny\.net|use\.typekit\.net|cdn\.jsdelivr\.net|unpkg\.com|cdnjs\.cloudflare\.com)"
    hits=$(grep -rnE --include='*.css' --include='*.ts' --include='*.tsx' --include='*.html' \
        "$third_party_asset" web/src web/index.html 2>/dev/null \
        | grep -vE '^web/src/api/' \
        | grep -vE '^[^:]*:[0-9]+:[[:space:]]*(/\*|\*|//|#|<!--)' || true)
    [ -n "$hits" ] && violation WEB003 \
        "off-origin URL in the SPA — type, icons and every other asset are self-hosted so the binary works offline (docs/design/09 §3)" \
        "$hits"
fi

# --- Canonical §17: raw hex and raw px live in the token layer, nowhere else -------------------
#
# canonical §17 promises this rule and names ESLint as the mechanism. ESLint does not lint CSS, so
# the promise went unkept and web/src/styles/base.css shipped a `text-underline-offset: 3px` that
# nothing caught (found in review of this gate's own PR). A grep does the whole job here and costs no
# dependency, which is the same argument WEB001 above makes for itself.
#
# THE TOKEN LAYER IS tokens.css ALONE, not all of web/src/styles. canonical §17 is normative and says
# "outside the token layer"; .claude/rules/web.md says "outside web/src/styles/", which is looser.
# AGENTS.md resolves that conflict in canonical's favour, so this gate implements canonical and the
# rules file's wording is the thing that needs correcting.
#
# SCOPED TO CSS, because canonical §17's rule needs TWO mechanisms and this is the dumb half — the
# same split law 4 uses above, for the same reason. A grep over *.tsx cannot tell a value from prose:
# web/src/routes/design.tsx renders the sentences "Base unit 4px x 0.70" and "a 1px accent border on
# transparent" as visible copy, and both would trip it. So:
#
#   this gate          CSS, by grep, in `lint / repo` — no Node toolchain needed
#   eslint.config.js   TS/TSX, by AST, in `lint / web` — sees `style={{ padding: "4px" }}` and knows
#                      JSX text is not a string literal
#
# Between them the rule holds over all of web/src, which is what §17 states. NEITHER IS SUFFICIENT
# ALONE and removing either is a §17 regression. The AST half's negative fixture is
# web/test-fixtures/lint/raw-token-values.tsx.
#
# The generic `gate` helper is not used because its comment stripping knows `#` and `//`, not CSS's
# `/* */`. Every component sheet here documents its own values in prose — "a 1px accent border",
# "fades over 48px at each end", "a 4000px-tall spacer" — so without stripping CSS comment lines this
# rule would fire almost entirely on its own documentation.
if has web/src; then
    css_lines=$(find web/src -name '*.css' -type f -print0 2>/dev/null \
        | xargs -0 grep -nE '.' 2>/dev/null \
        | grep -vE '^web/src/styles/tokens\.css:' \
        | grep -vE '^[^:]*:[0-9]+:[[:space:]]*(/\*|\*|//)' || true)

    hits=$(printf '%s\n' "$css_lines" | grep -E '#([0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{4}|[0-9a-fA-F]{3})\b' || true)
    [ -n "$hits" ] && violation DS001 \
        "raw hex colour outside web/src/styles/tokens.css — a colour with no named rung is a colour no guild theme can reach (canonical §17)" \
        "$hits"

    hits=$(printf '%s\n' "$css_lines" | grep -E '[0-9]+(\.[0-9]+)?px' || true)
    [ -n "$hits" ] && violation DS002 \
        "raw px outside web/src/styles/tokens.css — a value the scale does not carry gets a named rung, not an inline literal (canonical §17)" \
        "$hits"
fi

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

# MIG003 — a migration that has shipped is frozen.
#
# db/migrations-sqlite/SHIPPED.lock records `filename sha256` for every migration that has appeared
# in a tagged release, so a row in it means that file has ALREADY RUN on somebody's database.
# Editing it makes an existing install and a fresh install end up with different schemas, and
# "works on a fresh install, breaks on upgrade" is the most damaging bug class for this audience.
#
# TWO assertions, and the second is the one that makes the first mean anything:
#
#   1. Every listed file exists and still hashes to its recorded value.
#   2. The manifest at the MERGE BASE is an exact byte PREFIX of the manifest now.
#
# Without (2), (1) is trivially defeated by changing both halves in the same commit: edit the
# migration, rewrite its row — or just delete the row, which un-freezes the file entirely — and the
# tree is self-consistent again. The manifest ships in the same diff as the migration it protects,
# so it can only be trusted against its own history.
#
# (2) needs git history, so it SKIPS loudly when the base cannot be read (a shallow checkout).
# ci.yml's `lint / repo` job carries `fetch-depth: 0` and TestCI_LintRepoJob_FetchesFullHistory
# fails if that is removed, which is what stops the skip from becoming the normal case.
#
# This is NOT what atlas.sum already covers, and the difference is the reason the gate exists.
# atlas.sum protects the current set as it is: edit a migration, re-run `atlas migrate hash`, and
# the checksum agrees again — `make verify-generated` is satisfied because regeneration is
# idempotent, and the edited file ships. SHIPPED.lock records history instead of state, and nothing
# in this repository is allowed to rewrite it.
#
# Deliberately NOT a completeness check. A migration added on a feature branch has not shipped and
# must not be listed yet, so requiring every file to appear here would fire on the one change the
# rule is supposed to permit. Completeness is checked once, at tag time, by
# `make release-shipped-lock` in release.yml's `prepare` job.
#
# The check itself lives in internal/migrate/shippedlock because the release path runs the same code
# with one more assertion; a second copy here would be a second implementation nobody keeps in step.
# It is Go rather than shell (issue #129) so the row parsing and the merge-base prefix comparison —
# the highest-blast-radius logic in this file's reach — are unit-tested directly rather than only
# through this subprocess. `go` runs from THIS checkout, never from the tree under inspection: the
# negative fixtures are t.TempDir() trees with no module in them.
if [ -f db/migrations-sqlite/SHIPPED.lock ]; then
    # No Go, no check — and that is a FAILURE, not a skip, for the same reason the command itself
    # hard-fails on a migration it cannot read: a hash gate that cannot hash must not report green.
    # The `lint / repo` job installs the toolchain for exactly this.
    if ! command -v go >/dev/null 2>&1; then
        violation MIG003 "go is not on PATH, so db/migrations-sqlite/SHIPPED.lock could not be checked" \
            "install the toolchain with: make setup"
    elif lock_out=$(DKP_REPO_ROOT="$PWD" go -C "$gates_dir/.." run ./internal/migrate/shippedlock verify 2>&1); then
        printf '%s\n' "$lock_out"
    else
        violation MIG003 "a migration listed in db/migrations-sqlite/SHIPPED.lock was modified or deleted" \
            "$lock_out"
    fi
else
    note "skip" "[MIG003] db/migrations-sqlite/SHIPPED.lock does not exist yet"
fi

# --- ENUM001: a string-enum CHECK comes from a Go catalogue, never from a literal --------------
#
# Canonical §5: "both the SQL CHECK constraint and the OpenAPI enum are generated from one Go
# catalogue". Every string-enum CHECK in db/schema.hcl now is — ledger_batch.kind/source,
# audit_log.actor_kind/outcome, account.kind/system_key — and each has a test asserting its own
# region matches its own catalogue. NONE of them says anything about a SEVENTH enum, which is the
# hole this closes: a brand-new table added with
#
#   check "bid_session_state_enum" { expr = "state IN ('draft', 'open', 'extended')" }
#
# and no catalogue passes all three of those tests, `make verify-generated` and `make check`. The
# rule that it should not was prose in .claude/rules/migrations.md and AGENTS.md, and prose is what
# produced this finding three times already (#29 fixed two enums, #40 a third, #51/#53 the last
# three).
#
# canonical §5 makes bid.tier's DECLARATION ORDER semantic — the resolution ladder — so a literal
# that agrees with the Go list on values and disagrees on order is a resolver bug that no schema
# comparison would ever see. That is the expensive version of this defect and it is coming in Phase
# 1 or 2, in a PR whose author is thinking about bidding rather than about enum plumbing.
#
# SCOPED TO `check` BLOCKS, structurally, and to lists containing a QUOTED value:
#
#   caught   expr = "state IN ('draft', 'open')"                     the plain form (CheckExpr)
#   caught   expr = "k IS NULL OR k IN ('a', 'b')"                   the nullable form (NullableCheckExpr)
#   caught   expr = "state IN (\"draft\", \"open\")"                 SQLite's double-quoted literals
#   caught   expr = "state in ('draft', 'open')"                     SQL keywords are case-insensitive
#   caught   expr = "state IN /* set */ ('draft', 'open')"           comments are removed before scanning
#   ignored  expr = "hide_inactive IN (0, 1)"                        a boolean, not a string enum
#   ignored  expr = "retry IN (0, -- not 'draft'\n 1)"               a comment is not a vocabulary
#   ignored  where = "state IN ('open', 'extended')"                 an index predicate, NOT a check
#
# BOTH SQL QUOTE FORMS, and that pair is closed rather than an enumeration that might be missing an
# arm: SQLite produces a string literal from an apostrophe and — through the double-quoted-identifier
# misfeature it keeps for compatibility — from a double quote whose token resolves to no column. The
# other two quoting characters it accepts, backticks and brackets, are identifier quoting and cannot
# express a value. So a hand-written enum can be spelled exactly two ways and the gate reads both;
# changing quote style is not a way past it.
#
# The index-predicate exclusion is deliberate and is #97: a partial index over a SUBSET of a
# vocabulary is not the vocabulary, so it cannot be rendered from a catalogue as-is and a gate that
# demanded it would fire on correct work.
#
# The scan tracks the check block STRUCTURALLY and carries an unfinished IN list across lines, so a
# wrapped or heredoc expression is read as the one expression it is. A line-scoped scan misses that
# shape entirely — `IN (` alone on its line, then the values on theirs — which would have made the
# longest vocabularies, the ones most worth generating, the ones that walked through.
#
# The waiver is `// dkp:enum-literal <reason>` on the line above the check, the same in-tree marker
# idiom as `-- dkp:destructive-approved:` and `-- dkp:fixture deliberately-broken`. It lives in the
# schema rather than in an allowlist here so that an exception is visible in the diff a reviewer
# reads, and it requires a reason for the same purpose the ADR waiver below does.
#
# A GENERATED REGION IS ONE A CATALOGUE OWNS, NOT ONE THE SCHEMA CLAIMS. The markers are comments, so
# without this the exemption is self-service: wrap the new literal in a balanced
# `// BEGIN GENERATED` / `// END GENERATED` pair and the gate steps over it — and nothing downstream
# notices either, because `make gen` only rewrites the regions its catalogues declare and a
# fabricated one is not among them. So the marker line must match, WHOLE, a marker some catalogue
# declares in Go, and the marker set is read out of the Go source rather than restated here:
#
#   internal/*/kinds/*.go:  schemaEnumBegin = "  // BEGIN GENERATED — … Run `make gen`."
#
# Whole-line identity is the same rule schemaenum.Region.Replace uses to find its own region, which
# is what makes the two agree by construction. A marker nothing declares is a VIOLATION, not a silent
# non-exemption: it means either a fabricated region or a catalogue and a schema that have drifted,
# and both need saying. The Go twin is TestEnumMarkers_InSchema_AreExactlyTheRegisteredCatalogues,
# which closes the residue this cannot see — a marker const declared in Go but wired into no
# generator.
#
# An UNCLOSED `BEGIN GENERATED` is itself a violation too. Without that, one unbalanced marker line
# exempts every check after it — the whole rest of the file — and the gate stays green while doing
# nothing.
if [ -f db/schema.hcl ]; then
    # The marker lines the Go catalogues declare, extracted from the const block each one keeps them
    # in. Empty when internal/ does not exist yet, which fails CLOSED: with no catalogue to own it,
    # no region is generated and every marker is unrecognised.
    #
    # Joined with ASCII FS rather than newlines: a multi-line `awk -v` value is a parse error in BWK
    # awk ("newline in string"), and a marker line can hold anything except a control character.
    enum_markers=$(grep -rhE '^[[:space:]]*schemaEnum(Begin|End)[[:space:]]*=[[:space:]]*"' \
        --include='*.go' internal 2>/dev/null | sed -E 's/^[^"]*"//; s/"[[:space:]]*$//' | tr '\n' '\034' || true)

    # Explicit character ranges rather than POSIX classes: this runs under GNU awk on CI and BWK awk
    # on a laptop, and the class support differs between them. The two quote characters are built
    # with sprintf so that neither has to survive the shell's quoting of this program.
    if hits=$(awk -v known_markers="$enum_markers" '
        BEGIN {
            SQ = sprintf("%c", 39)
            DQ = sprintf("%c", 34)

            # mk, not m: m is a scalar in the brace counter below, and awk has one namespace for
            # both — a collision is a fatal "array in a scalar context" under gawk and a silently
            # different answer under BWK awk.
            count = split(known_markers, mk, sprintf("%c", 28))
            for (i = 1; i <= count; i++) {
                if (mk[i] != "") { declared[mk[i]] = 1 }
            }
        }

        # strip_sql_comments — remove SQL comments from an expression before it is scanned.
        #
        # `state IN -- why these\n  (${SQ}a${SQ})` and `state IN /* set */ (${SQ}a${SQ})` are the same
        # CHECK as the one written without them, so the scanner has to see the same thing. Removing
        # the comments is what makes the token boundary between the keyword and its parenthesis
        # insensitive to whatever a person wrote in the gap, rather than a list of gaps the pattern
        # happens to allow — the enumeration failure this rule has now been bitten by three times.
        #
        # Block-comment state is file-scoped, like the list state, because `/*` may close lines later.
        #
        # STRING CONTEXT IS DELIBERATELY NOT TRACKED, and the direction of that error is the reason it
        # is safe. A `--` inside a value truncates the rest of the line, which can only REMOVE text
        # from the scan — and never the quote that opens the literal it appears in, since that quote
        # comes first and has already been counted. So this can lose the closing parenthesis of a list
        # it has already reported; it cannot hide a vocabulary. The alternative, a full SQL lexer in
        # awk, buys nothing a grep gate needs.
        function strip_sql_comments(s,   out, i, n, ch, nxt) {
            out = ""
            n = length(s)
            i = 1

            while (i <= n) {
                ch = substr(s, i, 1)
                nxt = substr(s, i + 1, 1)

                if (in_block_comment) {
                    if (ch == "*" && nxt == "/") { in_block_comment = 0; i += 2 }
                    else { i++ }
                    continue
                }

                if (ch == "-" && nxt == "-") { break }
                if (ch == "/" && nxt == "*") { in_block_comment = 1; i += 2; continue }

                out = out ch
                i++
            }

            return out
        }

        # quoted_in_list — does an IN list hold a quoted value? Carries an unfinished list ACROSS
        # LINES, because a wrapped or heredoc expression is a normal way to write a long one:
        #
        #   expr = <<-SQL
        #     state IN (
        #       ${SQ}draft${SQ},
        #       ${SQ}open${SQ}
        #     )
        #   SQL
        #
        # A line-scoped scanner reads `IN (` with no quote after it, then two value lines with no
        # `IN (` on them, and finds nothing — which is how the longest vocabularies, the ones most
        # worth generating, would have been the ones that walked through. list_depth is therefore
        # file-scoped state: the scan runs character by character, entering a list at `IN (`, and
        # staying in it until the parenthesis that closes it however many lines later.
        #
        # Character-wise rather than by regex because the question is about the text BETWEEN the
        # parentheses — `IN (0, 1)` is a boolean and must not match, a quote anywhere in a list is a
        # vocabulary — and because nesting has to be counted rather than assumed away.
        #
        # `[Ii][Nn]`, because SQL keywords are case-insensitive: the uppercase the generator emits is
        # a convention rather than a rule, and a hand-written CHECK — the only kind this gate ever
        # sees — is written in whatever case its author was typing in. Spelled as a character class
        # rather than with IGNORECASE, which is a gawk extension and off under --posix. The leading
        # non-word character keeps JOIN and MIN out and still does: what precedes the keyword decides,
        # not its case.
        # The keyword and its parenthesis may also be split across the line break — the same shape as
        # the case above, one token earlier — so a line ending in the bare keyword arms pending_in and
        # the next line opening with `(` enters the list. A line with nothing left on it returns
        # early rather than clearing that state, which is what carries the keyword across a blank or
        # comment-only line between the two.
        function quoted_in_list(s,   i, n, ch, hit) {
            if (s ~ /^[ \t]*$/) { return 0 }

            n = length(s)
            i = 1

            if (list_depth == 0 && pending_in && match(s, /^[ \t]*\(/)) {
                i = RSTART + RLENGTH
                list_depth = 1
                reported = 0
            }

            pending_in = 0

            while (i <= n) {
                if (list_depth > 0) {
                    ch = substr(s, i, 1)

                    if (ch == "(") { list_depth++ }
                    else if (ch == ")") { list_depth-- }
                    else if (ch == SQ || ch == DQ) { hit = 1 }

                    i++
                    continue
                }

                if (!match(substr(s, i), /(^|[^A-Za-z0-9_])[Ii][Nn][ \t]*\(/)) {
                    if (match(substr(s, i), /(^|[^A-Za-z0-9_])[Ii][Nn][ \t]*$/)) {
                        pending_in = 1
                        list_line = NR
                        list_text = $0
                    }

                    break
                }

                # Just past the opening parenthesis the match ended on.
                i = i + RSTART + RLENGTH - 1
                list_depth = 1
                list_line = NR
                list_text = $0
                reported = 0
            }

            return hit
        }

        index($0, "BEGIN GENERATED") {
            if ($0 in declared) { in_region = 1; begin_line = NR }
            else {
                printf "db/schema.hcl:%d: BEGIN GENERATED marker no Go catalogue declares — a region is generated only if a catalogue in internal/*/kinds owns it:%s\n", NR, $0
            }
            next
        }

        index($0, "END GENERATED") {
            if ($0 in declared) { in_region = 0 }
            else {
                printf "db/schema.hcl:%d: END GENERATED marker no Go catalogue declares:%s\n", NR, $0
            }
            next
        }

        # Comment lines, in both HCL spellings. A gate that fires on the prose documenting it is a
        # gate people route around — and db/schema.hcl'"'"'s own header names the enum shape.
        /^[ \t]*(\/\/|#)/ {
            if ($0 ~ /dkp:enum-literal[ \t]+[^ \t]+[ \t]*[^ \t]/) { waived = 1 }
            else if ($0 ~ /dkp:enum-literal/) { waived = 0; bare_waiver = NR }
            next
        }

        # Entering a check block. The name is what a reviewer keys a waiver to, so it is carried
        # into the message.
        /^[ \t]*check[ \t]+"/ {
            in_check = 1
            depth = 0
            name = $0
            sub(/^[ \t]*check[ \t]+"/, "", name)
            sub(/".*$/, "", name)
            this_waived = waived

            # A list cannot span two check blocks. Clearing it here means an unbalanced parenthesis
            # in one block cannot swallow the next one.
            list_depth = 0
            pending_in = 0
            in_block_comment = 0
        }

        in_check {
            # The list is reported at the line it STARTED on — where the author is looking — and
            # once, however many quoted values follow it.
            if (!in_region && !this_waived && quoted_in_list(strip_sql_comments($0)) && !reported) {
                reported = 1
                printf "db/schema.hcl:%d: check \"%s\": %s\n", list_line, name, list_text
            }

            n = gsub(/[{]/, "&"); m = gsub(/[}]/, "&")
            depth += n - m
            if (depth <= 0) { in_check = 0; list_depth = 0; pending_in = 0; in_block_comment = 0 }
        }

        # A blank line or any other statement ends the waiver'"'"'s reach: it applies to the check
        # block it sits above, not to the rest of the file.
        !in_check { waived = 0 }

        END {
            if (in_region) {
                printf "db/schema.hcl:%d: unclosed BEGIN GENERATED marker — every check after it is silently exempt\n", begin_line
            }
            if (bare_waiver) {
                printf "db/schema.hcl:%d: dkp:enum-literal with no reason — the reason is the point of the waiver\n", bare_waiver
            }
        }
    ' db/schema.hcl 2>&1); then
        [ -n "$hits" ] && violation ENUM001 \
            "hand-written string-enum CHECK in db/schema.hcl — the values come from a Go catalogue between the BEGIN/END GENERATED markers (canonical §5, .claude/rules/migrations.md)" \
            "$hits"
    else
        # A scan that CANNOT RUN is a failure, never a pass. Without this the awk program's own
        # errors — a fatal on the wrong awk, an unreadable file — go to stderr and leave `hits`
        # empty, which reads exactly like a clean schema. That is the shape of bug that makes a gate
        # stop meaning anything without anybody noticing, and it was found in review of this rule.
        violation ENUM001 "the string-enum CHECK scan did not run — this is a gate failure, not a pass" \
            "$hits"
    fi
else
    note "skip" "[ENUM001] db/schema.hcl does not exist yet"
fi

# --- ADR001: a change that needs a decision record carries one --------------------------------
#
# docs/adr/README.md and docs/design/07-documentation-system.md both said, in bold, that this was
# enforced in `lint / repo`. It was not (#85), and that is worse than an ordinary stale sentence
# because of who reads it: an agent reading the README concludes the gate will catch it, and a
# reviewer reading the same line concludes CI already asked. Neither was true.
#
# NEEDS THE PR BODY, which no grep over the tree can see, so the inputs arrive from the environment
# and ci.yml's `lint / repo` job supplies them:
#
#   DKP_ADR_BASE_REF   the PR base sha. UNSET => this is not a pull request; skip, loudly.
#   DKP_ADR_PR_BODY    the PR body. May legitimately be EMPTY — a PR with no body is a PR that has
#                      not answered the question, so emptiness must fail rather than skip.
#
# Absence of the base ref disables the gate, which makes it fail-open in exactly one direction: a
# CI job that stopped passing the env would go quietly green. TestCI_LintRepoJob_PassesPullRequestContext
# is what holds that line, the same way TestCI_LintRepoJob_FetchesFullHistory holds MIG003's
# fetch-depth. A base ref that IS set and cannot be read is a violation, never a skip: that is the
# shallow-clone case, and it is the configuration most likely to have it.
#
# TRIGGERS — the four in the two documents:
#
#   go.mod                a NEW DIRECT requirement, compared against the base blob. A version bump
#                         or a new indirect does not fire, which is what keeps Renovate quiet.
#   deploy/Dockerfile     any change. The document says "new port, volume or process"; no grep can
#   db/schema.hcl         any change. …judge "new table or changed constraint" either.
#   internal/<pkg>/…      a file added under a top-level package that does not exist at the base.
#
# The two path triggers are deliberately BROADER than their parenthetical, and the docs now say so.
# Over-triggering costs one line in the PR body and is visible; under-triggering is invisible, which
# is the failure mode this gate exists to end. Same reasoning as WEB003 above.
#
# SATISFIED BY either half of what the documents promise: a file ADDED under docs/adr/, or an
# `adr: n/a — <reason>` line in the body. The reason is required — a bare `adr: n/a` is the box
# ticked without the thought, and harvesting the reason is the entire point.

# adr_direct_requires — the direct module paths of a go.mod on stdin. `// indirect` first, so it
# wins over both the block and the single-line spelling.
adr_direct_requires() {
    awk '
        /\/\/ indirect/                          { next }
        /^require[ \t]*\(/                       { inblock = 1; next }
        inblock && /^\)/                         { inblock = 0; next }
        inblock && NF >= 2 && $2 ~ /^v/          { print $1; next }
        /^require[ \t]+/ && NF >= 3 && $3 ~ /^v/ { print $2 }
    '
}

if [ -z "${DKP_ADR_BASE_REF:-}" ]; then
    note "skip" "[ADR001] DKP_ADR_BASE_REF is unset — no pull-request context to check against"
elif ! git rev-parse --verify --quiet "${DKP_ADR_BASE_REF}^{commit}" >/dev/null 2>&1; then
    violation ADR001 "the ADR base revision cannot be read, so the check cannot run" \
        "${DKP_ADR_BASE_REF} is not in this checkout. The lint / repo job carries fetch-depth: 0
for this reason; a shallow clone must not turn this gate into a green check."
else
    adr_base="${DKP_ADR_BASE_REF}"

    # base -> WORKING TREE, plus untracked files. In CI the tree is clean and this is exactly the
    # PR's diff; on a laptop it also sees work in progress, including an ADR that has been written
    # but not yet added. `--diff-filter=A` is what separates "added an ADR" from "touched the index".
    adr_changed=$( { git diff --name-only "$adr_base" --; git ls-files --others --exclude-standard; } 2>/dev/null | sort -u)
    adr_added=$( { git diff --name-only --diff-filter=A "$adr_base" --; git ls-files --others --exclude-standard; } 2>/dev/null | sort -u)

    adr_triggers=""

    # `[ -f go.mod ]` as well as the changed-path test: deleting go.mod entirely is a change to it,
    # and reading a file that is no longer there would abort the script rather than report a rule.
    if [ -f go.mod ] && printf '%s\n' "$adr_changed" | grep -qx 'go\.mod'; then
        adr_new_dep=$(comm -13 \
            <(git show "$adr_base:go.mod" 2>/dev/null | adr_direct_requires | sort -u) \
            <(adr_direct_requires < go.mod | sort -u) || true)
        [ -n "$adr_new_dep" ] && adr_triggers="$adr_triggers
go.mod — new direct dependency: $(printf '%s' "$adr_new_dep" | tr '\n' ' ')"
    fi

    for adr_path in deploy/Dockerfile db/schema.hcl; do
        printf '%s\n' "$adr_changed" | grep -qxF "$adr_path" &&
            adr_triggers="$adr_triggers
$adr_path — changed"
    done

    for adr_pkg in $(printf '%s\n' "$adr_added" | sed -n 's#^internal/\([^/][^/]*\)/.*#\1#p' | sort -u); do
        git cat-file -e "$adr_base:internal/$adr_pkg" 2>/dev/null ||
            adr_triggers="$adr_triggers
internal/$adr_pkg — new top-level package"
    done

    if [ -n "$adr_triggers" ]; then
        adr_new_record=$(printf '%s\n' "$adr_added" | grep -E '^docs/adr/[0-9]' || true)

        # The waiver line: `adr: n/a` plus a reason. Two whitespace-separated tokens after the
        # marker, so a separator alone ("adr: n/a —") is not a reason.
        adr_waiver=$(printf '%s\n' "${DKP_ADR_PR_BODY:-}" | awk '
            tolower($0) !~ /^[ \t]*adr:/ { next }
            {
                rest = $0
                sub(/^[ \t]*[Aa][Dd][Rr]:[ \t]*/, "", rest)
                if (tolower(rest) !~ /^n\/a/) { next }
                sub(/^[Nn]\/[Aa]/, "", rest)
                sub(/^[^a-zA-Z0-9]*/, "", rest)
                if (rest ~ /[^ \t]+[ \t]+[^ \t]/) { print "ok"; exit }
            }' || true)

        if [ -z "$adr_new_record" ] && [ -z "$adr_waiver" ]; then
            violation ADR001 \
                "this change needs an architecture decision record (docs/adr/README.md, docs/design/07 §ADRs)" \
                "triggered by:$adr_triggers

Add a file under docs/adr/ in this PR, or put a line in the PR BODY reading
  adr: n/a — <why this change does not need one>
A reason is required; the marker on its own is not one."
        fi
    fi
fi

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

# --- Supply chain: no QEMU in any workflow ----------------------------------------------------
# Multi-arch images are produced by CROSS-COMPILATION joined with `docker buildx imagetools`, never
# by an emulated build. Go cross-compiles at native speed and modernc.org/sqlite is pure Go, so the
# arm64 and arm/v7 images cost one more `go build` each — about 90 seconds for the whole matrix.
#
# The gate exists because a QEMU-emulated build runs 10-25x slower, which turns a 90-second image
# stage into 20-50 minutes, and the predictable response to that is someone deleting the arm64 leg
# "to make CI fast" — silently dropping the architecture half this audience runs on (older Raspberry
# Pis). Banning the string is what stops `docker/setup-qemu-action` or a `--platform` emulation from
# being reintroduced under wall-clock pressure. docs/design/06-cicd-and-release.md §7 and PR 7's
# acceptance criteria require this exact assertion.
#
# Comments are stripped first, exactly as every other gate here does: the committed workflows say
# "No QEMU" in prose to document the choice, and prose about a rule is not a breach of it. What
# survives stripping — a `uses: docker/setup-qemu-action@...` step, a `qemu` platform arg — is the
# real thing.
if has .github/workflows; then
    hits=$(grep -rniE 'qemu' .github/workflows 2>/dev/null | strip_comments || true)
    [ -n "$hits" ] && violation QEMU001 "QEMU in a workflow — multi-arch is cross-compiled, never emulated" "$hits"
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
# where it is unambiguous — SPEC008 in internal/specgate reads the generated OpenAPI document,
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
