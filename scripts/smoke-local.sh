#!/usr/bin/env bash
# First-boot smoke of the locally built image.
#
# Runs the image on an EMPTY volume and proves it boots: /healthz answers 200 (the container
# HEALTHCHECK path, which touches no database — canonical §13), then /readyz answers with a readiness
# report, then `dkp version` prints, then the SPA it serves is the real build rather than the
# placeholder. This is the PR-time `build / image` check; the release train's `smoke` job does the
# same against the PUBLISHED digest on real amd64 and arm64 hardware plus an upgrade from the
# previous refdb, and only then advances the moving tags.
#
# It is deliberately shell rather than Go: it exercises the actual container, entrypoint and
# HEALTHCHECK, none of which a Go test can see. `docker` and `curl` are the dependencies — curl
# because the SPA assertion has to read what the server actually sends, and because /readyz is
# probed from the host at the published port. The scratch image contains no HTTP client but
# `dkp healthcheck`, which only maps 200 to exit 0: it cannot express "answered, and 503 is a correct
# answer here", and cannot read a body at all (see scripts/smoke-spa.sh for why a status code cannot
# tell a placeholder from a SPA).
#
# IMAGE and VERSION mirror the Makefile so `make smoke-local` and a direct call agree.

set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/prokopto-dev/dragonkillparty}"
VERSION="${VERSION:-dev}"
ref="${IMAGE}:${VERSION}"

if ! command -v docker >/dev/null 2>&1; then
    echo "smoke-local: docker not found; skipping (this runs in CI's build / image job)" >&2
    exit 0
fi

# curl is checked here rather than where it is first used, because both users are below: the /readyz
# probe and scripts/smoke-spa.sh. Failing loud beats skipping — a check that quietly vanishes on the
# machine without the dependency is the shape of defect this script has already shipped twice.
if ! command -v curl >/dev/null 2>&1; then
    echo "smoke-local: curl not found — it is needed to read what the container serves" >&2
    exit 1
fi

# A host port the kernel picks, so parallel runs do not collide. Publish the container's 8080.
name="dkp-smoke-$$"
port=""
work="$(mktemp -d)"

cleanup() {
    docker rm -f "$name" >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT

echo "smoke-local: booting ${ref} on an empty volume"
# --tmpfs /data: an empty, writable volume for a first-boot migration, gone when the container dies.
docker run -d --name "$name" --tmpfs /data -p 127.0.0.1:0:8080 "$ref" >/dev/null

# Discover the mapped host port.
port="$(docker port "$name" 8080/tcp | head -1 | sed 's/.*://')"
if [ -z "$port" ]; then
    echo "smoke-local: could not determine the published port" >&2
    docker logs "$name" >&2 || true
    exit 1
fi

base="http://127.0.0.1:${port}"

# Poll /healthz until it answers 200 or we run out of tries. No sleep-and-hope beyond a short,
# bounded backoff: a container that has not answered /healthz in this many tries is not booting.
ok=0
for _ in $(seq 1 60); do
    code="$(docker run --rm --network "container:${name}" \
        --entrypoint /usr/local/bin/dkp "$ref" healthcheck >/dev/null 2>&1 && echo ok || echo no)"
    if [ "$code" = "ok" ]; then
        ok=1
        break
    fi
    sleep 0.5
done

if [ "$ok" -ne 1 ]; then
    echo "smoke-local: /healthz never answered — the container did not boot" >&2
    docker logs "$name" >&2 || true
    exit 1
fi
echo "smoke-local: /healthz ok (via dkp healthcheck)"

# /readyz must answer, and answer AS ITSELF. Not 200 specifically — a fresh boot may legitimately
# still be applying migrations, which is a 503 with state "pending" (internal/api/ready.go) — but a
# readiness report, from the readiness handler.
#
# Three checks, because each one alone is passable by something broken:
#
#   1. curl connected at all. This is the half that was missing: the line here used to end
#      `&& echo reachable || echo reachable`, so the command's exit status was discarded by
#      construction and the step printed "reachable" on every run since PR 7 (issue #84).
#   2. the status is 200 or 503. Those are the only two handleReadyz produces; anything else is a
#      different handler answering.
#   3. the body carries a "state". Load-bearing, not belt-and-braces: when Config.Readiness is nil
#      the route is never registered, and internal/ui's SPA catch-all then answers /readyz with
#      index.html and a 200. A status-code check alone calls that ready.
#
# From the HOST, at the published port. The old line probed $base — a host-mapped port — from INSIDE
# the container's network namespace, where nothing listens on it, so the probe could not have
# succeeded even without the tautology hiding it. curl is already this script's dependency (see the
# header, and scripts/smoke-spa.sh below), so nothing new is needed to do it correctly.
readyz_body="$work/readyz.json"

echo "smoke-local: GET ${base}/readyz"
readyz_code="$(curl -sS --max-time 20 -o "$readyz_body" -w '%{http_code}' "${base}/readyz")" || {
    echo "smoke-local: GET ${base}/readyz did not answer — the container is up but /readyz is not" >&2
    docker logs "$name" >&2 || true
    exit 1
}

# `|| true` on the body dumps only, and only to keep a SIGPIPE from `head` from replacing the exit
# code of a failure that is already being reported. Never on the checks themselves.
case "$readyz_code" in
    200 | 503) ;;
    *)
        echo "smoke-local: GET ${base}/readyz returned ${readyz_code}; handleReadyz answers 200 or 503 only" >&2
        sed 's/^/    /' "$readyz_body" | head -20 >&2 || true
        exit 1
        ;;
esac

readyz_state="$(sed -n 's/.*"state"[[:space:]]*:[[:space:]]*"\([a-z]*\)".*/\1/p' "$readyz_body" | head -1)"
if [ -z "$readyz_state" ]; then
    echo "smoke-local: ${base}/readyz answered ${readyz_code} with no readiness report in the body." >&2
    echo "  Nothing registered /readyz, so something else answered for it — the SPA catch-all serves" >&2
    echo "  index.html with a 200 for every path it does not have. What came back:" >&2
    sed 's/^/    /' "$readyz_body" | head -20 >&2 || true
    exit 1
fi
echo "smoke-local: /readyz ${readyz_code} — state ${readyz_state}"

# `dkp version` must print all three stamps.
if ! docker exec "$name" /usr/local/bin/dkp version; then
    echo "smoke-local: dkp version failed" >&2
    exit 1
fi

# The image must serve the BUILT SPA. This is the check that would have caught issue #55 on the day
# it landed: the container built its binary without ever running the Vite build, so every image
# booted perfectly and served "web UI not yet built into this binary". Everything above this line
# passed throughout.
bash "$(dirname "$0")/smoke-spa.sh" "$base"

echo "smoke-local: ok"
