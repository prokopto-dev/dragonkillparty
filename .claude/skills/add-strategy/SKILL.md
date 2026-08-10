---
name: add-strategy
description: Add a point-system strategy to internal/strategy. Use when a guild's DKP model cannot be expressed by configuring an existing strategy — zero-sum, tick, fixed price, attendance-weighted, decay, cap, start points, auctions, loot council, roll, relative bid.
argument-hint: "[strategy_id]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(make gen), Bash(make test-unit), Bash(make test), Bash(make check)
---

# Add a point strategy

Strategies are **pure functions that propose ledger batches**. The ledger and its invariants are not
pluggable: the guild-configurable part can only emit a proposal, and the trusted part validates it
against declared invariants before committing.

A subtly wrong change here is the most destructive class of defect in the product — a silent rounding
change reallocates points across the whole guild and nobody notices for weeks.

**Read first:** `docs/design/00-canonical-conventions.md` §10, `docs/concepts/invariants.md`, and an
existing strategy of the nearest shape (`internal/strategy/zero_sum.go` for allocation,
`internal/strategy/decay_percent.go` for cadence, `internal/strategy/tick.go` for attendance).

---

## Steps

### 1. Confirm it is a strategy, not a config knob

Read the config-knob table for the nearest existing strategy first. `cap` and `start_points` ship in
1.0; `epgp` and `suicide_kings` are held until a **named guild asks in an issue**, not a maintainer's
guess. Most "new system" requests are an existing strategy with different knobs.

If it *is* new: one file plus one test file, `internal/strategy/<id>.go`. This shape exists so
strategies can be written in parallel with near-zero conflict surface.

### 2. Define the config JSON Schema

`config_schema` renders the pool settings form and validates the config. Every knob a guild can turn
lives here — there is no second place. Knob names are lowercase `snake_case` and match the docs page.

### 3. Implement `PointStrategy`

```go
type PointStrategy interface {
    ID() string                  // "zero_sum" — lowercase snake_case, permanent
    Version() string             // semver; snapshotted into every batch it produces
    BalanceKinds() []string      // ["points"] | ["ep","gp"] | ["sk_position"]
    ConfigSchema() jsonschema.Schema

    PlanAttendance(ctx Ctx, ev AttendanceEvent) (BatchProposal, error)
    PlanAward(ctx Ctx, ev AwardEvent) (BatchProposal, error)
    PlanAdjustment(ctx Ctx, ev AdjustmentEvent) (BatchProposal, error)
    PlanDecay(ctx Ctx, run DecayRun) (BatchProposal, error)
    PlanReversal(ctx Ctx, b LedgerBatch) (BatchProposal, error)

    Spendable(ctx Ctx, acct AccountRef) (core.Centipoints, error)
    Priority(ctx Ctx, acct AccountRef) (Priority, error)
    PriceHint(ctx Ctx, item ItemRef) (*core.Centipoints, error)
    ValidateBid(ctx Ctx, acct AccountRef, bid Bid) error
    SettleAuction(ctx Ctx, s Session, bids []Bid) (Resolution, error)

    Invariants() []Invariant
}
```

**Purity is a law, enforced by an import-graph test:**

| Banned | Use instead |
|---|---|
| `internal/store` | `ctx` — a read-only façade over pool config, balances, attendance, roster, items |
| `time.Now` | the injected `Clock` |
| `math/rand` | the injected seeded `Rng` — **the seed is persisted onto the batch**, so replays are byte-identical |
| `float32`, `float64` | `core.Centipoints` (int64). A `golangci-lint` rule bans the types outright in this package. |

`Ctx.Balance(account, kind, as_of_seq)` is positional, never temporal. A backdated `effective_at`
must not change what a past balance *was*.

### 4. Get the arithmetic right

| Rule | Mechanism |
|---|---|
| Integer only, end to end | `NoFloat`, always on, plus the lint ban |
| Zero-sum splits use **largest-remainder allocation** with a deterministic tiebreak on `account_id` | `LargestRemainderSumsToDebit` |
| Credits sum to **exactly** the debit | `SumZero(kind, scope=batch)` |
| Decay is **posted** as explicit batches, never computed inside `Spendable()` | Idempotency key `(pool_id, cadence_period)` + `UNIQUE(pool_id, cadence_period)` on `decay_run` |
| Percentages resolve against a **frozen** balance snapshot at `seq_at_open` | The session records `seq_at_open`; resolving against live balances lets a concurrent decay run rewrite everyone's bid mid-auction |

Rounding each credit independently mints or destroys points. This is the classic bug and it is why
the allocator is shared, not re-implemented per strategy.

### 5. Declare `Invariants()` — and make them constrain something

**A strategy that declares nothing is a red flag and will be rejected in review.** The declared set
is what the ledger service checks before committing; an empty set means the ledger trusts you.

| Invariant | Meaning |
|---|---|
| `SumZero(kind, scope=batch)` | Entries sum to exactly 0 |
| `NonNegative(kind, floor)` | Balance may not drop below floor after this batch |
| `MonotoneNonDecreasing(kind)` | A kind never decreases except via reversal |
| `Permutation(kind)` | Positions remain a bijection over the eligible list |
| `RatioPreserved(a, b, tol)` | A decay batch scales two kinds identically |
| `Conserved(kind, total)` | Total across all accounts unchanged |
| `LargestRemainderSumsToDebit` | Allocation is exact |
| `NoFloat` | Always on; not yours to waive |

Pool-level invariants no strategy may waive: `NoFloat`, `EntriesReferenceLiveAccounts`,
`BatchNonEmpty`, `SeqMonotonic`.

### 6. Implement `PlanReversal` — reversal is not always negation

The default is negating every entry. That is wrong for any strategy with positional or ratio state:
reversing a Suicide Kings award must restore the *prior permutation*, not add a negative position.
This is where the interface earns its shape.

The reversal property must hold: **apply then reverse leaves every balance and every positional state
exactly as it was.**

`BatchProposal.Negated` drops `NonNegative` from the inherited set and keeps the conservation rules.
Do not add the floor back. A reversal is the only repair primitive an append-only ledger has, so a
floor on it does not prevent a debt — it prevents the correction, and leaves the original mistake
permanently unfixable. Declare `NonNegative` in `PlanAward`, where it refuses an overdraft on a
**spend** before anything is written.

### 7. Write property tests, not only example tests

Example tests prove the case you thought of. Properties prove the cases you did not.

| Property | Over |
|---|---|
| Every declared invariant holds | Randomised award/adjustment/decay/reversal sequences |
| Reversal is exactly invertible | Random batches, all balance kinds |
| Determinism | Same inputs + same seed → byte-identical `BatchProposal` |
| No float appears anywhere in the proposal | All planners |

Use **`testing/quick`**, not `rapid` — rapid is an unapproved dependency, and inside
`internal/strategy` importing `math/rand` trips gate PURE002 anyway, so draw cases from the injected
seeded `Rng` and print the base seed. `internal/strategy/fixed_price_test.go` is the worked example;
copy it. Budget: **200 checks per PR, 20 000 nightly** (`make test-property`). Coverage floor for
`internal/strategy` is **95%**, enforced by `make test-coverage-floor` as a required CI job, and it
is a one-way ratchet.

Add a golden `BatchProposal` file per planner, compared as canonical JSON so the *whole* proposal is
asserted rather than three cherry-picked fields. Goldens live under `test/golden/` and are
CODEOWNERS-protected; `-update` is refused when `CI=true`.

### 8. Register it

Add the strategy to the catalogue so `make gen` writes it into the pool-settings form, the
`strategy_id` `CHECK` constraint and the OpenAPI enum. Never hand-edit those three copies — a test
asserts they agree.

### 9. Docs

One page under `docs/concepts/strategies.md` with a **worked numeric example** in centipoints:
starting balances, the event, the resulting entries, the closing balances. Officers argue about this
maths; a worked example is the artefact that settles it.

### 10. `make check`

---

## Stop and ask if

- **The strategy needs to read the database.** It cannot. If `Ctx` lacks the fact you need, the
  question is what to add to `Ctx`, not whether to import `internal/store`.
- **The strategy needs its own randomness or its own clock.** Both are injected; the seed is
  persisted. If you need a second source, the design is wrong.
- **You cannot express a correction as a reversal.** An append-only ledger has no other repair
  primitive.
- **The guild's decay rule is unclear.** Do not guess. A wrong decay rule silently redistributes
  points across the entire roster.
- **It is `epgp` or a Suicide Kings variant.** Those are conditional on a named pilot guild; the SK
  `PlanReversal` is the hardest single piece of ledger code in the spec.
