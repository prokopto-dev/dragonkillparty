#!/usr/bin/env bash
# Build the SPA and stage it for go:embed.
#
# `make build` calls this before `go build`. It runs the Vite build (web/dist), then copies the
# output into internal/ui/dist, which is the go:embed target — //go:embed cannot reach upward out of
# its own directory, so the built assets have to be staged beside embed.go.
#
# The committed placeholders under internal/ui/dist (index.html, assets/app-placeholder.js) keep the
# package buildable with no JS toolchain; this script REPLACES them with the real output. The real
# output is gitignored — but the `rm -rf` below deletes the TRACKED placeholders to make room for it,
# so a build DOES leave the tree dirty (`git status` shows the placeholders deleted). Restore them
# with `git checkout -- internal/ui/dist` when you are done.
#
# This used to claim "a build never dirties the tree", which was false and actively misleading: the
# next `make test` failed in internal/ui, and before the fix it failed with a message about a wrong
# cache header rather than about a missing file. internal/ui/embed_test.go now DISCOVERS an embedded
# asset instead of naming app-placeholder.js, so the sequence AGENTS.md prescribes — `make build`
# then `make test` — no longer produces a misleading failure.
#
# Fails loudly when pnpm is missing rather than skipping: a `make build` that silently shipped the
# placeholder SPA would produce a binary that serves "web UI not yet built" to a guild. The one
# sanctioned skip is the pre-scaffold state, where web/package.json does not exist at all.
#
# DKP_WEB_STAGE=0 builds web/dist and stops before staging. `make budget-bundle` is the only caller
# that sets it, and the reason is the paragraph above: staging deletes the tracked placeholders, and
# the bundle budget became a prerequisite of `make check` (issue #166), so without this every `make
# check` would leave the tree dirty. The budget measures web/dist, so staging buys it nothing.

set -euo pipefail

cd "$(dirname "$0")/.."

die() { printf '\033[31m  %s\033[0m\n' "$*" >&2; exit 1; }

dist=internal/ui/dist

# Pre-scaffold: no web project yet. Nothing to build; the placeholder stays and the binary compiles.
# This branch disappears the moment web/package.json is committed.
if [ ! -f web/package.json ]; then
	printf '  web/ is not scaffolded — internal/ui serves the placeholder\n'
	exit 0
fi

command -v pnpm >/dev/null 2>&1 || die "pnpm is not installed — see make setup (Node + pnpm)"

# --frozen-lockfile: the lockfile is the contract; a build must never silently resolve a different
# tree than CI did. --ignore-scripts: no dependency lifecycle script runs during a build, which is
# the same posture CI installs with.
printf '  installing web dependencies (frozen, no scripts)\n'
(cd web && pnpm install --frozen-lockfile --ignore-scripts)

printf '  building the SPA (vite)\n'
(cd web && pnpm run build)

[ -d web/dist ] || die "vite produced no web/dist — the build did not emit anything"
[ -f web/dist/index.html ] || die "web/dist has no index.html — the SPA entry point is missing"

# No source map ships (security F3). A .map hands an attacker the unminified source and comments, and
# the embed's `all:` directive would carry it into the binary. Vite's default is sourcemap: false, so
# a .map here means someone turned it on — fail before staging rather than embedding it. The runtime
# lock is internal/ui's TestEmbed_NoSourceMapShips, which walks the embedded tree.
maps=$(find web/dist -type f -name '*.map' 2>/dev/null || true)
if [ -n "$maps" ]; then
	die "vite emitted source maps — a .map leaks the unminified SPA source and must not ship:
$(printf '%s\n' "$maps" | sed 's/^/  /')
Set build.sourcemap: false in web/vite.config.ts."
fi

# The measure-only caller stops here: web/dist is built and verified, nothing tracked has moved. See
# the header for why `make budget-bundle` wants that.
if [ "${DKP_WEB_STAGE:-1}" = "0" ]; then
	printf '  \033[32mSPA built\033[0m (DKP_WEB_STAGE=0 — not staged for go:embed)\n'
	exit 0
fi

# Stage into the embed directory. Clear the previous real output first so a renamed hashed asset
# does not leave a stale copy behind, but KEEP the tree so the committed placeholders' directory
# structure is not disturbed on a partial failure.
printf '  staging web/dist -> %s\n' "$dist"
rm -rf "$dist"
mkdir -p "$dist"
cp -R web/dist/. "$dist/"

printf '  \033[32mSPA built and staged for go:embed\033[0m\n'
