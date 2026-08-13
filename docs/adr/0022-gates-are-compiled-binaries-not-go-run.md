# ADR-0022 — A Go-implemented gate is compiled and run, never `go run`

**Status:** accepted · **Date:** 2026-08-12 · **Deciders:** owner

## Context and problem statement

Six repository gates moved from shell to Go (issues #123, #128, #129), and every one of them was
invoked the way the generators already were: `go run ./internal/...`. For a generator that is fine.
For a **gate** — a program whose whole output is a verdict a person reads while their PR is red —
`go run` does two things that matter (issue #142):

1. **It collapses the exit code.** Whatever the child exits with, `go run` exits 1. The shipped-lock
   command distinguishes 2 (usage) from 1 (finding), asserted by
   `TestRun_UnknownArguments_AreAUsageError`, and every caller could only see "non-zero". A release
   log asking "did the gate fail, or did the job mistype the command?" had no way to tell.
2. **It appends `exit status 1` to stderr**, inside the failure block, after the gate's own
   explanation. It reads like a crash in the tool rather than a finding about the tree.

A third cost was structural: `make lint-repo` ran `go run` on the engine, and the engine ran `go run`
again for `MIG003` — one Go build nested inside another, thirty times over in `test/repo`.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Keep `go run`, document both behaviours | Free | Documents a defect instead of fixing it; the exit-code distinction stays unreachable |
| B — Build the gate binaries into `$(GOTOOLS_BIN)` during `make setup` | Fastest per invocation | One more thing to keep fresh, and a stale binary is a gate reporting on code that is not in the tree |
| C — `go build -o` a temp binary at invocation, then run it | Real exit codes, no noise, no toolchain state; Go's build cache makes the rebuild about as cheap as the link `go run` already did | ~1 s on a cold cache, per invocation |
| D — Import the gate instead of spawning it, where the gate is in this module | No process at all; a typed error instead of captured stdout | Only available when the logic is an importable package |

## Decision outcome

**Chosen: D where the code is ours, C at the entry point.**

- `scripts/repo-gates.sh` **builds** `./internal/repogate/cmd/repogate` into a temp directory and runs
  it, propagating the child's status. A build failure is `GATE000` with its own message, because a
  gate that cannot compile must not report green.
- `MIG003` no longer spawns anything. `internal/migrate/shippedlock`'s logic became
  `internal/migrate/lockmanifest` (issue #173), and the engine calls `Verify` in process. The typed
  `ErrDisagrees` is what option D buys: the rule can now say "the manifest disagrees" and "the
  manifest could not be checked" as two different failures, which is the distinction `go run` erased.

- The **Makefile** call sites go through one `go_gate` macro that does the same thing — build into a
  temp directory, run the binary, propagate the status — with a build failure exiting 2 rather than
  1, because a gate that could not compile checked nothing. Four recipes use it: `licence-gate`,
  `verify-spec`, `shipped-lock-seal` and `release-shipped-lock` (issue #180).

`TestRepoGates_ScriptDelegatesToTheEngine` fails on a `go run` anywhere in the gate script, and
`TestCheck_NoRequiredGate_RunsThroughGoRun` fails on one in the recipe of any target a blocking CI
job runs (plus the two release-path recipes, which no PR job runs), so the decision is pinned rather
than remembered. Neither test touches a generator: `go run` is right for `scripts/gen-*.sh` and
`make mockup-site`, which is this ADR's first line.

### Consequences

- Good, because a red `lint / repo` now shows the gate's own words and nothing else.
- Good, because "the gates could not run" (exit 2) and "a rule fired" (exit 1) are separable by any
  caller, which is what a release job needs from a gate.
- Good, because one build replaces two on every gate run, and `test/repo` spawns about thirty.
- **Bad, because the entry point costs a `go build` on a cold cache** — around a second, paid on the
  first run after a toolchain or dependency change.
- **Bad, because `make` itself exits 2 on any failed recipe**, so the child's 1-versus-2 distinction
  survives to a caller of the *binary* and not to a caller of the *target*. What a release log gains
  either way is the gate's own words as the last thing on stderr, which was the louder half of the
  complaint in issue #142.

### Reversal cost

Minutes. The script's build-and-run block is six lines and `go run` is what it replaced. Reversing
option D is a day and would reinstate the nested build, so the two halves reverse independently.
