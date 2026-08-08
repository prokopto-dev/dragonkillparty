#!/usr/bin/env bash
# Generate THIRD_PARTY_NOTICES.txt from the RUNTIME dependency graph.
#
# NOTICE promises this file, and .goreleaser.yaml attaches it to every release archive and Linux
# package. It is the attribution the Apache-2.0, MIT, BSD and MPL-2.0 dependencies require us to ship
# with the binary — the same graph the licence gate (scripts/licence-gate.sh) polices for forbidden
# licences. This file is the positive side of that: for every module that IS allowed, reproduce its
# licence text.
#
# SCOPE: runtime only, unioned across the release platforms. `go list -deps ./cmd/dkp` without -test,
# because a module reachable only from a dependency's test binary is not linked into dkp and owes no
# attribution. The union across GOOS/GOARCH matters: golang.org/x/sys and modernc.org/libc pull in
# different files (and occasionally different modules) per platform, and the shipped darwin and
# windows binaries must carry the notices for what THEY link.
#
# HERMETIC and STDLIB-ONLY: needs the Go toolchain and the module cache, nothing else. No
# go-licenses, no network — adding a tool to generate an attribution file is not worth a dependency
# proposal, and the module cache already holds every LICENSE file we need.
#
# Usage: scripts/third-party-notices.sh [output-path]   (default: THIRD_PARTY_NOTICES.txt)

set -euo pipefail
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

OUT="${1:-THIRD_PARTY_NOTICES.txt}"

# The platforms the release actually ships. Keep in step with .goreleaser.yaml's build matrix and
# with scripts/licence-gate.sh's platform union — a platform in one and not the others is a hole.
PLATFORMS=(
    "linux/amd64" "linux/arm64" "linux/arm"
    "darwin/amd64" "darwin/arm64"
    "windows/amd64" "windows/arm64"
)

MODULE="$(go list -m)"

# Collect the union of runtime modules across every platform, as "path\tversion\tdir" lines.
mods_file="$(mktemp)"
trap 'rm -f "$mods_file"' EXIT

for platform in "${PLATFORMS[@]}"; do
    GOOS="${platform%/*}" GOARCH="${platform#*/}" \
        go list -deps -json ./cmd/dkp 2>/dev/null
done | MODULE="$MODULE" python3 -c '
import sys, os, json
dec = json.JSONDecoder()
data = sys.stdin.read()
mods = {}
i, n = 0, len(data)
self_mod = os.environ["MODULE"]
while i < n:
    while i < n and data[i] in " \t\r\n":
        i += 1
    if i >= n:
        break
    obj, i = dec.raw_decode(data, i)
    m = obj.get("Module")
    if m and m.get("Path") != self_mod:
        mods[m["Path"]] = (m.get("Version", ""), m.get("Dir", ""))
for p in sorted(mods):
    v, d = mods[p]
    print(f"{p}\t{v}\t{d}")
' > "$mods_file"

count="$(wc -l < "$mods_file" | tr -d ' ')"
if [ "$count" -eq 0 ]; then
    echo "third-party-notices: go list produced no runtime modules — refusing to write an empty file" >&2
    exit 1
fi

# find_license <module-dir> — print the path of the licence file, preferring a top-level LICENSE.
find_license() {
    local dir="$1" f
    for f in LICENSE LICENSE.md LICENSE.txt LICENCE COPYING LICENSE-MIT LICENSE-APACHE; do
        [ -f "$dir/$f" ] && { printf '%s\n' "$dir/$f"; return 0; }
    done
    # Some modules keep it one level down (e.g. a v2 subdir). Take the shallowest match.
    find "$dir" -maxdepth 2 -type f \
        \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' \) 2>/dev/null \
        | awk '{ print length, $0 }' | sort -n | head -1 | cut -d' ' -f2-
}

{
    printf 'Third-party notices for Dragon Kill Party\n'
    printf '=========================================\n\n'
    printf 'This binary statically links the Go modules listed below. Each is reproduced with its\n'
    printf 'licence text. Generated from the runtime dependency graph (go list -deps, no -test),\n'
    printf 'unioned across the release platforms, by scripts/third-party-notices.sh. Do not hand-edit:\n'
    printf 'run `make third-party-notices`.\n\n'
    printf '%d modules.\n' "$count"

    while IFS=$'\t' read -r path version dir; do
        printf '\n'
        printf -- '--------------------------------------------------------------------------------\n'
        printf '%s %s\n' "$path" "$version"
        printf -- '--------------------------------------------------------------------------------\n\n'
        lic="$(find_license "$dir" || true)"
        if [ -n "$lic" ] && [ -f "$lic" ]; then
            cat "$lic"
        else
            printf '(no licence file found in the module; see %s upstream)\n' "$path"
        fi
        printf '\n'
    done < "$mods_file"
} > "$OUT"

printf '  wrote %s (%d modules)\n' "$OUT" "$count"
