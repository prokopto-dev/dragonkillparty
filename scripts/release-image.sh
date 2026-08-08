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

# cache-from everywhere, cache-to only on main (branch-scoped caches; a PR's writes help nobody).
cache_args=(--cache-from "type=gha")
if [ "${GITHUB_REF:-}" = "refs/heads/main" ]; then
    cache_args+=(--cache-to "type=gha,mode=min")
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
    "${cache_args[@]}" \
    .

echo "release-image: pushed ${IMAGE}:${VERSION}-${ARCH}"

# HARD gate on the release path: measure the image we just pushed against the 30 MB compressed budget
# and FAIL the release if it is over. This is the enforcing counterpart to ci.yml's advisory PR step
# (which runs the same script with MODE=advise, exit 0 + `::warning::`). The matrix is fail-fast, so a
# single over-budget arch stops the release before the manifest is ever joined — a shipped image that
# blew the budget is a real regression, not a warning. image-size.sh reads the pushed per-arch tag via
# `docker manifest inspect`, so it measures the true compressed (registry) size.
MODE=enforce IMAGE="$IMAGE" VERSION="${VERSION}-${ARCH}" bash "$(dirname "$0")/image-size.sh"
