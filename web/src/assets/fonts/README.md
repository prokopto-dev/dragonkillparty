# Vendored fonts

The one place third-party licensed content is committed into this repository. Everything here is
byte-for-byte upstream: nothing in this directory was regenerated, re-compressed, subset or renamed,
so the checksums below can be re-derived from the upstream artefact by anyone who doubts them.

## Inter 4.1 — SIL Open Font License 1.1

`docs/design/09-frontend-and-design-system.md` §3 requires Inter at 400 (body) and 500 (headings),
**self-hosted**. The binary serves the SPA offline; a render-blocking `fonts.googleapis.com` request
contradicts that, and `mockups/nocturne/styles.css`'s `@import url('https://fonts.googleapis.com/…')`
is the thing that must not be copied. `scripts/repo-gates.sh` WEB003 is the gate that keeps it out.

| File | Bytes | SHA-256 |
|---|---|---|
| `Inter-Regular.woff2` | 111268 | `e06f6b1bc553aaea4e4668023ed0ab0a147129c3107f511bc7d03d361b0ae085` |
| `Inter-Medium.woff2` | 114348 | `0ff3e94614e1493eb556314fd247ae6c4a85a7783b4cc86be539940cf83f2a48` |
| `OFL.txt` | 4380 | `262481e844521b326f5ecd053e59b98c8b2da78c8ee1bdbb6e8174305e54935a` |

**Provenance.** <https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip>, released
2024-11-16, SHA-256 `9883fdd4a49d4fb66bd8177ba6625ef9a64aa45899767dde3d36aa425756b11e`. The two
faces are that archive's `web/Inter-Regular.woff2` and `web/Inter-Medium.woff2`; `OFL.txt` is its
top-level `LICENSE.txt`, unmodified.

**Licence.** SIL Open Font License 1.1 — permissive, embeddable, redistributable and
Apache-2.0-compatible. It requires the copyright notice and the licence text to travel with the
font, which is what `OFL.txt` beside the binaries is for. `NOTICE` records the vendoring and
`THIRD_PARTY_NOTICES.txt` reproduces the OFL text in the file the release archives attach — the
font is embedded in the binary by `internal/ui`, so the binary's notices file is where the
obligation actually lands. `scripts/licence-gate.sh` cannot see any of this: it reads the **Go**
module graph (`go list -deps ./cmd/dkp`), and a font is not a module. `test/repo/web_fonts_test.go`
is the gate that does see it.

## Re-vendoring, or adding a face

```sh
curl -fsSLO https://github.com/rsms/inter/releases/download/v4.1/Inter-4.1.zip
shasum -a 256 Inter-4.1.zip          # must match the archive hash above
unzip -j Inter-4.1.zip 'web/Inter-Regular.woff2' 'web/Inter-Medium.woff2' -d web/src/assets/fonts/
unzip -p Inter-4.1.zip LICENSE.txt > web/src/assets/fonts/OFL.txt
```

Then update the table above, add the `@font-face` block to `web/src/styles/fonts.css`, and run
`make third-party-notices`. A face with no `@font-face` block is dead weight in the binary, and
`test/repo/web_fonts_test.go` fails on it.

**These are the full faces, not a Latin subset.** Upstream ships no subset, and subsetting here
would mean committing a binary this repository generated rather than one it can point at — the
checksums above would stop meaning anything without a reproducible subsetting step in CI. The cost
is ~225 KB of font instead of ~60 KB, paid once per visitor: the faces are content-hashed by Vite
and served `Cache-Control: public, max-age=31536000, immutable` by `internal/ui`, they are not
render-blocking (`font-display: swap`), and they do not count against `web/bundle-budget.json`,
which measures entry JS. Issue #47 tracks doing the subset properly.
