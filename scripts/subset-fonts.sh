#!/usr/bin/env bash
# Regenerate the vendored Inter faces as a Latin subset — reproducibly.
#
# This script IS the provenance of web/src/assets/fonts/*.woff2. Those files are no longer
# byte-for-byte upstream: they are derived, and a derived binary in a repository is only checkable
# if the derivation is a committed, re-runnable function of inputs whose hashes are recorded. That
# is the whole of issue #47 — subsetting is easy, subsetting reproducibly is the work.
#
# The chain, end to end:
#
#   Inter-4.1.zip                  SHA-256 pinned below, verified after download
#     └─ web/Inter-{Regular,Medium}.woff2   SHA-256 pinned below, verified after extraction
#          └─ pyftsubset, flags pinned below, fonttools + brotli pinned below
#               └─ web/src/assets/fonts/Inter-{Regular,Medium}-latin.woff2
#     └─ LICENSE.txt → web/src/assets/fonts/OFL.txt   (copied verbatim, not subset)
#
# Every step verifies its input before using it, so a hash in
# web/src/assets/fonts/README.md that no longer matches is a failure here rather than a silent
# re-vendoring of whatever the network served today.
#
# USAGE
#   scripts/subset-fonts.sh            regenerate web/src/assets/fonts/ in place  (make subset-fonts)
#   scripts/subset-fonts.sh --verify   regenerate into a temp dir and diff against the committed
#                                      bytes; non-zero on any drift               (make verify-fonts)
#
#   DKP_INTER_ZIP=/path/to/Inter-4.1.zip   use an already-downloaded archive instead of fetching it
#                                          (33 MB; useful offline and when iterating)
#
# NEEDS: python3 ≥ 3.9 and network access — for the archive, and for pip to fetch two wheels into a
# throwaway venv. Nothing is installed into the system, `make setup` is untouched, and no lockfile
# in this repository gains an entry. That is deliberate: this runs when a font changes, which is
# roughly never, and paying for it in every contributor's setup would be the wrong trade.
#
# IS THE OUTPUT ACTUALLY REPRODUCIBLE? Verified before this route was taken, not assumed. The two
# subset faces come out byte-identical across fonttools 4.62.0 and 4.63.0, brotli 1.1.0 and 1.2.0,
# CPython 3.11, 3.12 and 3.13, and linux/amd64 and linux/arm64. woff2 is Brotli over a deterministic
# repacking, and the one wall-clock input a font has — head.modified — is preserved from the source
# face rather than stamped, because pyftsubset's --recalc-timestamp defaults off and this script
# pins it off explicitly. The matrix is recorded in web/src/assets/fonts/README.md; the pinned
# versions below are belt-and-braces, not the thing holding it up.
#
# WHAT IS PINNED, AND WHY EACH FLAG IS THERE — see the FEATURES and UNICODES comments below. Change
# any of them and the output changes, which is why the README's checksum table and this file are
# checked against each other by test/repo/web_fonts_subset_test.go.

set -euo pipefail
# DKP_REPO_ROOT points the script at a tree other than this checkout, the same mechanism
# scripts/repo-gates.sh and scripts/licence-gate.sh use, so a test can drive it against a fixture.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

# --- Pinned inputs --------------------------------------------------------------------------------

INTER_VERSION="4.1"
ARCHIVE_URL="https://github.com/rsms/inter/releases/download/v${INTER_VERSION}/Inter-${INTER_VERSION}.zip"
ARCHIVE_SHA256="9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e"

# The two faces inside the archive, and their hashes. These are the bytes this repository vendored
# before #47 — the subset's input is exactly the artefact the old provenance table pointed at, which
# is what keeps the chain from restarting at "some font we downloaded".
SRC_REGULAR_ZIPPATH="web/Inter-Regular.woff2"
SRC_REGULAR_SHA256="e06f6b1bc553aaea4e4668023ed0ab0a147129c3107f511bc7d03d361b0ae085"
SRC_MEDIUM_ZIPPATH="web/Inter-Medium.woff2"
SRC_MEDIUM_SHA256="0ff3e94614e1493eb556314fd247ae6c4a85a7783b4cc86be539940cf83f2a48"

# The licence travels verbatim: it is not subset, not reformatted, and its hash is pinned like the
# faces'. The OFL requires the copyright notice and licence text to accompany the Font Software, and
# a subset is still the Font Software.
SRC_LICENCE_ZIPPATH="LICENSE.txt"
SRC_LICENCE_SHA256="262481e844521b326f5ecd053e59b98c8b2da78c8ee1bdbb6e8174305e54935a"

FONTTOOLS_VERSION="4.63.0"
BROTLI_VERSION="1.2.0"

FONT_DIR="web/src/assets/fonts"

# --- Pinned subsetting parameters -----------------------------------------------------------------

# The Google Fonts `latin` range, verbatim — the range every self-hosted Inter on the web is cut to,
# so it is the one choice here nobody has to re-derive. Latin-1 plus the Western-European odds and
# ends, General Punctuation, the euro, the trademark sign, BOM and the replacement character.
#
# Then a documented delta: the symbols Nocturne actually renders and Inter actually has. The mockups
# in docs/design/mockups/ are the screens this SPA gets built from, and they render → 16 times (time
# and sequence ranges, mapping rows), plus ≥, ≠, ∞ and ⌘ (the command-palette hint). Cutting to the
# bare Google range would drop all five, and each would then paint in the system-ui fallback at a
# different weight beside Inter text — the machine-dependent rendering inconsistency #45 exists to
# stop, arriving through the back door. ← and ≤ are their pairs: a UI with a forward arrow grows a
# back arrow, and "≥ 40%" grows "≤ 10%". Seven codepoints, ~600 bytes, all documented in
# web/src/assets/fonts/README.md.
#
# NOT added, and worth knowing: ∩ (U+2229, one admin-console label) and ▸ (U+25B8, three first-run
# rows) are NOT in Inter 4.1 at all, so they fall back to system-ui today with the full face and will
# behave exactly the same after this change. Requesting a codepoint the source lacks is silently
# ignored by pyftsubset (--ignore-missing-unicodes is its default), so adding them would be a lie in
# this list rather than a fix.
UNICODES="U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+2000-206F,U+2074,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD"
UNICODES="${UNICODES},U+2190,U+2192,U+2260,U+2264-2265,U+221E,U+2318"

# The OpenType features kept. pyftsubset's DEFAULT list does not include tnum, and dropping tnum is
# the expensive mistake available here: docs/design/mockups/ sets `font-variant-numeric:tabular-nums`
# 221 times across the four surfaces — every balance, every points column, every countdown — and
# without the feature those figures stop aligning in exactly the tables a DKP site exists to show. A
# subset that renders beautifully and silently unaligns the standings is worse than no subset.
#
# The rest is the Latin shaping core (calt, ccmp, kern, locl, mark, mkmk): the features a browser
# turns on by itself for ordinary text. Everything else Inter carries — the stylistic sets and
# character variants (ss01–ss08, cv01–cv14), salt, aalt, dlig, case, cpsp — is reachable only from
# `font-feature-settings`, which nothing in web/src uses, and each retained feature drags its
# alternate glyphs in with it: the full font-variant-numeric set (pnum, zero, ordn, sups, subs, sinf,
# frac, numr, dnom) costs 9 KB across the pair for CSS nobody has written.
#
# That is a bet, so it is backed by a gate rather than by hope:
# TestWebFonts_SubsetKeepsEveryFeatureTheStylesheetsAsk in test/repo/web_fonts_subset_test.go fails
# build if a stylesheet asks for a font-variant-* value whose OpenType feature is not in this list.
# Adding one is a one-word edit here plus `make subset-fonts`.
FEATURES="calt,ccmp,kern,locl,mark,mkmk,tnum"

# Every English-language name record, not pyftsubset's default 0–6. The extra ~240 bytes across the
# pair carry the designer, the vendor, the trademark statement and — the reason this is here — name
# IDs 13 and 14, the OFL notice and its URL. A DERIVED font should be able to state its own licence
# when a font inspector opens it, not rely on a README two directories up. Non-English names are
# still dropped: --name-languages defaults to English only.
NAME_IDS="*"

# --- Output ---------------------------------------------------------------------------------------
#
# The `-latin` suffix is load-bearing documentation: these files are NOT the upstream artefact and
# the filename says so at every reference site — fonts.css, NOTICE, THIRD_PARTY_NOTICES.txt and the
# Vite build output all repeat it.
FACES=(
    "Inter-Regular|${SRC_REGULAR_ZIPPATH}|${SRC_REGULAR_SHA256}|Inter-Regular-latin.woff2"
    "Inter-Medium|${SRC_MEDIUM_ZIPPATH}|${SRC_MEDIUM_SHA256}|Inter-Medium-latin.woff2"
)

# --- Helpers --------------------------------------------------------------------------------------

die() { printf '\033[31mFAIL\033[0m %s\n' "$1" >&2; exit 1; }
note() { printf '  %s\n' "$1"; }

# sha256 of a file, on both the laptops (shasum) and the runners (sha256sum).
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    else
        shasum -a 256 "$1" | cut -d' ' -f1
    fi
}

# expect_sha <file> <want> <what>. A mismatch is fatal and says which link of the chain broke: a
# re-tagged upstream release and a corrupted download need different responses from a human.
expect_sha() {
    local got
    got="$(sha256_of "$1")"
    [ "$got" = "$2" ] || die "$3 hash mismatch
  want $2
  got  $1 → $got
  The pinned input changed under us. Do NOT update the constant to match: check the upstream
  release first, then re-vendor deliberately (web/src/assets/fonts/README.md)."
}

VERIFY=0
case "${1:-}" in
    "") ;;
    --verify) VERIFY=1 ;;
    *) die "unknown argument: $1 (usage: subset-fonts.sh [--verify])" ;;
esac

command -v python3 >/dev/null 2>&1 || die "python3 not found — this script needs python3 ≥ 3.9 to run pyftsubset"
python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)' \
    || die "python3 is older than 3.9: $(python3 --version 2>&1)"
command -v unzip >/dev/null 2>&1 || die "unzip not found"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# --- 1. The archive -------------------------------------------------------------------------------

archive="$tmp/Inter-${INTER_VERSION}.zip"

if [ -n "${DKP_INTER_ZIP:-}" ]; then
    [ -f "$DKP_INTER_ZIP" ] || die "DKP_INTER_ZIP=$DKP_INTER_ZIP does not exist"
    cp "$DKP_INTER_ZIP" "$archive"
    note "archive   $DKP_INTER_ZIP (DKP_INTER_ZIP)"
else
    command -v curl >/dev/null 2>&1 || die "curl not found, and DKP_INTER_ZIP is unset"
    note "archive   $ARCHIVE_URL"
    curl -fsSL --retry 3 -o "$archive" "$ARCHIVE_URL" \
        || die "download failed. Offline? Point DKP_INTER_ZIP at a local copy of Inter-${INTER_VERSION}.zip."
fi

expect_sha "$archive" "$ARCHIVE_SHA256" "Inter-${INTER_VERSION}.zip"
note "          sha256 ok"

# --- 2. The sources -------------------------------------------------------------------------------

src="$tmp/src"
mkdir -p "$src"

unzip -q -o -j "$archive" "$SRC_LICENCE_ZIPPATH" -d "$src" || die "extracting $SRC_LICENCE_ZIPPATH failed"
mv "$src/$(basename "$SRC_LICENCE_ZIPPATH")" "$src/OFL.txt"
expect_sha "$src/OFL.txt" "$SRC_LICENCE_SHA256" "$SRC_LICENCE_ZIPPATH"

for face in "${FACES[@]}"; do
    IFS='|' read -r name zippath want _out <<< "$face"
    unzip -q -o -j "$archive" "$zippath" -d "$src" || die "extracting $zippath failed"
    expect_sha "$src/${name}.woff2" "$want" "$zippath"
done
note "sources   3 files extracted, all sha256 ok"

# --- 3. The tool ----------------------------------------------------------------------------------

venv="$tmp/venv"
python3 -m venv "$venv" || die "python3 -m venv failed — on Debian/Ubuntu, install python3-venv"
"$venv/bin/pip" install --quiet --disable-pip-version-check --no-input \
    "fonttools==${FONTTOOLS_VERSION}" "brotli==${BROTLI_VERSION}" \
    || die "pip install failed. Offline? This step needs network for two wheels."
note "tool      fonttools==${FONTTOOLS_VERSION} brotli==${BROTLI_VERSION} (throwaway venv)"

# --- 4. The subset --------------------------------------------------------------------------------

out="$tmp/out"
mkdir -p "$out"

for face in "${FACES[@]}"; do
    IFS='|' read -r name _zippath _want outname <<< "$face"
    # --no-recalc-timestamp is the one that matters for reproducibility: it keeps head.modified from
    # the source face instead of stamping the wall clock. It is pyftsubset's default and pinned here
    # anyway, because a default is not a promise. --no-recalc-bounds keeps the source's glyph bounds
    # for the same reason.
    "$venv/bin/pyftsubset" "$src/${name}.woff2" \
        --output-file="$out/$outname" \
        --flavor=woff2 \
        --unicodes="$UNICODES" \
        --layout-features="$FEATURES" \
        --name-IDs="$NAME_IDS" \
        --no-recalc-timestamp \
        --no-recalc-bounds \
        || die "pyftsubset failed for $name"
done

cp "$src/OFL.txt" "$out/OFL.txt"

# --- 5. Ship it, or check it ----------------------------------------------------------------------

printf '\n'

if [ "$VERIFY" -eq 1 ]; then
    drift=0
    for f in "$out"/*; do
        rel="$FONT_DIR/$(basename "$f")"
        if [ ! -f "$rel" ]; then
            printf '\033[31mMISSING\033[0m %s — regeneration produced a file the tree does not have\n' "$rel"
            drift=1
            continue
        fi
        if cmp -s "$f" "$rel"; then
            printf '  \033[32mok\033[0m       %-34s %8s bytes  %s\n' "$rel" "$(wc -c <"$rel" | tr -d ' ')" "$(sha256_of "$rel")"
        else
            printf '\033[31mDRIFT\033[0m    %s\n' "$rel"
            printf '           committed  %8s bytes  %s\n' "$(wc -c <"$rel" | tr -d ' ')" "$(sha256_of "$rel")"
            printf '           rebuilt    %8s bytes  %s\n' "$(wc -c <"$f" | tr -d ' ')" "$(sha256_of "$f")"
            drift=1
        fi
    done

    # A committed face the regeneration does not produce is drift in the other direction — a leftover
    # from an older recipe, still embedded in every binary.
    for f in "$FONT_DIR"/*.woff2; do
        [ -f "$out/$(basename "$f")" ] || {
            printf '\033[31mORPHAN\033[0m   %s — committed but this script does not produce it\n' "$f"
            drift=1
        }
    done

    if [ "$drift" -ne 0 ]; then
        printf '\n\033[31mFAIL\033[0m the vendored faces are not what this script produces.\n'
        printf '  Either the committed bytes were edited by hand, or the pinned parameters changed\n'
        printf '  without `make subset-fonts` being run and the README table updated.\n'
        exit 1
    fi

    printf '\n\033[32mok\033[0m %s reproduces byte-for-byte from %s\n' "$FONT_DIR" "$(basename "$ARCHIVE_URL")"
    exit 0
fi

cp "$out"/* "$FONT_DIR/"

printf 'Wrote %s:\n\n' "$FONT_DIR"
printf '| File | Bytes | SHA-256 |\n|---|---|---|\n'
for f in "$FONT_DIR"/*.woff2 "$FONT_DIR/OFL.txt"; do
    printf '| `%s` | %s | `%s` |\n' "$(basename "$f")" "$(wc -c <"$f" | tr -d ' ')" "$(sha256_of "$f")"
done
printf '\nPaste that table into %s/README.md, then run `make third-party-notices`.\n' "$FONT_DIR"
