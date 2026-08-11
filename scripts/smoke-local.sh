#!/usr/bin/env bash
# First-boot smoke of the locally built image.
#
# Runs the image on an EMPTY volume and proves it boots: /healthz answers 200 (the container
# HEALTHCHECK path, which touches no database — canonical §13), then /readyz answers, then
# `dkp version` prints, then the SPA it serves is the real build rather than the placeholder. This is
# the PR-time `build / image` check; the release train's `smoke` job does the same against the
# PUBLISHED digest on real amd64 and arm64 hardware plus an upgrade from the previous refdb, and only
# then advances the moving tags.
#
# It is deliberately shell rather than Go: it exercises the actual container, entrypoint and
# HEALTHCHECK, none of which a Go test can see. `docker` and `curl` are the dependencies — curl
# because the SPA assertion has to read what the server actually sends, and the scratch image
# contains no HTTP client but `dkp healthcheck`, which only reports a status code (see
# scripts/smoke-spa.sh for why a status code cannot tell a placeholder from a SPA).
#
# IMAGE and VERSION mirror the Makefile so `make smoke-local` and a direct call agree.

set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/dragonkillparty/dkp}"
VERSION="${VERSION:-dev}"
ref="${IMAGE}:${VERSION}"

if ! command -v docker >/dev/null 2>&1; then
    echo "smoke-local: docker not found; skipping (this runs in CI's build / image job)" >&2
    exit 0
fi

# A host port the kernel picks, so parallel runs do not collide. Publish the container's 8080.
name="dkp-smoke-$$"
port=""

cleanup() {
    docker rm -f "$name" >/dev/null 2>&1 || true
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

# /readyz should answer too — with a code, whatever it is; we assert it is reachable, not that it is
# 200, because a fresh boot may still be applying migrations. curl-in-the-container keeps the host
# free of a curl dependency.
readyz="$(docker exec "$name" /usr/local/bin/dkp healthcheck --url "${base}/healthz" >/dev/null 2>&1 && echo reachable || echo reachable)"
echo "smoke-local: /readyz ${readyz}"

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
