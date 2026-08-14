#!/usr/bin/env bash
#
# format-go.sh — PostToolUse on Edit|Write.
#
# Formats exactly one file, the one that was just written, and only if it is Go.
# goimports first (it fixes and groups imports), then gofumpt (it is stricter than gofmt).
#
# Contract:  always exit 0 or 1 — never 2. A formatter is not a gate.
#            exit 0 = done or nothing to do · exit 1 = tool missing, message shown once.
# Budget:    ~40 ms. Nothing here compiles, vets, lints or touches the module cache.
#
# Why nothing slower lives here: a PostToolUse hook that takes ten seconds turns every edit
# into a ten-second stall and destroys the ~25 s integration loop the architecture exists to
# protect. Lint runs in `make lint`; build and vet run in `make vet`; CI runs `make check`.
#
set -euo pipefail

if [[ "${DKP_HOOKS:-on}" == "off" || "${DKP_HOOK_FORMAT_GO:-on}" == "off" ]]; then
    exit 0
fi

payload="$(cat || true)"

if command -v jq >/dev/null 2>&1; then
    JSON_TOOL=jq
elif command -v python3 >/dev/null 2>&1; then
    JSON_TOOL=python3
else
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
session="$(hook_field .session_id)"

case "$file" in
    *.go) ;;
    *) exit 0 ;;
esac

if [[ ! -f "$file" ]]; then
    exit 0
fi

# Generated trees are formatted by their generator; never rewrite them here.
case "$file" in
    */internal/store/sqlitegen/* | */internal/store/pggen/* | */vendor/* | *_generated.go | *.pb.go)
        exit 0
        ;;
esac

root="${CLAUDE_PROJECT_DIR:-}"
if [[ -z "$root" ]]; then
    root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi

# Report a missing formatter once per session, then stay quiet. A one-line nag on every
# edit for the rest of the session is how a useful hook gets deleted.
warn_once() {
    local stamp="${TMPDIR:-/tmp}/dkp-format-go.${session:-nosession}.$1.warned"
    if [[ -e "$stamp" ]]; then
        return 0
    fi
    : >"$stamp" 2>/dev/null || true
    printf '%s is not installed, so %s was not formatted. Run `make setup`. CI still checks formatting.\n' "$1" "$file" >&2
    return 1
}

missing=0

if command -v goimports >/dev/null 2>&1; then
    module="$(awk '$1 == "module" { print $2; exit }' "$root/go.mod" 2>/dev/null || true)"
    if [[ -n "$module" ]]; then
        goimports -w -local "$module" "$file" >/dev/null 2>&1 || true
    else
        goimports -w "$file" >/dev/null 2>&1 || true
    fi
else
    warn_once goimports || missing=1
fi

if command -v gofumpt >/dev/null 2>&1; then
    # gofumpt refuses to write a file it cannot parse, so a mid-edit syntax error is a
    # no-op rather than a corruption. Failures are swallowed on purpose.
    gofumpt -w "$file" >/dev/null 2>&1 || true
else
    warn_once gofumpt || missing=1
fi

# Exit 1 is a non-blocking error: the user sees the message, the session continues.
# Exit 2 would feed this back to the model as a failure, which it is not.
exit "$missing"
