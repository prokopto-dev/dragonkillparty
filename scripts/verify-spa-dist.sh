#!/usr/bin/env bash
# Verify a staged SPA directory is the REAL Vite output, not the committed placeholder.
#
# internal/ui/dist is the go:embed target, and it carries COMMITTED PLACEHOLDERS so the package
# compiles with no JS toolchain (internal/ui/embed.go). That safety net is also a trap: a build path
# that never runs scripts/build-web.sh still compiles, still links, still boots — and serves "web UI
# not yet built into this binary" to a guild. That is issue #55: the container build ran `go build`
# directly for the whole of Phase 0, so every image shipped the placeholder.
#
# So the staging step gets a gate. This runs between "stage the SPA" and "compile the binary" — in
# deploy/Dockerfile's build stage — and fails the build rather than letting a placeholder reach an
# image. It is usable by hand too:
#
#     bash scripts/verify-spa-dist.sh [dir]      # default: internal/ui/dist
#
# THE PRIMARY CHECK IS POSITIVE, not a placeholder blocklist: index.html must reference a
# content-hashed bundle under assets/, and every asset it references must exist. A placeholder fails
# that by construction (it references no bundle at all), and so does every other way of ending up
# with a dist that cannot boot a SPA — a half-copied directory, an index pointing at a renamed hash.
# The placeholder marker is checked too, but only to make the message name the real cause.
#
# The runtime half of the pair is scripts/smoke-spa.sh, which asserts a RUNNING container serves the
# built SPA. Both are needed: this one fails the build before anything is pushed and covers the
# cross-compiled arm64 image (no runner can boot that without QEMU, which this project bans), and
# that one proves what is actually served over HTTP.

set -euo pipefail

# The placeholder's marker sentence, from internal/ui/dist/index.html. Diagnostic only — the positive
# checks below are what gate — so a reworded placeholder costs message quality, never correctness.
placeholder_marker='web UI not yet built into this binary'

dist="${1:-internal/ui/dist}"

die() { printf '\033[31m  %s\033[0m\n' "$*" >&2; exit 1; }

[ -d "$dist" ] || die "$dist does not exist — nothing was staged for go:embed"

index="$dist/index.html"
[ -f "$index" ] || die "$index is missing — the SPA has no entry document"

if grep -qF "$placeholder_marker" "$index"; then
	die "$index is the COMMITTED PLACEHOLDER, not the built SPA.
  The web build never ran, or its output never reached $dist. scripts/build-web.sh is what builds
  and stages it — that is what \`make build\` runs, and what deploy/Dockerfile's \`web\` stage runs."
fi

# Every asset index.html references. Matched from the document rather than listed from the
# filesystem on purpose: a leftover bundle nobody links to is exactly what an incremental copy leaves
# behind, and it would satisfy a "some .js exists" check while the page stayed blank.
referenced=$(grep -oE '(src|href)="[^"]*/assets/[^"]+"' "$index" | sed -E 's/^[^"]*"(.*)"$/\1/' || true)

bundles=$(printf '%s\n' "$referenced" | grep -E '\.js$' || true)
if [ -z "$bundles" ]; then
	die "$index references no JavaScript bundle under assets/, so it cannot be the Vite output.
  It references:
$(printf '%s' "${referenced:-(nothing)}" | sed 's/^/    /')"
fi

# At least one bundle must be content-hashed. The one-year immutable Cache-Control internal/ui sends
# for every asset is only safe because the name changes when the bytes do (web/vite.config.ts pins
# assets/[name]-[hash].js); a build that turned hashing off would pin browsers to a stale bundle.
if ! printf '%s\n' "$bundles" | grep -qE -- '-[A-Za-z0-9_-]{8,}\.js$'; then
	die "no referenced bundle carries a content hash:
$(printf '%s' "$bundles" | sed 's/^/    /')
  internal/ui serves assets with a one-year immutable cache header, which is only sound for hashed
  names. Check build.rollupOptions.output in web/vite.config.ts."
fi

# Every referenced asset must be on disk. A dangling reference is the failure the HTTP smoke cannot
# see cheaply: internal/ui falls back to index.html for a path it does not have, so the browser gets
# HTML where it asked for JavaScript and the page dies in the console with a 200 on every request.
missing=""
while IFS= read -r ref; do
	[ -n "$ref" ] || continue
	# References are absolute ("/assets/x.js") or relative ("./assets/x.js"); both resolve under $dist.
	rel="${ref#/}"
	rel="${rel#./}"
	[ -f "$dist/$rel" ] || missing="${missing}    ${ref}"$'\n'
done <<<"$referenced"

if [ -n "$missing" ]; then
	die "$index references assets that are not in $dist:
${missing}  A partially staged dist still serves index.html for those paths, so the page loads and
  then fails in the browser."
fi

# The placeholder bundle must not have survived beside the real output. It can: COPY merges into an
# existing directory, so staging over the placeholder rather than replacing it leaves this file in
# the tree, where the embed's `all:` directive picks it up and ships it.
if [ -e "$dist/assets/app-placeholder.js" ]; then
	die "$dist/assets/app-placeholder.js is still present — the built SPA was staged OVER the
  placeholder instead of replacing it. Remove $dist before copying the real output in."
fi

printf '  \033[32mSPA verified\033[0m in %s — %s bundle(s) referenced by index.html\n' \
	"$dist" "$(printf '%s\n' "$bundles" | grep -c .)"
