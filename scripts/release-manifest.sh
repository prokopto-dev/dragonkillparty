#!/usr/bin/env bash
# release-manifest — join the per-arch images into ONE manifest list and emit its immutable digest.
#
# `docker buildx imagetools create` writes a manifest list and executes NO instruction of any target
# architecture — that is the whole reason multi-arch costs no emulation. Called by release.yml's
# `manifest` job (all three arches) and by edge.yml (amd64 + arm64).
#
# It writes ONLY the immutable tag (:VERSION). The moving tags (:1, :1.x, :latest) are advanced later,
# by release-promote.sh, and ONLY after smoke passes — so a broken build leaves :1 on the previous
# digest. Emits `digest=sha256:...` to GITHUB_OUTPUT for the smoke and promote jobs to pin.

set -euo pipefail

IMAGE="${IMAGE:?IMAGE is required}"
VERSION="${VERSION:?VERSION is required}"

# Which arches were built. edge.yml sets ARCHES to a subset; release.yml builds all three.
ARCHES="${ARCHES:-amd64 arm64 armv7}"

sources=()
for arch in $ARCHES; do
    sources+=("${IMAGE}:${VERSION}-${arch}")
done

echo "release-manifest: joining ${sources[*]} into ${IMAGE}:${VERSION}"
docker buildx imagetools create --tag "${IMAGE}:${VERSION}" "${sources[@]}"

# The edge channel also gets a sha-addressable tag so a specific edge build is pinnable.
if [ -n "${SHA7:-}" ]; then
    short="${SHA7:0:7}"
    docker buildx imagetools create --tag "${IMAGE}:${VERSION}-${short}" "${sources[@]}"
    echo "release-manifest: also tagged ${IMAGE}:${VERSION}-${short}"
fi

# The immutable digest of the manifest list. Everything downstream pins THIS, never a tag.
digest="$(docker buildx imagetools inspect "${IMAGE}:${VERSION}" \
    --format '{{json .Manifest.Digest}}' | tr -d '"')"

if [ -z "$digest" ]; then
    echo "release-manifest: could not resolve the manifest digest" >&2
    exit 1
fi

echo "release-manifest: ${IMAGE}@${digest}"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
    echo "digest=${digest}" >>"$GITHUB_OUTPUT"
fi
