#!/usr/bin/env bash
# Assert every command in AGENTS.md's canonical command table is a real Makefile target.
#
# Direction matters and is deliberate: every ROW must resolve to a TARGET. The reverse is not
# checked — the Makefile also carries internal helpers (fmt, build, the release-* and fixture-*
# targets CI calls) that would only be noise in a file agents read every session. AGENTS.md states
# this explicitly.
#
# This is the mechanism that keeps AGENTS.md from rotting: the file agents trust most is the file
# most likely to drift, because nothing executes it.

set -euo pipefail
cd "$(dirname "$0")/.."

[ -f AGENTS.md ] || { echo "AGENTS.md not found"; exit 1; }
[ -f Makefile ]  || { echo "Makefile not found";  exit 1; }

# Rows look like: | dev server (Go :8080 + Vite :5173) | `make dev` | — |
# No mapfile: macOS ships bash 3.2, and contributors run this locally.
commands=()
while IFS= read -r c; do
    [ -n "$c" ] && commands+=("$c")
done < <(grep -oE '`make [a-z][a-z0-9-]*' AGENTS.md | sed 's/`make //' | sort -u)

if [ "${#commands[@]}" -eq 0 ]; then
    echo "no 'make <target>' commands found in AGENTS.md — has the table format changed?"
    exit 1
fi

missing=()
for c in "${commands[@]}"; do
    # A target is real if make knows it. -n dry-runs; we only care that it resolves.
    if ! make -n "$c" >/dev/null 2>&1; then
        # Distinguish "no such target" from "target exists but its recipe would fail".
        if ! grep -qE "^${c}:" Makefile; then
            missing+=("$c")
        fi
    fi
done

if [ "${#missing[@]}" -ne 0 ]; then
    printf '\033[31mFAIL\033[0m AGENTS.md names %d command(s) with no Makefile target:\n' "${#missing[@]}"
    printf '  make %s\n' "${missing[@]}"
    printf '\nAdd the target and the AGENTS.md row in the same change — never invent one.\n'
    exit 1
fi

printf '  \033[32m%d canonical commands, all real targets\033[0m\n' "${#commands[@]}"
