---
paths: ["internal/ledger/**", "internal/strategy/**"]
description: Batch and entry shapes, seq allocation, reversal semantics, the Invariant vocabulary, largest-remainder allocation, and the purity rules for strategies.
---

# Ledger and strategy

Highest blast radius in the repo. A plausible-looking wrong change here reallocates points across
the whole guild and nobody notices for weeks. Use plan mode.

The division: **`internal/strategy` proposes, `internal/ledger` validates and commits.** A strategy
is a pure function that returns a `BatchProposal`; it never touches the database and never decides
whether the proposal is legal. That split is what lets a guild configure its own rules without being
able to corrupt the ledger.

## The two tables

```
ledger_batch(id ULID PK, pool_id, seq, kind, strategy_id, strategy_version,
             config_snapshot_json, rng_seed, source, source_ref, actor_user_id, actor_token_id,
             reason, reverses_batch_id, effective_at, recorded_at, effective_day,
             idempotency_key, entry_count, net_amount_cp, prev_hash, hash)

ledger_entry(id ULID PK, batch_id, pool_id, seq, account_id, character_id, balance_kind,
             amount_cp, item_id, item_award_id, raid_id, tick_id, metadata_json)
```

| Field | Meaning you will get wrong otherwise |
|---|---|
| `seq` | **Per pool**, on the batch. A balance is defined *as of a `seq`*, never as of a timestamp. The global outbox sequence is `event_seq` and is a different number |
| `effective_at` | **Game truth.** May be backdated |
| `recorded_at` | **System truth.** Never backdated. Disputes need both |
| `config_snapshot_json` | The exact rules in force when planned. Changing a pool's config must not change what a past batch meant |
| `rng_seed` | Persisted so a replay is byte-identical |
| `amount_cp` | `CHECK (amount_cp <> 0)`. A zero entry is noise that breaks `entry_count` reasoning |
| `character_id` | Attribution only. It **never** affects a balance — re-parenting an alt must not move history |
| `net_amount_cp` | Σ entries; `0` for zero-sum. A cheap invariant check that costs one column |

Balances are derived: `COALESCE(sum(e.amount_cp), 0)` over `ledger_entry` joined to `ledger_batch`
with `b.seq <= ?`. `balance_snapshot` is a **droppable cache**, maintained in the same transaction
as the write and verified nightly by the replay job. Never read a balance from anywhere else.

## Append-only, and what follows from it

Four triggers, one per table per verb:

```sql
CREATE TRIGGER trg_ledger_batch_no_update BEFORE UPDATE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;
-- ... _no_delete, and the same pair on ledger_entry
```

An integration test asserts `UPDATE ledger_entry SET amount_cp = 1` raises, so the guardrail itself
cannot be silently regressed. Two consequences the code must respect:

- **"Is this batch reversed?" is a query, not a column.** You can never
  `UPDATE ledger_batch SET reversed_by = …`. `ux_batch_reverses` — a unique index on
  `reverses_batch_id` — makes `EXISTS (SELECT 1 FROM ledger_batch WHERE reverses_batch_id = ?)` an
  index-only lookup *and* enforces that a batch is reversed at most once.
- **Forward pointers live on the mutable fact tables** (`raid_tick.ledger_batch_id`,
  `item_award.void_batch_id`). Facts point at money; money never points at mutable state.

## `seq` allocation

Inside `store.Tx` only — the write pool is `_txlock=immediate` with `SetMaxOpenConns(1)`, so this is
the only writer:

```sql
-- name: NextPoolSeq :one
SELECT COALESCE(max(seq), 0) + 1 AS next_seq FROM ledger_batch WHERE pool_id = ?;
```

`ux_batch_seq (pool_id, seq)` is the guardrail if the single-writer property is ever lost. **This is
dialect divergence #1** — on Postgres it must become a locked counter row or an advisory lock;
`max+1` is not safe under real concurrency. Do not copy this pattern anywhere else.

## Reversals

A correction is a **new batch**, `kind = 'reversal'`, `reverses_batch_id` set. The original stays
visible and renders struck through. Reversal of a reversal is just another reversal.

`PlanReversal`'s default is entry-wise negation. **The default is wrong for at least one balance
kind and you must not assume it.**

> **Suicide Kings' inverse is not negation.** `sk_position` is an ordering, not a quantity. Negating
> a position delta does not restore the list, because everyone below the winner shifted up in the
> meantime. `suicide_kings` records the suicide as an explicit delta
> (`account`, `from_pos`, `to_pos`, `shifted_set`) — or snapshots the whole list, which is ~100 rows
> and cheap at guild scale — and `PlanReversal` replays that delta backwards. The
> `Permutation(kind=sk_position)` invariant is what catches you if you get it wrong.

Any strategy whose balance kind is not a plain quantity must override `PlanReversal` and say so in
its doc comment. `RatioPreserved`-style kinds (EPGP's EP/GP pair) must reverse both legs together.

**Retroactive zero-sum edits compensate, never replay.** Reversing a six-month-old zero-sum award
means every intermediate balance was arithmetically "wrong", and you cannot fix that without
rewriting history. Default: one corrective batch at today's `seq`, surfaced in a per-account
Corrections tab. Full replay exists only as an explicit officer job with a mandatory dry-run diff
emitting a single net-delta `correction` batch.

**Decay is posted, not computed.** Decay runs emit explicit batches with idempotency key
`(pool_id, cadence_period)`, so a balance is always literally a `SUM` and "why did my points change?"
is answerable. Computed weighting is permitted in `Priority()` and **never** in `Spendable()`.

## Largest-remainder allocation

Zero-sum credits must sum to **exactly** the debit. Rounding each credit independently mints or
destroys points, and at 40 attendees a night that is a visible drift within a month.

```
Given a debit of P centipoints split over N accounts with weights w_i (Σw = W):

  quota_i     = P * w_i / W                       (exact rational, computed in int64)
  base_i      = floor(quota_i)
  remainder_i = quota_i - base_i
  R           = P - Σ base_i                      (0 <= R < N)

  Sort by remainder_i DESC, then account_id ASC.  Award +1 cp to the first R accounts.
```

- The tiebreak on `account_id` (ULID, lexicographic) is **mandatory and deterministic**. Without it
  two replays of the same batch differ and the determinism hash test fails.
- Compute in `int64` throughout. `P * w_i` before dividing; never build a ratio as a float.
- Assert `Σ credits + debit == 0` before returning the proposal. `LargestRemainderSumsToDebit` will
  reject you at commit time, but failing in the planner names the strategy.
- Degenerate cases route to a **system account**, never to a silent drop: `N = 0` (solo kill) →
  `guild_bank` per `solo_policy`; a rotted item → `write_off`; an unallocatable remainder →
  `residue`. System accounts are ledger-addressable precisely so `Conserved` stays verifiable.

## The Invariant vocabulary — executable objects, not prose

```go
type Invariant interface {
    Name() string
    Check(ctx InvariantCtx, b BatchProposal) error   // returns a named, quotable failure
}
```

Every strategy returns its set from `Invariants()`. The ledger service checks the declared set
**plus** the universal set no strategy may waive, then commits in one transaction with the
idempotency key and the hash chain.

| Invariant | Constrains |
|---|---|
| `SumZero(kind, scope=batch)` | Zero-sum award batches sum to exactly 0 |
| `NonNegative(kind, floor)` | Balance may not drop below `floor` after this batch |
| `MonotoneNonDecreasing(kind)` | EPGP's GP never decreases except via reversal |
| `Permutation(kind=sk_position)` | Positions remain a bijection over the eligible list |
| `RatioPreserved(a, b, tol)` | A decay batch scales EP and GP identically |
| `Conserved(kind, total)` | Total across all accounts unchanged |
| `LargestRemainderSumsToDebit` | Credits sum to the debit exactly |
| **Universal, always on** | `NoFloat`, `BatchNonEmpty`, `EntriesReferenceLiveAccounts`, `SeqMonotonic` |

A new strategy that declares no invariants is a red flag — the declared set must actually constrain
its planner. Every invariant gets a **property test** (`pgregory.net/rapid`), not just an example
test: `SumZero` under random award/reversal sequences, SK positions remaining a permutation under
random suicides/insertions/absences, EPGP decay preserving PR within tolerance.

## Purity — the third architectural law

`internal/strategy` may not import `internal/store`, may not import `time` for wall-clock use, and
may not use `math/rand`. Enforced by an **import-graph test**, not by review.

```go
// ctx is a read-only façade: pool config, Balance(account, kind, asOfSeq),
// attendance statistics, roster, item catalogue, Clock, Rng.
func (s *ZeroSum) PlanAward(ctx strategy.Ctx, ev AwardEvent) (BatchProposal, error)
```

- The **Clock** is injected. `s.clock.Now()`, never `time.Now()`.
- The **Rng** is seeded, and `ctx.Rng().Seed()` is written onto `ledger_batch.rng_seed`. Persisting
  the seed is what makes a replay byte-identical; without it, tie-break RNG makes the ledger
  unreproducible and the determinism test meaningless.
- No floats. `golangci-lint` bans `float32`/`float64` in both packages, and `NoFloat` catches it
  again at runtime.
- Planning the same event twice must produce **byte-identical** proposals; a test hashes the
  canonical JSON of the `BatchProposal` and compares. Compare the whole proposal against a golden,
  not three chosen fields.

Strategies shipping in 1.0: `fixed_price`, `tick`, `attendance_weighted`, `zero_sum`,
`decay_percent`, `decay_window`, `cap`, `start_points`, `loot_council`, `roll`, `relative_bid`,
`auction_open`, `auction_sealed`.

`epgp` and `suicide_kings` (three variants) are **conditional, not scheduled**: designed here, built
only when a named guild asks in an issue. Their invariants (`MonotoneNonDecreasing`, `Permutation`,
`RatioPreserved`) are documented above so the interface is right when they land.

## Stop and ask if

- You are about to add a `kind` to `ledger_batch` — it is a CHECK constraint, an OpenAPI enum and a
  docs page in one.
- A correction cannot be expressed as a reversal or a compensating batch.
- A strategy needs to read something the `Ctx` façade does not expose. Widening the façade is a
  design decision; importing `internal/store` is a law violation.
- A guild's decay, cap or start-points rule is ambiguous. Guessing produces silently wrong balances,
  which is worse than an error.
