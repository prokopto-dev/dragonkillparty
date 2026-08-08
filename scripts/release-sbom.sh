#!/usr/bin/env bash
# release-sbom — produce an SPDX SBOM for the published manifest list.
#
# Written to sbom.spdx.json for the actions/attest-sbom step in release.yml, which attaches it as a
# signed attestation (never via the deprecated `cosign attach sbom` path). syft reads the pushed
# image; the SBOM covers the manifest list, so `gh attestation verify` works for any architecture a
# user pulls.
#
# Called by release.yml's `manifest` job. DIGEST is the manifest-list digest.

set -euo pipefail

IMAGE="${IMAGE:?IMAGE is required}"
DIGEST="${DIGEST:?DIGEST is required (sha256:...)}"
OUT="${SBOM_OUT:-sbom.spdx.json}"

if ! command -v syft >/dev/null 2>&1; then
    echo "release-sbom: syft not found — install anchore/syft in the workflow before this step" >&2
    exit 1
fi

echo "release-sbom: syft ${IMAGE}@${DIGEST} -> ${OUT}"
syft "${IMAGE}@${DIGEST}" -o spdx-json="${OUT}"

# A zero-length or absent SBOM would silently attest nothing.
if [ ! -s "$OUT" ]; then
    echo "release-sbom: ${OUT} is empty — syft produced no SBOM" >&2
    exit 1
fi

echo "release-sbom: wrote ${OUT}"
