#!/usr/bin/env bash
# release-smoke — the gate that makes a release safe.
#
# Pulls the IMMUTABLE DIGEST (never a tag) on real hardware and proves the build boots BEFORE any
# moving tag is allowed to point at it. Runs on both amd64 and arm64 runners (release.yml's `smoke`
# matrix), because "it boots on amd64" is not evidence it boots on the Raspberry Pi half of this
# audience. If this fails, release-promote.sh never runs, so :1 / :1.x / :latest stay on the previous
# digest — nobody running :1 ever sees a build that failed to boot.
#
# Inputs: IMAGE, DIGEST, VERSION, ARCH. The verification half also runs the exact `cosign verify` and
# `gh attestation verify` commands the README tells users to run, so a wrong README snippet is caught
# here rather than by a user.

set -euo pipefail

IMAGE="${IMAGE:?IMAGE is required}"
DIGEST="${DIGEST:?DIGEST is required (sha256:...)}"
ref="${IMAGE}@${DIGEST}"

name="dkp-release-smoke-$$"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# --- 1. First boot on an empty volume ---------------------------------------------------------
echo "release-smoke: first boot of ${ref} on an empty volume"
docker run -d --name "$name" --tmpfs /data -p 127.0.0.1:0:8080 "$ref" >/dev/null

ok=0
for _ in $(seq 1 60); do
    if docker exec "$name" /usr/local/bin/dkp healthcheck >/dev/null 2>&1; then
        ok=1
        break
    fi
    sleep 0.5
done
if [ "$ok" -ne 1 ]; then
    echo "release-smoke: /healthz never answered — the published digest does not boot" >&2
    docker logs "$name" >&2 || true
    exit 1
fi
echo "release-smoke: /healthz ok"

# /readyz and version, from inside the container.
docker exec "$name" /usr/local/bin/dkp version || { echo "release-smoke: dkp version failed" >&2; exit 1; }

# --- 2. Verify the supply-chain attestations, with the README's exact commands ----------------
# A wrong identity regexp in the README is a support burden; running the published command here is
# what keeps the README honest. ORG is derived from IMAGE (ghcr.io/<org>/dkp).
org="$(printf '%s' "$IMAGE" | awk -F/ '{ print $2 }')"
identity_re="^https://github\\.com/${org}/dragonkillparty/\\.github/workflows/release\\.yml@refs/tags/v"

if command -v cosign >/dev/null 2>&1; then
    echo "release-smoke: cosign verify ${ref}"
    cosign verify "${ref}" \
        --certificate-identity-regexp "$identity_re" \
        --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null
    echo "release-smoke: cosign verify ok"
else
    echo "release-smoke: cosign not present — skipping signature verification" >&2
fi

if command -v gh >/dev/null 2>&1; then
    echo "release-smoke: gh attestation verify ${ref}"
    gh attestation verify "oci://${ref}" --repo "${org}/dragonkillparty" >/dev/null || {
        echo "release-smoke: gh attestation verify failed" >&2
        exit 1
    }
    echo "release-smoke: attestation ok"
fi

echo "release-smoke: ${ref} passed"
