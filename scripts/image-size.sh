#!/usr/bin/env bash
# advisory / image-size — measure the compressed image against the 30 MB budget.
#
# docs/design/06-cicd-and-release.md §7 sets the scratch image budget at 30 MB compressed / 65 MB
# uncompressed. This measures the COMPRESSED size (the sum of the gzipped layer sizes, which is what
# a `docker pull` transfers and what the registry reports) and compares it to the budget.
#
# ADVISORY on the PR path: it prints the number and warns on a breach, and the ci.yml step runs it
# with continue-on-error so a breach is a signal, not a merge block — a legitimate breach is a
# one-line budget edit reviewed in the diff. On the RELEASE path the same measurement is a hard gate
# (a shipped image that blew the budget is a real regression). MODE selects which: MODE=enforce exits
# non-zero on a breach, anything else advises.
#
# `docker` is the only dependency; the number comes from `docker manifest inspect` (registry sizes)
# with a fallback to `docker image inspect` for a locally built, unpushed image.

set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/dragonkillparty/dkp}"
VERSION="${VERSION:-dev}"
ref="${IMAGE}:${VERSION}"
MODE="${MODE:-advise}"

# 30 MB, in bytes. Kept as the literal so the number in the code matches the number in the doc.
budget_bytes=$((30 * 1024 * 1024))

if ! command -v docker >/dev/null 2>&1; then
    echo "image-size: docker not found; skipping (this runs in CI's build / image job)" >&2
    exit 0
fi

# Prefer the registry's own compressed sizes (what a pull transfers). Fall back to the local image's
# layer sizes for a build that has not been pushed — those are uncompressed, so the fallback is a
# conservative over-estimate, which is the safe direction for a budget.
compressed=""
if manifest="$(docker manifest inspect "$ref" 2>/dev/null)"; then
    compressed="$(printf '%s' "$manifest" \
        | grep -oE '"size":[[:space:]]*[0-9]+' \
        | grep -oE '[0-9]+' \
        | awk '{ s += $1 } END { print s }')"
fi

if [ -z "$compressed" ] || [ "$compressed" = "0" ]; then
    if size="$(docker image inspect "$ref" --format '{{.Size}}' 2>/dev/null)"; then
        compressed="$size"
        echo "image-size: using LOCAL uncompressed size (image not pushed) — a conservative bound" >&2
    fi
fi

if [ -z "$compressed" ] || [ "$compressed" = "0" ]; then
    echo "image-size: could not measure ${ref}; is it built?" >&2
    # Not a hard failure in advise mode: the measurement is missing, not breached.
    [ "$MODE" = "enforce" ] && exit 1
    exit 0
fi

mb() { awk -v b="$1" 'BEGIN { printf "%.1f", b / 1048576 }'; }

msg="image-size: ${ref} is $(mb "$compressed") MB (budget $(mb "$budget_bytes") MB)"

# Emit into the CI summary when available, so the number is visible without opening the log.
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$msg" >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$compressed" -gt "$budget_bytes" ]; then
    echo "::warning::${msg} — OVER budget"
    echo "$msg — OVER budget"
    [ "$MODE" = "enforce" ] && exit 1
    exit 0
fi

echo "$msg — ok"
