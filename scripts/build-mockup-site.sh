#!/usr/bin/env bash
# Assemble the publishable mockup site from docs/design/mockups/.
#
# The vendored .dc.html files stay byte-exact — they are the design reference, and a diff against a
# fresh export has to be readable. Every adjustment needed to publish them happens in
# scripts/dc-publish.py, on copies. This script is the guards plus the file shuffling.
#
# Usage: scripts/build-mockup-site.sh [output-dir]     (default: _site)
set -euo pipefail

REPO_ROOT="${DKP_REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
SRC="$REPO_ROOT/docs/design/mockups"
OUT="${1:-$REPO_ROOT/_site}"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }

echo "mockup site → $OUT"

# ── [MOCK001] the runtime has no expression evaluator ────────────────────────────────────────────
#
# harness/mockup-runtime.js resolves a binding as a dotted path or a literal and nothing else. That
# is safe to do without eval precisely because no mockup needs more. If a refreshed export ever
# introduces `{{ a || b }}` or `{{ f(x) }}`, the binding would silently render empty — so fail here
# instead, loudly, with the offending expressions named.
# Extracted in Python, not grep: a character class of "not }" cannot see `{{ a } b }}`, and a
# binding the gate cannot see is exactly the one that would slip through and render empty.
bad_bindings="$(python3 - "$SRC" <<'PY'
import pathlib, re, sys

PATH_OR_LITERAL = re.compile(
    r"^(?:[A-Za-z_$][A-Za-z0-9_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*)*"
    r"|true|false|null|-?[0-9]+(?:\.[0-9]+)?)$"
)
bad = set()
for f in sorted(pathlib.Path(sys.argv[1]).glob("*.dc.html")):
    for body in re.findall(r"\{\{(.*?)\}\}", f.read_text(encoding="utf-8"), re.S):
        expr = body.strip()
        if not PATH_OR_LITERAL.match(expr):
            bad.add(expr)
for expr in sorted(bad):
    print(expr)
PY
)"
if [ -n "$bad_bindings" ]; then
  red "[MOCK001] binding(s) the runtime cannot resolve without an expression evaluator:"
  printf '  %s\n' "$bad_bindings"
  red "Compute it in renderVals(), or extend docs/design/mockups/harness/mockup-runtime.js."
  red "Do not let a binding render empty."
  exit 1
fi
green "  [MOCK001] all bindings are plain paths or literals"

# ── [MOCK002] no third-party runtime crept back in ───────────────────────────────────────────────
# NOTICE states no third-party source is vendored. The design tool's runtime is unlicensed, so its
# filenames are refused outright rather than trusted to stay deleted.
for forbidden in support.js ios-frame.jsx _ds_bundle.js; do
  if find "$SRC" -name "$forbidden" -print -quit | grep -q .; then
    red "[MOCK002] $forbidden is the design tool's unlicensed runtime and must not be vendored."
    red "See NOTICE and docs/design/mockups/README.md. Use harness/mockup-runtime.js instead."
    exit 1
  fi
done
green "  [MOCK002] no third-party runtime vendored"

# ── Assemble ─────────────────────────────────────────────────────────────────────────────────────
rm -rf "$OUT"
mkdir -p "$OUT/nocturne"

cp "$SRC/nocturne/styles.css"       "$OUT/nocturne/styles.css"
cp "$SRC/harness/mockup-runtime.js" "$OUT/mockup-runtime.js"
cp "$SRC/harness/ios-frame.js"      "$OUT/ios-frame.js"
cp "$SRC/index.html"                "$OUT/index.html"

title_for() {
  case "$1" in
    admin-console.dc.html)  echo "Admin console" ;;
    guild-portal.dc.html)   echo "Guild portal" ;;
    my-characters.dc.html)  echo "My characters" ;;
    public-site.dc.html)    echo "Public site" ;;
    first-run.dc.html)      echo "First run" ;;
    *)                      echo "Mockup" ;;
  esac
}

count=0
for src in "$SRC"/*.dc.html; do
  name="$(basename "$src")"
  dest="$OUT/$name"

  python3 "$REPO_ROOT/scripts/dc-publish.py" "$src" "$dest" "$(title_for "$name")"

  # [MOCK003] the rewrites must leave nothing pointing at the design tool's layout.
  if grep -q '_ds/\|support\.js\|text/x-dc' "$dest"; then
    red "[MOCK003] $name still references the design tool's runtime after rewriting:"
    grep -n '_ds/\|support\.js\|text/x-dc' "$dest" | head -5
    exit 1
  fi
  count=$((count + 1))
done

green "  [MOCK003] $count surfaces rewritten, no stale runtime references"
green "mockup site ready: $OUT"
