#!/usr/bin/env bash
# Assert a RUNNING dkp serves the built SPA, not the placeholder.
#
#     bash scripts/smoke-spa.sh http://127.0.0.1:8080
#
# This is the runtime half of the issue #55 gate. scripts/verify-spa-dist.sh checks what was staged
# for go:embed; this checks what a booted binary actually answers with, which is the thing a guild
# sees. scripts/smoke-local.sh runs it against the locally built image in CI's `build / image` job,
# and scripts/release-smoke.sh runs it against the published digest before any moving tag advances.
#
# WHY THE BODY AND NOT THE STATUS CODE. internal/ui falls back to index.html for every path it does
# not have, with a 200 — that is what makes client-side routing work on a page refresh. So a
# placeholder image, a missing bundle and a healthy SPA all answer 200 to everything, and only the
# BYTES distinguish them. Two requests do it: the index must reference a content-hashed bundle, and
# that bundle must come back as JavaScript rather than as the index fallback wearing its name.
#
# curl is a dependency, deliberately and not silently: `make setup` already requires it (see
# scripts/install-atlas.sh), every GitHub runner has it, and skipping the check when it is absent
# would turn the gate into decoration on exactly the machines nobody is watching.

set -euo pipefail

base="${1:?usage: smoke-spa.sh <base-url>   e.g. http://127.0.0.1:8080}"
base="${base%/}"

placeholder_marker='web UI not yet built into this binary'

die() { printf 'smoke-spa: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 ||
	die "curl is not installed — it is needed to read what the server serves"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "smoke-spa: GET ${base}/"
curl -fsS --max-time 20 -o "$work/index.html" "${base}/" ||
	die "GET ${base}/ failed — the server did not serve the SPA entry document"

if grep -qF "$placeholder_marker" "$work/index.html"; then
	die "the server is serving the COMMITTED PLACEHOLDER, not the built SPA.
  The binary embedded internal/ui/dist's placeholder because its build never ran the Vite build
  (issue #55). deploy/Dockerfile's \`web\` stage builds and stages it; scripts/build-web.sh is what
  \`make build\` runs."
fi

# The bundle the index actually loads, content-hashed (web/vite.config.ts pins assets/[name]-[hash]).
bundle=$(grep -oE 'src="[^"]*/assets/[^"]+\.js"' "$work/index.html" |
	sed -E 's/^src="(.*)"$/\1/' | head -1 || true)

[ -n "$bundle" ] ||
	die "the served index references no JavaScript bundle under /assets — it is not the Vite output:
$(sed 's/^/    /' "$work/index.html" | head -20)"

# Resolve the reference against the base. Vite emits an absolute "/assets/..." by default; a relative
# "./assets/..." or bare "assets/..." is equally valid and resolves the same way for a root-served SPA.
rel="${bundle#./}"
rel="${rel#/}"
path="/${rel}"

echo "smoke-spa: GET ${base}${path}"
code=$(curl -sS --max-time 20 -D "$work/headers" -o "$work/bundle" -w '%{http_code}' "${base}${path}") ||
	die "GET ${base}${path} failed — the bundle the index references was not served"

[ "$code" = "200" ] || die "GET ${base}${path} returned ${code}, want 200"

# The fallback trap: a bundle that is absent from the embed comes back as index.html with a 200. A
# browser would then evaluate HTML as a module and the page would die in the console, which is worse
# than a 404 because every check that looks at status codes stays green.
if grep -qiE '<!doctype html|<div id="root"' "$work/bundle"; then
	die "${path} served HTML, not JavaScript — the referenced bundle is missing from the binary and
  internal/ui fell back to index.html. The staged dist was incomplete."
fi

[ -s "$work/bundle" ] || die "${path} served an empty body"

# Hashed assets carry the one-year immutable cache header; that is the contract the hashed name buys
# and internal/ui's TestEmbed_HashedAsset_IsImmutable pins it in-process. Asserting it here proves
# the SHIPPED binary serves it, from the same request a browser makes.
grep -qi '^cache-control:.*immutable' "$work/headers" ||
	die "${path} did not carry an immutable Cache-Control header:
$(grep -i '^cache-control:' "$work/headers" || echo '    (no Cache-Control at all)')"

printf 'smoke-spa: ok — the served SPA is the real build (%s, %s bytes)\n' \
	"$path" "$(wc -c <"$work/bundle" | tr -d ' ')"
