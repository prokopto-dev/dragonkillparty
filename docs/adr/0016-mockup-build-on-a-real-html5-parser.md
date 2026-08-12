# ADR-0016 — Parse the mockups with an HTML5 parser, not with regular expressions

**Status:** accepted · **Date:** 2026-08-11 · **Deciders:** owner

## Context and problem statement

The five vendored `.dc.html` design mockups are published as a static site, and publishing means
rewriting them: repoint the runtime, rewrite the stylesheet path, strip the script type that stops
the authored logic executing, and move each `<sc-for>`/`<sc-if>` onto the element it repeats. That
last one exists because the HTML5 tree construction algorithm *foster-parents* an unknown element out
of a `<table>`, leaving it empty — which silently emptied 37 of the mockups' tables. All of it is
HTML parsing, and `scripts/dc-publish.py` did it with regular expressions and a hand-written
quote-state scanner whose own comments documented two near-misses. There is no HTML parser in Go's
standard library, so correcting this needs a dependency decision.

## Considered options

| Option | For | Against |
|---|---|---|
| A — keep the Python regex parser | No new dependency; it works today on these five files | It reimplements a tokenizer, badly. Its logic was never unit-testable — only its stdout was grepped — and the failures it produces are silent: a binding renders empty, a table renders headers and no body |
| B — `golang.org/x/net/html` | The canonical Go HTML5 implementation, BSD-3-Clause, Go-team maintained, already in the module graph via huma and goose. Typed, unit-testable, and the tree builder can *verify* the result | A direct dependency where there was none, on a module the shipped binary does not need |
| C — hand-write an HTML5 tokenizer in Go | No dependency; full control | Same defect as A with more code. A spec-conformant tokenizer is roughly 2,000 lines and its bugs are exactly the ones being fixed |
| D — shell out to a Node/Python HTML parser | Reuses a mature parser without a Go dependency | Adds a *runtime* toolchain requirement to a build that otherwise needs only Go, and moves the fragility from parsing to subprocess plumbing |

## Decision outcome

**Chosen: B.** The job is parsing, so use a parser; the only real question was which, and there is
one obvious answer in Go. `internal/mockup` replaces both scripts, and `internal/mockup/sitegen` is
the command `make mockup-site` runs — under `internal/` rather than `cmd/dkp` for the reason
`internal/ledger/enumgen` gives, that `cmd/dkp` is the product binary and an officer never publishes
a design reference.

The dependency is deliberately narrow. `golang.org/x/net/html` is imported by `internal/mockup` and
nothing else; `go list -deps ./cmd/dkp` does not reach it, so `THIRD_PARTY_NOTICES.txt` is unchanged
and the shipped binary does not link it. `scripts/licence-gate.sh` already allows BSD-3-Clause, and
huma and goose both require this exact version, so it selects nothing new.

The two halves of the parser are used for opposite jobs, and that split is the substance of the
decision. **`html.Tokenizer` does the rewrite**, because the tree builder foster-parents `<sc-for>`
out of a table — the bug being fixed cannot also be the tool that fixes it. **`html.Parse` does the
verification**: each finished page is handed to the same algorithm a browser runs and refused if any
of the mockups' own elements came back with fewer element children than the markup gave it. Counting
rows does not work — foster parenting does not delete them, it empties the element that wrapped them,
and `mockup-runtime.js` repeats a directive's *children*.

**Enforcement:** `MOCK001`–`MOCK004` and the foster-parenting refusal live in `internal/mockup`;
`test/repo/mockup_gates_test.go` drives each against a negative fixture, and
`TestMockupSite_MakefileTarget_RunsTheGoCommand` pins the wiring so the target cannot drift back to a
script.

### Consequences

- Good, because the gates' logic is now unit-testable rather than black-box-grepped, and every one
  has a negative fixture proving it fires.
- Good, because the verification is stronger than what it replaces: the old check was a proxy for
  foster parenting in the one case anyone had thought of, and this one catches the failure itself,
  including through `<x-import>` and `<helmet>`.
- Good, because the mockup build now has a PR-time signal. It previously ran only on `main`.
- **Bad, because** it is a direct dependency this repository did not have, on a module the product
  does not need. Every dependency is a supply-chain surface and a thing to keep current.
- **Bad, because** publishing a design reference now needs a Go toolchain. `.github/workflows/pages.yml`
  gained a `setup-toolchain` step it did not have.
- **Bad, because** the lift still only knows `<sc-for>` and `<sc-if>`. The new gate can refuse a page
  for an `<x-import>` inside a table with no remedy available, since the vendored files may not be
  edited — issue #147.

### Reversal cost

A day. `internal/mockup` is ~1,100 lines with no callers outside `make mockup-site` and
`pages.yml`, imports nothing else from this repository, and produces byte-identical output to the
scripts it replaced — so reverting is restoring two files from git history and dropping one
`require` line. Nothing depends on it at runtime.
