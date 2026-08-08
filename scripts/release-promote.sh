#!/usr/bin/env bash
# release-promote — advance the moving tags to the ALREADY-SMOKED digest.
#
# This is the ONLY place :1.x, :1 and :latest are written, and release.yml only reaches it after the
# `smoke` job passed. It re-tags the exact digest smoke verified — never a rebuild — with imagetools,
# which writes manifest lists and runs nothing. :1 is the tag documentation pins everywhere; :latest
# is discouraged in the README because it will one day cross a major and change the deploy contract.
#
# Inputs: IMAGE, VERSION (e.g. 1.5.0), DIGEST (the smoked manifest digest). MAJOR/MINOR are derived
# from VERSION so a patch release advances :1.5 and :1 to itself.

set -euo pipefail

IMAGE="${IMAGE:?IMAGE is required}"
VERSION="${VERSION:?VERSION is required}"
DIGEST="${DIGEST:?DIGEST is required (sha256:...)}"

# Only strict X.Y.Z advances the stable moving tags. A pre-release (1.5.0-rc.1) is handled by
# release-promote-rc.sh, which advances :next only.
if ! printf '%s' "$VERSION" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "release-promote: ${VERSION} is not a stable X.Y.Z — refusing to move stable tags" >&2
    exit 1
fi

major="${VERSION%%.*}"
minor="${VERSION%.*}" # X.Y

src="${IMAGE}@${DIGEST}"

for tag in "${minor}" "${major}" "latest"; do
    echo "release-promote: ${IMAGE}:${tag} -> ${DIGEST}"
    docker buildx imagetools create --tag "${IMAGE}:${tag}" "${src}"
done

echo "release-promote: moving tags advanced to ${DIGEST}"
