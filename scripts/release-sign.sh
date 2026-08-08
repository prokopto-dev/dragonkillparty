#!/usr/bin/env bash
# release-sign — cosign keyless signature over the published manifest digest.
#
# Keyless: GitHub OIDC -> Fulcio (short-lived cert) -> Rekor (transparency log). No key to store or
# rotate. The certificate identity is THIS workflow file at the tag ref, which is why refactoring the
# release job into a reusable workflow would break every published `cosign verify` snippet — the
# README pins --certificate-identity-regexp to the release.yml path at refs/tags/v.
#
# Called by release.yml's `manifest` job after the digest exists. DIGEST is the manifest-list digest
# from release-manifest.sh; signing the digest (not a tag) means the signature covers exact bytes.

set -euo pipefail

IMAGE="${IMAGE:?IMAGE is required}"
DIGEST="${DIGEST:?DIGEST is required (sha256:...)}"

if ! command -v cosign >/dev/null 2>&1; then
    echo "release-sign: cosign not found — the sigstore/cosign-installer step must run first" >&2
    exit 1
fi

echo "release-sign: cosign sign (keyless) ${IMAGE}@${DIGEST}"
COSIGN_EXPERIMENTAL=1 cosign sign --yes "${IMAGE}@${DIGEST}"
echo "release-sign: signed"
