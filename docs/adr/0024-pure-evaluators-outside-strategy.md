# ADR-0024 — Pure rule evaluators live outside `internal/strategy` and carry their own purity proof

**Status:** accepted · **Date:** 2026-08-13 · **Deciders:** owner

## Context and problem statement

ROADMAP Phase 1 item 13 needs a main-swap rule evaluator: an ordered stack of rules over a base cost
producing a quote that is reproducible, so that Phase 7 can hold one at request time and re-price it
to the same number ([`docs/design/10-ui-decisions.md`](../design/10-ui-decisions.md) §4). It is not a
point strategy — it proposes no batch, moves no balance kind, and has no use for eleven of
`PointStrategy`'s twelve methods — yet it has every property law 3 exists to protect: no store, no
wall clock, no float. Law 3 names one tree, and all four of its mechanisms (`PURE001`, `PURE002`,
`CLOCK002`, and golangci's float ban) are scoped to `internal/strategy`. So the open question is not
only where the code goes; it is what enforces purity for a pure package that is not a strategy, and
the answer is a precedent for the evaluators Phases 6 to 8 will add beside it.

## Considered options

| Option | For | Against |
|---|---|---|
| A — implement it as a strategy in `internal/strategy` | Purity is enforced by four gates that already exist and that everyone reads | Eleven `ErrUnsupported` methods to satisfy an interface about proposing batches. It would be the only "strategy" a pool cannot be configured to run, and `TestArch_Strategy_*` would then be guarding a package holding something that is not one |
| B — **a new top-level `internal/swap`, purity proved by a package-local `arch_test.go`** | The package is exactly its subject: rules in, quote out. The proof travels with the code it constrains and fails in the package a change was made in | Law 3's enforcement is now in two places, and the second one is a file somebody has to remember to copy |
| C — fold it into Phase 7's guild-ops, beside the ledger posting | One package owns main swaps end to end | The posting reads and writes the store, so purity becomes unenforceable in the tree that holds the pricing — and pricing is the half that has to be reproducible |
| D — B, but widen the repo gates' `tree` fields to cover `internal/swap` | One catalogue, read by everyone, gates every pure tree | A rule named for law 3 would then cover trees law 3 does not name; and a per-tree list is a list somebody must extend for each new package, which is the same forgetting as B with a longer feedback loop |

## Decision outcome

**Chosen: B, with D declined for now rather than rejected.** A pure evaluator is its own package, and
it carries its own proof. The rule this sets, for the evaluators still to come:

- **The package holds rules and arithmetic only.** Every fact arrives in the request value; the clock
  is injected and is consulted only when the caller names no instant. Money stays `core.Centipoints`
  and ratios stay integer basis points.
- **`arch_test.go` is the gate, not a second opinion.** In `internal/swap` it bans `internal/store`
  transitively (with `internal/ledger` as a positive control, so a broken walk cannot pass
  vacuously), `time` and `math/rand` as direct imports of the shipped files, `clock.System` resolved
  through the import's local name, and `float32`/`float64` everywhere including the tests. A
  fabricated tainted tree in `t.TempDir()` requires all four to go red.
- **Test files may import `time`.** They cannot link into the binary, and `time.Date` is how a
  readable year boundary gets written into a table. `CLOCK001` is repo-wide and reads `_test.go`, so
  the case that would actually matter — a test reading the wall clock — is still caught.

D is the cheap upgrade and the trigger is named: **when a third pure package lands**, move these
checks into `internal/repogate` as a tree list, and delete the duplicated auditors. Two copies is a
tolerable price for keeping a rule where its subject is; three is a pattern that should be data.

### Consequences

- Good, because the evaluator's purity is a compile-and-test property of the package that has it,
  and a reviewer reads the proof next to the code rather than in a catalogue two directories away.
- Good, because `internal/strategy` keeps meaning one thing — a configurable point system — and
  `PointStrategy` does not grow a member that proposes nothing.
- Good, because the float ban reaches a package that quotes prices, which golangci's path-scoped
  exclusion does not.
- **Bad, because law 3's enforcement is now in two places.** `make lint-repo` alone no longer proves
  every pure tree is pure; the package's own suite is half the answer, and somebody adding
  `internal/<next>` must copy a file nothing tells them to copy.
- **Bad, because roughly 150 lines of import-graph auditor now exist twice**, in
  `internal/strategy/arch_test.go` and `internal/swap/arch_test.go`, and a fix to one will not reach
  the other.
- **Bad, because a package-local gate is easier to weaken than a repo gate.** Deleting a test file is
  a smaller-looking diff than deleting a rule from `rules.hcl`, and only review catches it.

### Reversal cost

Small, in either direction. Folding the evaluator back into another package is a `git mv` and an
import rewrite over ~1,300 lines with no persistence and no wire format to migrate; adopting option D
is a tree list in `internal/repogate` plus deleting the duplicated auditors. Nothing here is written
to a database, so no data decision follows from it.
