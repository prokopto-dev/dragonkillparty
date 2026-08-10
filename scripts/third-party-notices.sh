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

# --- Vendored assets ---------------------------------------------------------------------------
#
# Third-party content that is NOT a Go module and therefore invisible to everything above: files
# committed into this tree, built into web/dist by Vite and embedded into the binary by internal/ui.
# `go list -deps` cannot see a font, and neither can scripts/licence-gate.sh, which reads the same
# graph. The obligation is identical all the same — Inter's SIL OFL 1.1 requires its copyright notice
# and licence text to travel with the font, and the font travels inside the binary this file is
# attached to.
#
# One entry per asset: "<label>|<licence file>|<file>[,<file>…]", all paths relative to the repo
# root. Adding a vendored asset without adding a row here is caught by test/repo/web_fonts_test.go,
# which requires every committed face to be recorded in NOTICE and in this file.
VENDORED_ASSETS=(
    "Inter 4.1 — SIL Open Font License 1.1|web/src/assets/fonts/OFL.txt|web/src/assets/fonts/Inter-Regular.woff2,web/src/assets/fonts/Inter-Medium.woff2"
)

# The count is taken once and every later expansion of the array is guarded by it: bash 3.2 — the
# shell on the laptops — treats "${arr[@]}" on an EMPTY array as an unbound variable under `set -u`,
# and an empty list is a legitimate state (nothing vendored), not a reason to abort.
asset_count="${#VENDORED_ASSETS[@]}"

# `||` rather than `&&`: under `set -e` a trailing `[ … ] && x=y` whose test FAILS returns non-zero
# as a whole statement and kills the script. The pluralisation would then be a landmine that goes off
# on the day a second asset is vendored.
assets_plural=""
[ "$asset_count" -eq 1 ] || assets_plural="s"

# NO SILENT SKIP. A missing licence file or a missing asset means the row and the tree disagree, and
# the failure mode of guessing is shipping an unattributed font — so fail here, where someone is
# running the generator, rather than in the release archive.
#
# The `read -a` split is deliberate over a `while read` fed by a pipe: bash runs the loop body of a
# pipeline in a subshell, where an `exit 1` ends the subshell and not this script.
if [ "$asset_count" -gt 0 ]; then
    for entry in "${VENDORED_ASSETS[@]}"; do
        lic_path="${entry#*|}"
        lic_path="${lic_path%%|*}"
        [ -f "$lic_path" ] || {
            echo "third-party-notices: vendored asset licence $lic_path does not exist" >&2
            exit 1
        }

        IFS=',' read -r -a asset_files <<< "${entry##*|}"
        for f in "${asset_files[@]}"; do
            [ -f "$f" ] || {
                echo "third-party-notices: vendored asset $f does not exist" >&2
                exit 1
            }
        done
    done
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
    printf 'This binary statically links the Go modules listed below, and embeds the vendored assets\n'
    printf 'listed after them. Each is reproduced with its licence text. Generated from the runtime\n'
    printf 'dependency graph (go list -deps, no -test), unioned across the release platforms, by\n'
    printf 'scripts/third-party-notices.sh. Do not hand-edit: run `make third-party-notices`.\n\n'
    printf '%d modules, %d vendored asset%s.\n' "$count" "$asset_count" "$assets_plural"

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

    if [ "$asset_count" -gt 0 ]; then
        printf '\n'
        printf -- '================================================================================\n'
        printf 'Vendored assets — embedded in the binary, not linked as Go modules\n'
        printf -- '================================================================================\n'

        for entry in "${VENDORED_ASSETS[@]}"; do
            label="${entry%%|*}"
            lic_path="${entry#*|}"
            lic_path="${lic_path%%|*}"

            printf '\n'
            printf -- '--------------------------------------------------------------------------------\n'
            printf '%s\n' "$label"
            printf -- '--------------------------------------------------------------------------------\n\n'

            IFS=',' read -r -a asset_files <<< "${entry##*|}"
            for f in "${asset_files[@]}"; do
                printf '%s\n' "$f"
            done
            printf '\n'

            cat "$lic_path"
            printf '\n'
        done
    fi
} > "$OUT"

printf '  wrote %s (%d modules, %d vendored asset%s)\n' "$OUT" "$count" "$asset_count" "$assets_plural"
