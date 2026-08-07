#!/usr/bin/env bash
# Install the pinned Atlas CLI, verifying it against a committed SHA-256.
#
# Atlas is the ONLY pinned tool in this project that cannot be `go install`ed, and that is a fact
# about Atlas rather than a preference here:
#
#   * ariga.io/atlas/cmd/atlas's own go.mod contains `replace ariga.io/atlas => ../..`, and
#     `go install <pkg>@<version>` refuses any module carrying a replace directive.
#   * The vanity import path serves no go-import meta tags for that subpath; the module proxy
#     answers "unrecognized import path".
#   * The last version ever published to the proxy for that module path is v0.13.1, from August
#     2023. The cmd/atlas/vX tags stopped at v0.9.1. The current release is v1.3.0.
#
# So the `go install <module>@$(VERSION)` line every other tool in `make setup` uses is not
# available, and the alternative — `curl https://atlasgo.sh | sh` — pins nothing and verifies
# nothing. This script is the third option: a version pinned in one place, an artefact verified
# against a checksum committed to this repository, and one implementation shared by `make setup`
# and .github/actions/setup-toolchain, so the laptop and CI cannot install different binaries.
#
# It is called as `make install-atlas` from both. The Makefile owns GOTOOLS_BIN and passes it in;
# nothing here re-derives an install location, because two copies of that logic is exactly the
# class of drift the shared script exists to remove.

set -euo pipefail

# DKP_REPO_ROOT lets a test point this script at a fixture tree — the same mechanism the gate
# scripts use, for a narrower reason. There is no useful test that downloads a 30 MB binary, but
# there IS a useful test that a Makefile pin with no matching checksum row fails *before* the
# network is touched. That test needs a fabricated Makefile and atlas.sha256 pair in t.TempDir(),
# and it cannot live in this checkout without breaking the real install. `make install-atlas`
# strips the variable with `env -u` for the same reason every other gate target does.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() { printf '\033[31m  %s\033[0m\n' "$*" >&2; exit 1; }

dest=${1:-}
[ -n "$dest" ] || die "usage: install-atlas.sh <dest-dir>   (make install-atlas passes GOTOOLS_BIN)"

# The version, from the single source of truth. `$1==k { print $3 }` over `NAME ?= value` is the
# same three-field parse .github/actions/setup-toolchain/action.yml already uses for every other
# pin, so ATLAS_VERSION must keep that shape.
version=$(awk '$1=="ATLAS_VERSION" { print $3; exit }' Makefile)
[ -n "$version" ] || die "no ATLAS_VERSION in Makefile — the pin must read 'ATLAS_VERSION ?= vX.Y.Z'"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
    linux|darwin) ;;
    *) die "unsupported OS '$os' — the Atlas community edition publishes linux and darwin only" ;;
esac

case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) die "unsupported architecture '$(uname -m)'" ;;
esac

asset="atlas-community-$os-$arch-$version"

# Look the checksum up BEFORE any network access. A version bump that forgets scripts/atlas.sha256
# fails here, loudly and offline, rather than installing an unverified binary.
want=$(awk -v a="$asset" '$2==a { print $1; exit }' scripts/atlas.sha256)
[ -n "$want" ] || die "no checksum pinned for $asset — add its row to scripts/atlas.sha256"

# sha256 <file> — macOS ships shasum, Ubuntu ships sha256sum, and CI runs on both.
sha256() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{ print $1 }'
    elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{ print $1 }'
    else die "neither sha256sum nor shasum is available"; fi
}

# Idempotence is checked against the CHECKSUM, not against `atlas version`. Comparing version
# strings would accept a binary that reports v1.3.0 and is not the artefact this repository pinned,
# which is the whole thing the checksum exists to detect.
if [ -x "$dest/atlas" ] && [ "$(sha256 "$dest/atlas")" = "$want" ]; then
    printf '  atlas %s already installed and verified\n' "$version"
    exit 0
fi

mkdir -p "$dest"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

printf '  installing atlas %s (community edition, %s/%s)\n' "$version" "$os" "$arch"
curl -fsSL --retry 3 --retry-connrefused -o "$tmp/atlas" \
    "https://release.ariga.io/atlas/$asset" \
    || die "download failed: https://release.ariga.io/atlas/$asset"

got=$(sha256 "$tmp/atlas")
[ "$got" = "$want" ] || die "checksum mismatch for $asset
    expected $want
    got      $got
  Do not install this binary. Either scripts/atlas.sha256 is stale, or the artefact changed."

# install(1) writes atomically enough for this purpose and sets the mode in one step. Writing over
# a running binary would be the alternative and it fails on some filesystems.
install -m 0755 "$tmp/atlas" "$dest/atlas"
printf '\033[32m  atlas %s verified and installed\033[0m into %s\n' "$version" "$dest"
