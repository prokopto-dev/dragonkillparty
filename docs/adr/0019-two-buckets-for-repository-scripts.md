# ADR-0019 — Two buckets for `scripts/`: glue stays bash, logic moves to Go

**Status:** accepted · **Date:** 2026-08-12 · **Deciders:** owner

## Context and problem statement

Phase 0 audited all 33 files in `scripts/` — about 4,800 lines of bash and Python holding the
architectural gates, the code generators and the release train ([#125](https://github.com/prokopto-dev/dragonkillparty/issues/125)).
Three defects had already shipped from them: a gate helper whose pattern matched the wrong prefix and
silently stripped nothing for months, an `awk` program handed its filename so a padded word count
re-split (#84, #111), and a Python version floor reported as OpenAPI *spec* drift (#83). Every one is
a failure of a language chosen by habit rather than by what the script does. Without a rule, the
language of each script is a fresh argument, and "why is `verify-spec` Go but `release-sign.sh` still
bash?" gets asked once per script for the life of the project.

## Considered options

| Option | For | Against |
|---|---|---|
| A — keep everything shell and Python | One convention; no toolchain in the lint job; a forty-line wrapper reads as what it runs | Logic is observable only through a subprocess, so a branch cannot have a test; a pattern cannot tell a comment from a call; the #84/#111 class stays open |
| B — move everything to Go | One language, everything unit-testable | A Go program that shells out to cosign, buildx or vite is longer than the wrapper it replaces and closes no defect class; thousands of lines rewritten for nothing |
| C — keep everything, add `shellcheck` and `shfmt` | Catches the #84/#111 shapes at near-zero cost, no rewrite | A linter finds the bug shapes it knows; it does not make a licence classifier or a manifest parser testable |
| D — two buckets, decided by what the script does | Each script pays only for what it is; the fragile half becomes ordinary Go with ordinary tests | Two conventions in one directory, and the entry point can stay a `.sh` even when the logic is Go |

## Decision outcome

**Chosen: D**, and C as well — the buckets are not an argument against a shell linter ([#122](https://github.com/prokopto-dev/dragonkillparty/issues/122)).

The test is what the script *does*:

- **Thin glue around a real CLI** — cosign, syft, docker/buildx, atlas, sqlc, pnpm, vite, playwright,
  eslint, `gh`, curl, pyftsubset — **stays bash.** The wrapper's job is to assemble arguments and
  propagate an exit code, and the tool it drives owns the behaviour worth testing.
- **A script that parses, rewrites or computes** — **moves to Go**: stdlib only, under `internal/`,
  never linked into `dkp`, with every negative fixture in `test/repo/` carried over unedited so no
  gate is weakened by its own move.

Landed under this rule: the mockup site build ([ADR-0017](0017-mockup-build-on-a-real-html5-parser.md),
`internal/mockup`), the OpenAPI spec gate (`internal/specgate`), the migration rewriter
(`internal/migrate/migrationfmt`), the shipped-migration manifest (`internal/migrate/shippedlock`),
the licence classifier ([ADR-0016](0016-bespoke-licence-classifier.md), `internal/licence`) and the
repository gates ([ADR-0018](0018-repo-gates-as-a-go-engine.md), `internal/repogate`). What remains in
`scripts/` is 28 shell files and one Python file, `check-links.py`, and all of it is glue.

**Enforced by:** review, and deliberately nothing else. The template is right that a rule without a
mechanism is a wish, so this one is stated as what it is rather than dressed as a gate: no test can
tell whether a new eighty-line bash script is glue or a parser. The mechanism that does exist is the
one that matters at the boundary — each moved gate keeps its negative fixtures in `test/repo/`, so a
move that quietly changed behaviour goes red.

### Consequences

- Good, because the fragile half is now ordinary Go: a classifier or a manifest parser is unit-tested
  as a function instead of through a 250 ms subprocess with `grep` over its stdout.
- Good, because the glue stayed short. The audit's ~4,800 lines are now about 2,800 in `scripts/`,
  and none of the remainder is doing anything a pattern can get wrong.
- Good, because the moves were behaviour-preserving by construction — the fixtures came across
  unedited, which is the property that made six rewrites landable in one phase.
- **Bad, because the gates now need the Go toolchain, where bash and `python3` are on every supported
  platform already.** `GATE000` makes a missing Go a hard failure. That is fine here — Go is a
  prerequisite for this repository — and it is precisely the trade another project would make
  differently.
- **Bad, because `go run` is not a great way to invoke a gate.** It collapses the child's exit code
  and prints `exit status 1` into a failure block a human is reading, and it compiles on every
  invocation ([#142](https://github.com/prokopto-dev/dragonkillparty/issues/142) is open on it).
- **Bad, because the file you open is not always the file that decides.** `scripts/repo-gates.sh`
  survives as a shim over `internal/repogate`, so a reader following the Makefile lands on a script
  that no longer holds the rule.
- **Bad, because tooling in `internal/` has to be kept out of the shipped binary**, which is a rule
  per package (stdlib only, plus a package-graph assertion) rather than a rule per repository.

### Reversal cost

Per script, and cheap: a day each. The buckets are a naming of where code lives, not a coupling —
each Go tool is a package with a `cmd` entry point and a suite that would carry back to a subprocess
test unchanged. The cost that does not reverse is the six rewrites themselves, which is why the rule
exists rather than a case-by-case argument.
