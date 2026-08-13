# ADR-0021 — The gate catalogue is HCL data, and `ENUM001` parses the schema

**Status:** accepted · **Date:** 2026-08-12 · **Deciders:** owner
**Amends:** [ADR-0018](0018-repo-gates-as-a-go-engine.md) — its option B, in part. Everything else in
that record stands: the engine is Go, the Go-syntax laws are `go/parser` analyzers, and
`scripts/repo-gates.sh` is the entry point.

## Context and problem statement

ADR-0018 chose a Go engine with a **typed Go rule catalogue**, and rejected HCL (its option B) for
two reasons: `hashicorp/hcl/v2` was a dependency nobody had decided about, and "a parse failure on a
half-written file turns a gate into a bypass". It recorded the cost it accepted — "rules are Go
source, so a non-Go contributor reads code to learn what is banned".

Two things then happened. The owner approved the dependency (2026-08-11). And `ENUM001` — the rule
that reads `db/schema.hcl` for hand-written string-enum CHECKs — collected **five bypasses in four
review rounds**, every one of them an input shape its hand-written scanner did not model: a heredoc,
a wrapped list, a comment between the keyword and its parenthesis, SQLite's double-quoted literals, a
lowercase keyword. Each was fixed. The rate of discovery was the finding.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Keep the typed Go catalogue and the hand-written schema scanner | No dependency; already works | The next reviewer finds the sixth bypass; a rule is Go source a non-Go reader cannot audit |
| B — HCL for the catalogue only | Rules become data; no change to how any rule decides | `ENUM001` keeps its scanner and its bypass rate |
| C — HCL for both: the catalogue is data, and `ENUM001` reads the schema with `hclsyntax` | The parser hands back the expression's **evaluated** string, so heredocs, wrapping and escapes stop being shapes to model | A schema that does not parse has to mean something |

## Decision outcome

**Chosen: C.** The dependency is one module (MPL-2.0, already allowed by the licence gate) and it is
tooling-only — `TestRepoGates_IsNotLinkedIntoTheBinary` keeps `dkp` free of it.

- **`internal/repogate/rules.hcl`** declares every config-shaped rule: id, tree, globs, pattern,
  allowlist, comment openers. It is `go:embed`ed, so the tree under inspection cannot supply it — a
  gate engine that read its rules out of a tainted fixture would be one a tainted tree could disarm.
- **A catalogue that does not decode is `GATE000`**, never an empty catalogue. That is the whole
  fail-closed property: these rules are the money rules, the design tokens, law 4's text half, the
  supply-chain pins and the AGPL firewall, and a decoder answering with an empty slice would disable
  all of them while printing `repo gates passed`.
- **`ENUM001` parses `db/schema.hcl`**, and **a schema that does not parse is a violation**. That is
  the answer to ADR-0018's objection: the rule does not report a pass either. A merge-conflict marker
  in that file is a tree where `make gen`, Atlas and sqlc are already failing.

Rules that need behaviour are still Go and cannot be data: the syntax laws, `MIG001`/`MIG002` (which
read a migration's structure through `internal/migrate/sqlscan`), `MIG003` (hashes and git history)
and `ADR001` (a diff).

### Consequences

- Good, because what is banned is now readable without reading Go, with the reason beside the rule.
- Good, because three of `ENUM001`'s five historical bypasses stop being expressible.
- Good, because `hclsyntax` reads the schema the way Atlas does, rather than the way a scanner guesses.
- **Bad, because the gate engine now has a third-party dependency**, on the one job that had none.
  `make lint-repo` needs the module cache warm; a cold, offline checkout cannot run the gates.
- **Bad, because a pattern in a heredoc has no compiler checking it.** A typo is a decode failure
  rather than a build failure, so it is caught one step later — by `GATE000`, and by the inventory
  test that asserts every rule still covers every tree it is documented to cover.
- **Bad, because a schema mid-edit now fails `lint / repo` on `ENUM001`** rather than being scanned
  as text. It fails honestly, and it names the parse error.

### Reversal cost

Half a day. `decodeRules` produces the same `[]textRule` the Go table did, so returning to Go is a
transcription with the fixtures already in place; `ENUM001`'s scanner is in the history of
`internal/repogate/enum.go` and its twelve fixture cases are unchanged and still the specification.
