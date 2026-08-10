#!/usr/bin/env bash
#
# guard-protected-paths.sh — PreToolUse on Edit|Write.
#
# Blocks writes to files that are produced by `make gen`, to migrations that have already
# shipped, and to dependency manifests. Warns (ask) on golden files and fixtures.
#
# Contract:  exit 0 = allow · exit 2 + stderr = block · stdout JSON {permissionDecision:"ask"} = warn
# Budget:    ~20 ms. Pure string matching, no network, no build tools.
# Fails open: a missing JSON parser or an unparseable payload allows the edit.
#
set -euo pipefail

if [[ "${DKP_HOOKS:-on}" == "off" || "${DKP_HOOK_GUARD_PATHS:-on}" == "off" ]]; then
	exit 0
fi

payload="$(cat || true)"

if command -v jq >/dev/null 2>&1; then
	JSON_TOOL=jq
elif command -v python3 >/dev/null 2>&1; then
	JSON_TOOL=python3
else
	printf 'guard-protected-paths.sh: neither jq nor python3 found; protected paths are UNGUARDED.\n' >&2
	exit 0
fi

hook_field() {
	case "$JSON_TOOL" in
	jq) printf '%s' "$payload" | jq -r "$1 // empty" 2>/dev/null || true ;;
	python3)
		printf '%s' "$payload" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for k in sys.argv[1].strip(".").split("."):
    d = d.get(k) if isinstance(d, dict) else None
    if d is None:
        break
sys.stdout.write(d if isinstance(d, str) else "")
' "$1" 2>/dev/null || true
		;;
	esac
}

file="$(hook_field .tool_input.file_path)"
if [[ -z "$file" ]]; then
	file="$(hook_field .tool_input.notebook_path)"
fi
if [[ -z "$file" ]]; then
	exit 0
fi

root="${CLAUDE_PROJECT_DIR:-}"
if [[ -z "$root" ]]; then
	root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi

# Path relative to the repo root, with ./ and duplicate slashes collapsed.
rel="$file"
case "$rel" in
"$root"/*) rel="${rel#"$root"/}" ;;
esac
rel="$(printf '%s' "$rel" | sed -e 's|/\./|/|g' -e 's|//*|/|g' -e 's|^\./||')"
base="${rel##*/}"

deny() {
	printf 'BLOCKED by .claude/hooks/guard-protected-paths.sh\n\n  %s\n\n%s\n' "$rel" "$1" >&2
	exit 2
}

# permissionDecision "ask" surfaces the reason and hands the decision to the human.
# The reason must not contain a literal " or newline; use \n escape sequences.
ask() {
	printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"%s"}}\n' "$1"
	exit 0
}

# ---------------------------------------------------------------------------
# 1. Migrations that have shipped. SHIPPED.lock is an append-only manifest of
#    `filename sha256` written by the release job. CI recomputes every hash.
# ---------------------------------------------------------------------------
lock="$root/db/migrations-sqlite/SHIPPED.lock"
case "$rel" in
db/migrations-sqlite/SHIPPED.lock)
	# The manifest itself. Denied for a different reason than the migrations it lists — it is not
	# Atlas output, so `make gen` will not restore a hand-edit, and a row silently dropped by hand
	# un-freezes a migration that has already run on somebody's database.
	deny "SHIPPED.lock is an append-only record, not a file you edit. Rows are appended by
the release seal and never rewritten or removed.

  seal it:    make shipped-lock-seal      (in the Release PR)
  check it:   make lint-repo              (MIG003)"
	;;
db/migrations-*/*)
	if [[ -f "$lock" ]] && awk -v b="$base" '$1 == b { found = 1 } END { exit !found }' "$lock"; then
		deny "This migration is listed in db/migrations-sqlite/SHIPPED.lock — it shipped in a tagged
release and its hash is verified by CI. Editing it makes every existing install
diverge from every new one.

Write a NEW migration instead:  make migration NAME=<snake_case>"
	fi
	;;
esac

# ---------------------------------------------------------------------------
# 2. Generated files. Change the source, run make gen, commit the diff.
# ---------------------------------------------------------------------------
gen_source=""
case "$rel" in
openapi/openapi.json)
	gen_source="the Go types Huma compiles against, in internal/api/" ;;
internal/store/sqlitegen/* | internal/store/pggen/*)
	gen_source="db/queries/*.sql and db/schema.hcl (sqlc)" ;;
web/src/api/*)
	gen_source="openapi/openapi.json (openapi-typescript)" ;;
clients/ts/* | clients/python/*)
	gen_source="openapi/openapi.json (SDK generators)" ;;
web/dist/*)
	gen_source="web/src/ (Vite build output)" ;;
db/migrations-*/*)
	gen_source="db/schema.hcl (Atlas generates the migration; goose applies it)" ;;
esac

if [[ -n "$gen_source" ]]; then
	deny "This file is GENERATED. Hand-edits are erased by the next \`make gen\` and CI fails on
the drift.

  source:  $gen_source
  fix:     edit the source, then run \`make gen\`, then commit the diff

For a new migration use \`make migration NAME=<snake_case>\`."
fi

# ---------------------------------------------------------------------------
# 3. Dependency manifests. AGENTS.md: propose the dependency, a human decides.
# ---------------------------------------------------------------------------
case "$rel" in
go.mod | go.sum | web/pnpm-lock.yaml | web/package.json | pnpm-lock.yaml)
	deny "Dependency changes are a human decision, not an agent decision.

Propose it instead: name the module, why it is needed, what it replaces, and its
licence. A GPL/AGPL runtime dependency fails the licence gate outright (this
project is Apache-2.0). See AGENTS.md > Do not."
	;;
esac

# ---------------------------------------------------------------------------
# 4. Expected outputs. Editable, but never as a way to make a failure go away.
# ---------------------------------------------------------------------------
case "$rel" in
test/golden/* | test/fixtures/*)
	ask "STOP AND READ. $rel is an EXPECTED OUTPUT and is CODEOWNERS-protected.\n\nRewriting a golden file or a fixture to match new behaviour inverts the point of\nthe test: the assertion stops describing what is correct and starts describing\nwhat the code currently does.\n\nApprove this ONLY if the expected output genuinely changed and you can say why in\nthe commit message. If you are here because a test went red, fix the code.\n\nAGENTS.md: \\\"Do not rewrite anything under test/golden/ or test/fixtures/ to go green.\\\""
	;;
esac

exit 0
