#!/usr/bin/env bash
# Install the pinned shellcheck, verifying it against a committed SHA-256.
#
# This is the SECOND pinned tool in the project that cannot be `go install`ed, and for a blunter
# reason than Atlas: shellcheck is written in Haskell. The alternatives were all worse:
#
# (A comment line here may not OPEN with the tool's own name: shellcheck reads `# shellcheck ...` as
# a directive and fails the file with SC1072. Found by this gate, on this file.)
#
#   * the runner image's shellcheck — unpinned, whichever version ubuntu-24.04 happens to carry, so
#     CI and the laptop would disagree about a green lint and a base-image bump would move the gate.
#     That is exactly the defect issue #156 filed against jobs that declared no toolchain.
#   * a third-party setup action — another `uses:` to pin and to trust, wrapping the same download.
#   * `apt-get install shellcheck` — unpinned again, and slower than fetching one binary.
#
# So this mirrors scripts/install-atlas.sh exactly: a version pinned in one place (SHELLCHECK_VERSION
# in the Makefile), an artefact verified against a checksum committed to this repository and reviewed
# in a pull request, and ONE implementation shared by `make setup` and
# .github/actions/setup-toolchain, so the laptop and CI cannot install different binaries.
#
# LICENCE. shellcheck is GPL-3.0. It is a development TOOL invoked as a separate process, never
# linked into `dkp` and never distributed with it, so it is not in either graph the licence gate
# reads (the Go runtime module graph and the JS production graph) and it does not touch this
# project's Apache-2.0 posture — the same standing as gofumpt or golangci-lint, whose licences are
# likewise not the product's. Transcribing shellcheck's source would be a different question; running
# it is not.
#
# It is called as `make install-shellcheck` from both call sites. The Makefile owns GOTOOLS_BIN and
# passes it in; nothing here re-derives an install location, because two copies of that logic is the
# class of drift a shared script exists to remove.
set -euo pipefail

# DKP_REPO_ROOT lets a test point this script at a fixture tree, for install-atlas.sh's reason: there
# is no useful test that downloads a binary, but there IS a useful test that a Makefile pin with no
# matching checksum row fails BEFORE the network is touched. `make install-shellcheck` strips the
# variable with `env -u`.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() {
    printf '\033[31m  %s\033[0m\n' "$*" >&2
    exit 1
}

dest=${1:-}
[ -n "$dest" ] || die "usage: install-shellcheck.sh <dest-dir>   (make install-shellcheck passes GOTOOLS_BIN)"

# The version, from the single source of truth. `$1==k { print $3 }` over `NAME ?= value` is the same
# three-field parse .github/actions/setup-toolchain/action.yml uses for every other pin, so
# SHELLCHECK_VERSION must keep that shape.
version=$(awk '$1=="SHELLCHECK_VERSION" { print $3; exit }' Makefile)
[ -n "$version" ] || die "no SHELLCHECK_VERSION in Makefile — the pin must read 'SHELLCHECK_VERSION ?= vX.Y.Z'"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
    linux | darwin) ;;
    *) die "unsupported OS '$os' — koalaman publishes linux and darwin builds only" ;;
esac

# koalaman names assets by uname's architecture strings, NOT by Go's. x86_64 and aarch64, never
# amd64/arm64 — getting this wrong yields a 404 rather than a wrong binary, but it yields it on
# somebody's laptop rather than here.
case "$(uname -m)" in
    x86_64 | amd64) arch=x86_64 ;;
    arm64 | aarch64) arch=aarch64 ;;
    *) die "unsupported architecture '$(uname -m)'" ;;
esac

asset="shellcheck-$version.$os.$arch.tar.xz"

# Look the checksum up BEFORE any network access. A version bump that forgets
# scripts/shellcheck.sha256 fails here, loudly and offline, rather than installing an unverified
# binary. TestInstallShellcheck_PinWithoutChecksum_FailsBeforeTheNetwork holds this.
want=$(awk -v a="$asset" '$2==a { print $1; exit }' scripts/shellcheck.sha256)
[ -n "$want" ] || die "no checksum pinned for $asset — add its row to scripts/shellcheck.sha256"

# sha256 <file> — macOS ships shasum, Ubuntu ships sha256sum, and CI runs on both.
sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{ print $1 }'
    else
        die "neither sha256sum nor shasum is available"
    fi
}

# Idempotence is checked against the VERSION STRING here rather than against the binary's checksum,
# and the difference from install-atlas.sh is a fact about the artefacts: Atlas publishes the binary
# itself, so the pinned digest IS the installed file's digest. koalaman publishes a tar.xz, so the
# committed digest covers the ARCHIVE and the extracted binary hashes to something else entirely.
# Re-verifying would mean re-downloading, which is the cost this check exists to avoid.
if [ -x "$dest/shellcheck" ] && "$dest/shellcheck" --version 2>/dev/null | grep -qx "version: ${version#v}"; then
    printf '  shellcheck %s already installed\n' "$version"

    exit 0
fi

mkdir -p "$dest"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

printf '  installing shellcheck %s (%s/%s)\n' "$version" "$os" "$arch"
curl -fsSL --retry 3 --retry-connrefused -o "$tmp/$asset" \
    "https://github.com/koalaman/shellcheck/releases/download/$version/$asset" \
    || die "download failed: https://github.com/koalaman/shellcheck/releases/download/$version/$asset"

got=$(sha256 "$tmp/$asset")
[ "$got" = "$want" ] || die "checksum mismatch for $asset
    expected $want
    got      $got
  Do not install this binary. Either scripts/shellcheck.sha256 is stale, or the artefact changed."

# Extracted only after the archive has been verified — an unverified tarball is not something to
# hand to tar. -J is xz; both the macOS bsdtar and Ubuntu's GNU tar handle it.
tar -xJf "$tmp/$asset" -C "$tmp" \
    || die "could not extract $asset — is xz support present in tar?"

bin="$tmp/shellcheck-$version/shellcheck"
[ -x "$bin" ] || die "$asset did not contain shellcheck-$version/shellcheck — has the archive layout changed?"

install -m 0755 "$bin" "$dest/shellcheck"
printf '\033[32m  shellcheck %s verified and installed\033[0m into %s\n' "$version" "$dest"
