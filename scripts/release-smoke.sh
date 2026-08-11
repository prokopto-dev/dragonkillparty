#!/usr/bin/env bash
# release-smoke — the gate that makes a release safe.
#
# Pulls the IMMUTABLE DIGEST (never a tag) on real hardware and proves the build boots BEFORE any
# moving tag is allowed to point at it. Runs on both amd64 and arm64 runners (release.yml's `smoke`
# matrix), because "it boots on amd64" is not evidence it boots on the Raspberry Pi half of this
# audience. If this fails, release-promote.sh never runs, so :1 / :1.x / :latest stay on the previous
# digest — nobody running :1 ever sees a build that failed to boot.
#
# Inputs: IMAGE, DIGEST, VERSION, ARCH, and VERIFY_SUPPLY_CHAIN.
#
# VERIFY_SUPPLY_CHAIN=1 opts the run into section 2, which runs the exact `cosign verify` and
# `gh attestation verify` commands the README tells users to run, so a wrong README snippet is caught
# here rather than by a user. It is OFF by default because only a tagged release has anything to
# verify: the signature and the attestations are produced by release.yml's `manifest` job, and the
# edge channel publishes neither. `curl` and `docker` are the dependencies of section 1.

set -euo pipefail

IMAGE="${IMAGE:?IMAGE is required}"
DIGEST="${DIGEST:?DIGEST is required (sha256:...)}"
ref="${IMAGE}@${DIGEST}"

# curl is checked here rather than where it is first used, because both users are below: the /readyz
# probe and scripts/smoke-spa.sh. Failing loud beats skipping — a check that quietly vanishes on the
# machine without the dependency is a claim of release coverage nobody has. Every GitHub runner has
# curl, so this fires on a misconfigured self-hosted one, which is exactly when it should.
if ! command -v curl >/dev/null 2>&1; then
    echo "release-smoke: curl not found — it is needed to read what the published image serves" >&2
    exit 1
fi

name="dkp-release-smoke-$$"
work="$(mktemp -d)"
cleanup() {
    docker rm -f "$name" >/dev/null 2>&1 || true
    rm -rf "$work"
}
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

# The published port, discovered BEFORE the checks that need it. It used to be derived further down,
# immediately before scripts/smoke-spa.sh; the /readyz probe below is the second user of it and the
# earlier of the two, so the discovery moves up rather than being duplicated.
port="$(docker port "$name" 8080/tcp | head -1 | sed 's/.*://')"
[ -n "$port" ] || { echo "release-smoke: could not determine the published port" >&2; exit 1; }
base="http://127.0.0.1:${port}"

# /readyz must answer, and answer AS ITSELF. This is the probe the script's comment promised and its
# code never made (issue #99): a `# /readyz and version` comment sat above a `dkp version` call and
# nothing else, so the only health-adjacent check in the file was the /healthz boot poll above.
# scripts/smoke-local.sh has the worked version of this against a locally built image; these are the
# same three checks against the PUBLISHED digest, which is the one that gates the moving tags.
#
# Not 200 specifically — a first boot may legitimately still be applying migrations, which is a 503
# with state "pending" (internal/api/ready.go) — but a readiness report, from the readiness handler:
#
#   1. curl connected at all.
#   2. the status is 200 or 503. Those are the only two handleReadyz produces; anything else is a
#      different handler answering.
#   3. the body carries a "state". Load-bearing, not belt-and-braces: when Config.Readiness is nil
#      the route is never registered, and internal/ui's SPA catch-all then answers /readyz with
#      index.html and a 200. A status-code check alone calls that ready.
#
# From the HOST, at the published port — inside the container's namespace nothing listens on it.
readyz_body="$work/readyz.json"

echo "release-smoke: GET ${base}/readyz"
readyz_code="$(curl -sS --max-time 20 -o "$readyz_body" -w '%{http_code}' "${base}/readyz")" || {
    echo "release-smoke: GET ${base}/readyz did not answer — the digest boots but /readyz is not served" >&2
    docker logs "$name" >&2 || true
    exit 1
}

# `|| true` on the body dumps only, and only to keep a SIGPIPE from `head` from replacing the exit
# code of a failure that is already being reported. Never on the checks themselves.
case "$readyz_code" in
    200 | 503) ;;
    *)
        echo "release-smoke: GET ${base}/readyz returned ${readyz_code}; handleReadyz answers 200 or 503 only" >&2
        sed 's/^/    /' "$readyz_body" | head -20 >&2 || true
        exit 1
        ;;
esac

readyz_state="$(sed -n 's/.*"state"[[:space:]]*:[[:space:]]*"\([a-z]*\)".*/\1/p' "$readyz_body" | head -1)"
if [ -z "$readyz_state" ]; then
    echo "release-smoke: ${base}/readyz answered ${readyz_code} with no readiness report in the body." >&2
    echo "  Nothing registered /readyz, so something else answered for it — the SPA catch-all serves" >&2
    echo "  index.html with a 200 for every path it does not have. What came back:" >&2
    sed 's/^/    /' "$readyz_body" | head -20 >&2 || true
    exit 1
fi
echo "release-smoke: /readyz ${readyz_code} — state ${readyz_state}"

# Version, from inside the container.
docker exec "$name" /usr/local/bin/dkp version || { echo "release-smoke: dkp version failed" >&2; exit 1; }

# The published digest must serve the BUILT SPA, not internal/ui's committed placeholder. "It boots"
# was the whole of this gate until issue #55, and a placeholder image boots flawlessly — it just
# serves "web UI not yet built into this binary" to every officer who opens it. Checked here, on the
# digest, before any moving tag is allowed to point at it.
bash "$(dirname "$0")/smoke-spa.sh" "$base"

# --- 2. Verify the supply-chain attestations, with the README's exact commands ----------------
# OPT-IN, and deliberately not gated on tool presence (issue #107). `command -v cosign` looked like a
# portable skip and was really a coin toss on the runner image: cosign is not preinstalled, so it
# skipped, while `gh` IS preinstalled on GitHub-hosted ubuntu runners — so the edge channel, which
# has no signature and no attestation to verify and passes no token, ran `gh attestation verify`
# anyway and failed there. Which channel signs its images is a property of the WORKFLOW, so the
# workflow says so: release.yml's smoke job sets VERIFY_SUPPLY_CHAIN=1 and installs cosign, edge.yml
# leaves it unset.
if [ "${VERIFY_SUPPLY_CHAIN:-0}" = "1" ]; then
    # Past here the caller has ASKED for verification, so a missing tool is a failure and never a
    # skip. A warn-and-continue branch would turn the release train's one supply-chain gate into a
    # log line on any runner that happens not to carry the tool — the shape that hid issue #84.
    #
    # A wrong identity regexp in the README is a support burden; running the published command here
    # is what keeps the README honest. ORG comes from IMAGE (ghcr.io/<org>/dragonkillparty).
    org="$(printf '%s' "$IMAGE" | awk -F/ '{ print $2 }')"
    identity_re="^https://github\\.com/${org}/dragonkillparty/\\.github/workflows/release\\.yml@refs/tags/v"

    if ! command -v cosign >/dev/null 2>&1; then
        echo "release-smoke: VERIFY_SUPPLY_CHAIN=1 but cosign is not installed." >&2
        echo "  cosign ships on no GitHub runner image, so the job asking for verification installs" >&2
        echo "  it (sigstore/cosign-installer, as release.yml's smoke job does)." >&2
        exit 1
    fi

    echo "release-smoke: cosign verify ${ref}"
    cosign verify "${ref}" \
        --certificate-identity-regexp "$identity_re" \
        --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null
    echo "release-smoke: cosign verify ok"

    if ! command -v gh >/dev/null 2>&1; then
        echo "release-smoke: VERIFY_SUPPLY_CHAIN=1 but gh is not installed." >&2
        exit 1
    fi
    if [ -z "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ]; then
        echo "release-smoke: VERIFY_SUPPLY_CHAIN=1 but no GH_TOKEN is set." >&2
        echo "  gh attestation verify reads the attestation through the GitHub API; inside Actions" >&2
        echo "  the job must pass GH_TOKEN: \${{ github.token }} and hold attestations: read." >&2
        exit 1
    fi

    echo "release-smoke: gh attestation verify ${ref}"
    gh attestation verify "oci://${ref}" --repo "${org}/dragonkillparty" >/dev/null || {
        echo "release-smoke: gh attestation verify failed" >&2
        exit 1
    }
    echo "release-smoke: attestation ok"
else
    echo "release-smoke: supply-chain verification skipped — VERIFY_SUPPLY_CHAIN is not 1."
    echo "  Only a tagged release has a cosign signature and a build-provenance attestation to check;"
    echo "  both are written by release.yml's manifest job, so this channel has nothing to verify."
fi

echo "release-smoke: ${ref} passed"
