#!/usr/bin/env bash
# release-image — cross-compile and push ONE per-architecture image.
#
# Called once per arch by release.yml's `image` matrix and by edge.yml. Go cross-compiles at native
# speed on the amd64 runner (modernc.org/sqlite is pure Go, CGO off), so there is NO QEMU here or
# anywhere — QEMU001 in scripts/repo-gates.sh asserts the string appears in no workflow. Each arch is
# pushed as a distinct tag; release-manifest.sh joins them into a manifest list afterwards.
#
# Inputs (from the workflow env): IMAGE, VERSION, ARCH (amd64 | arm64 | armv7), and the build stamps.
# buildx maps ARCH to --platform; armv7 becomes linux/arm/v7.

set -euo pipefail

IMAGE="${IMAGE:?IMAGE is required}"
VERSION="${VERSION:?VERSION is required}"
ARCH="${ARCH:?ARCH is required}"

case "$ARCH" in
amd64) platform="linux/amd64" ;;
arm64) platform="linux/arm64" ;;
armv7) platform="linux/arm/v7" ;;
*)
    echo "release-image: unknown ARCH ${ARCH}" >&2
    exit 1
    ;;
esac

# The build stamps, defaulted so a manual invocation still produces a labelled image.
VERSION_STAMP="${VERSION}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
DATE="${DATE:-$(git log -1 --format=%cI 2>/dev/null || echo unknown)}"

# The layer cache. BUILDX_CACHE_FROM / BUILDX_CACHE_TO are this repository's convention rather than
# anything buildx honours by name — the Makefile's `docker` recipe reads the same pair — and the
# workflow calling this script is what sets them. Unset, as on a laptop or a manual invocation, no
# cache flag is passed and the build is exactly what it was.
#
# THIS USED TO SAY type=gha, and it never worked (issue #163). buildx authenticates to the Actions
# cache service with ACTIONS_RUNTIME_TOKEN plus the cache service URL, and GitHub injects those into
# the environment of JavaScript and Docker actions only — never into a `run:` step, which is what
# invokes this file. `docker/build-push-action` can use the backend for that reason and a shell
# invocation of the same command cannot, so the release path's cache was either inert or a hard
# `failed to configure gha cache` depending on the buildx version, and no release had been cut to
# find out which. Exposing the variables needs a third-party action in a job that holds
# `packages: write`, which is a dependency decision (AGENTS.md); ci.yml's `build / image` had already
# answered the same question with the local backend and actions/cache, so both paths now share one
# mechanism. test/repo/docker_layer_cache_test.go asserts the gha backend is named nowhere.
#
# The old `GITHUB_REF = refs/heads/main` test went with it. WHICH REF MAY WRITE is a property of the
# workflow — it is the workflow that declares the actions/cache step making a write possible at all —
# and a script re-deriving it would be a second copy of the doctrine to keep in step with the first.
#
# The array can now be EMPTY, which it never could before, so the build below expands it as
# `${cache_args[@]+"${cache_args[@]}"}`: under `set -u`, bash 3.2 — what macOS ships, and what
# scripts/budget-bundle.sh already accommodates — treats a plain "${cache_args[@]}" on an empty array
# as an unbound variable and aborts.
cache_args=()
if [ -n "${BUILDX_CACHE_FROM:-}" ]; then
    cache_args+=(--cache-from "$BUILDX_CACHE_FROM")
fi
if [ -n "${BUILDX_CACHE_TO:-}" ]; then
    cache_args+=(--cache-to "$BUILDX_CACHE_TO")
fi

echo "release-image: building ${IMAGE}:${VERSION}-${ARCH} for ${platform} (no QEMU — cross-compiled)"
docker buildx build \
    --platform "$platform" \
    --file deploy/Dockerfile \
    --build-arg "VERSION=${VERSION_STAMP}" \
    --build-arg "COMMIT=${COMMIT}" \
    --build-arg "DATE=${DATE}" \
    --provenance=false \
    --tag "${IMAGE}:${VERSION}-${ARCH}" \
    --push \
    ${cache_args[@]+"${cache_args[@]}"} \
    .

echo "release-image: pushed ${IMAGE}:${VERSION}-${ARCH}"

# HARD gate on the release path: measure the image we just pushed against the 30 MB compressed budget
# and FAIL the release if it is over. This is the enforcing counterpart to ci.yml's advisory PR step
# (which runs the same script with MODE=advise, exit 0 + `::warning::`). The matrix is fail-fast, so a
# single over-budget arch stops the release before the manifest is ever joined — a shipped image that
# blew the budget is a real regression, not a warning. image-size.sh reads the pushed per-arch tag via
# `docker manifest inspect`, so it measures the true compressed (registry) size.
MODE=enforce IMAGE="$IMAGE" VERSION="${VERSION}-${ARCH}" bash "$(dirname "$0")/image-size.sh"
