#!/usr/bin/env bash
# verify-image-arch — prove a locally built image is the architecture it claims to be.
#
# Run by `make verify-image-arm64` (nightly-verify.yml's `image / arm64-cross`) immediately after
# `PLATFORM=linux/arm64 make docker`, against the image that build --load'ed into the local store.
#
# Usage: verify-image-arch.sh <platform>          e.g. linux/arm64
# Inputs: IMAGE, VERSION — exported by the Makefile, so this measures exactly the ref that was built.
#
# WHY THE BYTES AND NOT JUST THE LABEL
# ------------------------------------
# buildx writes the image config's architecture from `--platform`, so `docker image inspect` reports
# what the build was ASKED for, not what it produced. The Dockerfile is what makes the two agree: it
# threads buildx's TARGETARCH into `GOARCH=${TARGETARCH} go build`. Drop that ARG — a refactor, a
# merge, a "simplification" — and the build still succeeds, the image is still labelled arm64, and
# the binary inside it is amd64. Nothing else in this repository would notice: running an arm64 image
# is the one thing CI never does, because that needs the QEMU this project bans (release.yml's
# header, QEMU001 in scripts/repo-gates.sh). So the check reads the ELF header of the binary itself,
# which requires no emulation and cannot be satisfied by a label.
#
# Both checks are hard failures. This script is a gate, not an advisory measurement like
# scripts/image-size.sh — a missing tool here is a broken runner, not a reason to pass.

set -euo pipefail

platform="${1:?usage: verify-image-arch.sh <platform>, e.g. linux/arm64}"

IMAGE="${IMAGE:-ghcr.io/prokopto-dev/dragonkillparty}"
VERSION="${VERSION:-dev}"
ref="${IMAGE}:${VERSION}"

# Per platform: the architecture `docker image inspect` must report, and the ELF machine type the
# binary must carry. e_machine is a little-endian uint16 at offset 18 of the ELF header —
# 0x00b7 = EM_AARCH64, 0x003e = EM_X86_64 (elf.h). Read as two bytes, so the constants below are the
# on-disk order.
case "$platform" in
linux/amd64)
    want_arch="amd64"
    want_machine="3e00"
    ;;
linux/arm64)
    want_arch="arm64"
    want_machine="b700"
    ;;
*)
    echo "verify-image-arch: unsupported platform ${platform}" >&2
    echo "  Known: linux/amd64, linux/arm64. Adding one means adding its EM_ constant here — the" >&2
    echo "  ELF check is the point of this script, so a platform without one must not silently pass." >&2
    exit 1
    ;;
esac

for tool in docker od; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "verify-image-arch: ${tool} not found; it is required to inspect ${ref}" >&2
        exit 1
    fi
done

# --- 1. The image config -----------------------------------------------------------------------
# Catches the plain miss: PLATFORM never reached buildx, so the "arm64" build is a host build.
got_arch="$(docker image inspect "$ref" --format '{{.Architecture}}')" || {
    echo "verify-image-arch: ${ref} is not in the local image store." >&2
    echo "  \`make docker\` builds with --load, which puts it there; a build that pushed instead" >&2
    echo "  would leave nothing here to check." >&2
    exit 1
}

if [ "$got_arch" != "$want_arch" ]; then
    echo "verify-image-arch: ${ref} reports architecture ${got_arch}, expected ${want_arch}." >&2
    echo "  The build did not target ${platform} at all — check that PLATFORM reached the" >&2
    echo "  \`docker buildx build\` line in the Makefile's \`docker\` recipe." >&2
    exit 1
fi
echo "verify-image-arch: ${ref} image config says ${got_arch}"

# --- 2. The binary inside ----------------------------------------------------------------------
work="$(mktemp -d)"
cid=""
cleanup() {
    if [ -n "$cid" ]; then
        docker rm -f "$cid" >/dev/null 2>&1 || true
    fi
    rm -rf "$work"
}
trap cleanup EXIT

# `docker create` allocates a container and never starts it, so a foreign-architecture image is fine
# here — no instruction of the target architecture is executed, and none needs to be. --platform is
# explicit so the daemon does not compare the image against the host and complain about a mismatch
# that is the entire point of this job.
cid="$(docker create --platform "$platform" "$ref")"
docker cp "${cid}:/usr/local/bin/dkp" "${work}/dkp"

magic="$(od -An -t x1 -N 4 "${work}/dkp" | tr -d ' \n')"
if [ "$magic" != "7f454c46" ]; then
    echo "verify-image-arch: /usr/local/bin/dkp in ${ref} is not an ELF binary (magic ${magic})." >&2
    exit 1
fi

machine="$(od -An -t x1 -j 18 -N 2 "${work}/dkp" | tr -d ' \n')"
if [ "$machine" != "$want_machine" ]; then
    echo "verify-image-arch: ${ref} is labelled ${want_arch}, but /usr/local/bin/dkp has ELF machine" >&2
    echo "  0x${machine} and ${want_arch} is 0x${want_machine}." >&2
    echo "  The image config comes from --platform, so buildx labelled this correctly and compiled" >&2
    echo "  something else: deploy/Dockerfile's build stage must pass buildx's TARGETARCH through to" >&2
    echo "  \`GOARCH=\${TARGETARCH} go build\`. An image in this state boots nowhere, and no other" >&2
    echo "  check in this repository can see it — nothing in CI runs an arm64 image (no QEMU)." >&2
    exit 1
fi

echo "verify-image-arch: ${ref} contains a ${want_arch} binary (ELF machine 0x${machine}) — ok"
