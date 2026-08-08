#!/usr/bin/env bash
# Build the SPA and stage it for go:embed.
#
# `make build` calls this before `go build`. It runs the Vite build (web/dist), then copies the
# output into internal/ui/dist, which is the go:embed target — //go:embed cannot reach upward out of
# its own directory, so the built assets have to be staged beside embed.go.
#
# The committed placeholders under internal/ui/dist (index.html, assets/app-placeholder.js) keep the
# package buildable with no JS toolchain; this script REPLACES them with the real output. That output
# is gitignored, so a build never dirties the tree.
#
# Fails loudly when pnpm is missing rather than skipping: a `make build` that silently shipped the
# placeholder SPA would produce a binary that serves "web UI not yet built" to a guild. The one
# sanctioned skip is the pre-scaffold state, where web/package.json does not exist at all.

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

# Stage into the embed directory. Clear the previous real output first so a renamed hashed asset
# does not leave a stale copy behind, but KEEP the tree so the committed placeholders' directory
# structure is not disturbed on a partial failure.
printf '  staging web/dist -> %s\n' "$dist"
rm -rf "$dist"
mkdir -p "$dist"
cp -R web/dist/. "$dist/"

printf '  \033[32mSPA built and staged for go:embed\033[0m\n'
