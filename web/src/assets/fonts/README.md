# Vendored fonts

The one place third-party licensed content is committed into this repository.

`OFL.txt` is byte-for-byte upstream. The two `.woff2` faces are **derived**: a Latin subset of
upstream Inter 4.1, cut by [`scripts/subset-fonts.sh`](../../../../scripts/subset-fonts.sh). A
generated binary in a repository is only checkable if the generator is committed and its output is
reproducible, so that script pins every input hash, every flag and both tool versions, and
regenerating produces these bytes exactly — on a laptop and on a runner, this year and next.

## Inter 4.1, Latin subset — SIL Open Font License 1.1

`docs/design/09-frontend-and-design-system.md` §3 requires Inter at 400 (body) and 500 (headings),
**self-hosted**. The binary serves the SPA offline; a render-blocking `fonts.googleapis.com` request
contradicts that, and `mockups/nocturne/styles.css`'s `@import url('https://fonts.googleapis.com/…')`
is the thing that must not be copied. `scripts/repo-gates.sh` WEB003 is the gate that keeps it out.

| File | Bytes | SHA-256 |
|---|---|---|
| `Inter-Regular-latin.woff2` | 18232 | `a37501ee19b21c63e39c325b1f5533741e607adbd874e850e84e3525cf74df73` |
| `Inter-Medium-latin.woff2` | 18508 | `14edf904092b6ad8ac9eb17f54d7b50150f3cf8cdec713882b22188d06533b1e` |
| `OFL.txt` | 4380 | `262481e844521b326f5ecd053e59b98c8b2da78c8ee1bdbb6e8174305e54935a` |

36 KB for the pair, down from 225 KB — 185 KB less in git, in every binary and in every container
image. `test/repo/web_fonts_subset_test.go` asserts this table matches the committed bytes, so a
hand-edited face or a stale row fails `make check`.

**The `-latin` suffix is deliberate.** These are not the upstream artefact and the filename says so
everywhere it appears — `fonts.css`, `NOTICE`, `THIRD_PARTY_NOTICES.txt`, the Vite output.

## Provenance

| Link | Artefact | SHA-256 |
|---|---|---|
| upstream archive | [`Inter-4.1.zip`](https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip), released 2024-11-16 | `9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e` |
| subset input | that archive's `web/Inter-Regular.woff2` | `e06f6b1bc553aaea4e4668023ed0ab0a147129c3107f511bc7d03d361b0ae085` |
| subset input | that archive's `web/Inter-Medium.woff2` | `0ff3e94614e1493eb556314fd247ae6c4a85a7783b4cc86be539940cf83f2a48` |
| licence | that archive's `LICENSE.txt`, copied verbatim as `OFL.txt` | `262481e844521b326f5ecd053e59b98c8b2da78c8ee1bdbb6e8174305e54935a` |

The two inputs are the exact bytes this repository vendored before the subset landed (#45), which is
what keeps the chain from restarting at "some font we downloaded". `scripts/subset-fonts.sh` verifies
all four hashes before it subsets anything, and refuses to proceed on a mismatch rather than
re-vendoring whatever the network served today.

**The command**, as the script runs it, with `fonttools==4.63.0` and `brotli==1.2.0` in a throwaway
virtualenv:

```sh
pyftsubset Inter-Regular.woff2 \
  --output-file=Inter-Regular-latin.woff2 \
  --flavor=woff2 \
  --unicodes=U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+2000-206F,U+2074,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD,U+2190,U+2192,U+2260,U+2264-2265,U+221E,U+2318 \
  --layout-features=calt,ccmp,kern,locl,mark,mkmk,tnum \
  --name-IDs='*' \
  --no-recalc-timestamp \
  --no-recalc-bounds
```

## Is it actually reproducible?

Checked before this route was taken, not assumed — an unverifiable "re-run it and commit whatever
came out" would have been no better than the ad-hoc subset #47 was filed to avoid. The two faces come
out **byte-identical** across every axis that plausibly varies:

| Axis | Values tried | Result |
|---|---|---|
| repeat runs | same image, twice | identical |
| CPU architecture | linux/amd64, linux/arm64, darwin/arm64 | identical |
| Python | CPython 3.11, 3.12, 3.13 | identical |
| `fonttools` | 4.62.0, 4.63.0 | identical |
| `brotli` | 1.1.0, 1.2.0 | identical |

woff2 is Brotli over a deterministic repacking, and the one wall-clock input a font carries —
`head.modified` — is preserved from the source face rather than stamped: `--recalc-timestamp` is off
by pyftsubset's default and pinned off explicitly, because a default is not a promise. The version
pins are therefore belt-and-braces rather than the thing holding this up.

Two gates keep it true:

- `make verify-fonts` regenerates into a temp directory and diffs against the committed bytes. It
  needs network (the 33 MB archive, two wheels), so it runs **nightly**, not in PR CI — a GitHub
  outage must not fail an unrelated parser fix. `nightly-verify.yml`'s `fonts / subset-reproducible`
  job is that run.
- `test/repo/web_fonts_subset_test.go` covers the parts that need no network on every `make check`: the
  table above matches the committed bytes, the provenance hashes match the script's constants, the
  stylesheet's `unicode-range` matches `--unicodes`, and no stylesheet asks for an OpenType feature
  the subset dropped.

## What the subset keeps, and what it drops

**Characters.** The Google Fonts `latin` range verbatim — Latin-1, the Western-European odds and
ends, General Punctuation, the euro, the trademark sign, BOM, the replacement character — plus seven
codepoints Nocturne actually renders and Inter actually has:

| Added | Why |
|---|---|
| `→` U+2192 | 16 uses across the mockups: time and sequence ranges (`23:00 → in progress`, `seq 88,214 → 88,241`), mapping rows |
| `←` U+2190 | its pair; a UI with a forward arrow grows a back arrow |
| `≥` U+2265 | attendance thresholds — the portal's filter tag and the ballot's eligibility rule (`attendance ≥ 40%`) |
| `≤` U+2264 | its pair; `≥ 40%` grows `≤ 10%` |
| `≠` U+2260 | a loot-priority rule expression (`character.is_main AND spec ≠ primary`) |
| `∞` U+221E | an unlimited quantity in the guild-bank table |
| `⌘` U+2318 | the command-palette hint (`⌘K`) |

Without them each would paint in the system-ui fallback beside Inter text at a different weight —
the machine-dependent rendering inconsistency #45 exists to stop, arriving through the back door.

Dropped, and safe to drop: Greek, Cyrillic, Vietnamese, Latin Extended beyond the range above, and
combining marks (a precomposed `é` is one glyph and is kept; `e` + U+0301 is not). The SPA is
English-only at 1.0 (`.claude/rules/web.md`) and P99 character and guild names are ASCII.

Two glyphs the mockups use are **not in Inter 4.1 at all** — `∩` U+2229 (one admin-console label) and
`▸` U+25B8 (three first-run rows). They fall back to system-ui today with the full face and behave
identically after the subset; this change did not cause them and cannot fix them.

**OpenType features.** `calt,ccmp,kern,locl,mark,mkmk` — the Latin shaping core a browser turns on by
itself — plus **`tnum`**, which pyftsubset's default list does *not* include. Dropping `tnum` is the
expensive mistake available here: the mockups set `font-variant-numeric: tabular-nums` 221 times
across the four surfaces — every balance, every points column, every countdown — and without the
feature those figures stop aligning in exactly the tables a DKP site exists to show. Inter's other
features (the
stylistic sets `ss01`–`ss08`, character variants `cv01`–`cv14`, `salt`, `aalt`, `dlig`, `case`,
`cpsp`, and the rest of the `font-variant-numeric` set) are reachable only from
`font-feature-settings`, which nothing in `web/src` uses; keeping the numeric set alone costs 9 KB
across the pair for CSS nobody has written. `TestWebFonts_SubsetKeepsEveryFeatureTheStylesheetsAsk`
turns that bet into a build failure the moment a stylesheet asks for one of them.

**Name records.** Every English-language record, not pyftsubset's default 0–6. The extra ~240 bytes
carry the designer, the vendor, the trademark statement, and name IDs 13 and 14 — the OFL notice and
its URL — so a derived font states its own licence when a font inspector opens it.

## Licence

SIL Open Font License 1.1 — permissive, embeddable, redistributable and Apache-2.0-compatible.

**Subsetting is a Modified Version, and the OFL permits it.** Inter declares **no Reserved Font
Name**: its notice is `Copyright (c) 2016 The Inter Project Authors`, with no "with Reserved Font
Name" clause, so OFL §3's rename requirement does not apply and these faces may keep the family name
`Inter`. What the licence does require is that the copyright notice and the licence text travel with
the Font Software — which is what `OFL.txt` beside the binaries is for, plus name IDs 0, 13 and 14
retained *inside* each face.

`NOTICE` records the vendoring and `THIRD_PARTY_NOTICES.txt` reproduces the OFL text in the file the
release archives attach — the font is embedded in the binary by `internal/ui`, so the binary's
notices file is where the obligation actually lands. The licence gate (`internal/licence`) cannot
see any of this: it reads the **Go** module graph (`go list -deps ./cmd/dkp`), and a font is not a module.
`test/repo/web_fonts_test.go` is the gate that does see it.

## Regenerating, re-vendoring, or adding a face

```sh
make subset-fonts     # re-cut the subset from upstream and rewrite this directory
make verify-fonts     # regenerate into a temp dir and prove the committed bytes match
```

Both need network. `DKP_INTER_ZIP=/path/to/Inter-4.1.zip` reuses an already-downloaded archive.

To change the subset — a new codepoint, a new OpenType feature — edit the pinned constant in
`scripts/subset-fonts.sh`, run `make subset-fonts`, and paste the printed table into this file. The
`make check` gates fail until the table, the stylesheet's `unicode-range` and the bytes agree.

To move to a new Inter release, update `INTER_VERSION`, `ARCHIVE_SHA256` and the two input hashes in
the script (`shasum -a 256` on the archive and on the extracted faces), then regenerate. To add a
weight, add it to `FACES` in the script, add the `@font-face` block to `web/src/styles/fonts.css`,
and run `make third-party-notices` — a face with no `@font-face` block is dead weight in the binary,
and `test/repo/web_fonts_test.go` fails on it. A third weight is also a design change: §3 fixes body
at 400 and headings at 500.
