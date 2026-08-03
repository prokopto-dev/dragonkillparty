# ADR-0002 — Append-only ledger

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

The single hardest question in guild software is "why did my points change?", and it is asked by a
raider who is already angry. EQdkp Plus answers it badly: an officer can edit a raid, an adjustment
or a member's total in place, so history is whatever the last edit says it was, and a dispute becomes
one person's word against another's. Every other feature in this product — the importer, bidding,
decay, the audit trail — is downstream of whether a balance can be trusted.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Mutable `balance` column, corrected in place | Trivial to implement; one row per member; what EQdkp does | History is unrecoverable; a bug or a bad import silently overwrites ten years of DKP; a dispute has no evidence |
| B — Mutable ledger rows plus a separate audit log | Familiar; corrections stay one operation | The audit log and the ledger drift; the audit log is written by the same code that got it wrong; nothing structurally prevents the `UPDATE` |
| C — Append-only ledger; corrections are reversal batches | History is complete by construction; balances are provable; replays are deterministic | Every correction is two records; the UI must render struck-through history; officers used to an edit button find it slower |

## Decision outcome

**Chosen: C.** `ledger_batch` and `ledger_entry` rows are written once and never modified. To fix a
mistake you write a **new batch with `reverses_batch_id` set** and the entries negated (or a
strategy-specific inverse); the original stays visible, struck through. Balances are **derived**,
defined as of a per-pool `seq` — never as of a timestamp — and `balance_snapshot` is a droppable
cache, rebuilt on demand and verified nightly. Decay is **posted** as explicit batches with an
idempotency key of `(pool_id, cadence_period)`, so a balance is always literally a `SUM` rather than
a formula evaluated at read time.

**Enforced by:** a `BEFORE UPDATE OR DELETE ON ledger_batch|ledger_entry … RAISE(ABORT, 'ledger is
append-only')` trigger, **plus** an integration test asserting the trigger actually fires — so the
guardrail itself cannot be silently regressed by a migration. A nightly `verify` job rebuilds every
snapshot from an empty cache and alarms on drift, which turns a rollup bug into a bug report instead
of a support ticket.

### Consequences

- Good, because every dispute has an answer that is a link, not an argument: the batch, its source
  artifact, its actor, and the reversal if there was one.
- Good, because an import can be undone. `undo-this-import` is a reversal batch, not a restore, which
  is what makes a parallel run zero-risk for a guild evaluating the product.
- Good, because an agent cannot "fix" a failing balance test by editing history — the database
  refuses, at the layer no code path can talk its way past.
- Good, because a replay is byte-identical: strategies are pure, the clock and RNG are injected, and
  the seed is persisted onto the batch.
- **Bad, because correcting a typo costs two batches and a UI that must explain both.** Officers
  migrating from EQdkp's edit button will read this as friction, and on a bad raid night — a
  misconfigured pool, a double-posted tick — the reversal traffic doubles the row count and the
  standings page grows a wall of struck-through entries.
- **Bad, because the ledger only ever grows.** It is the one table with no retention story: pruning it
  is definitionally forbidden, so a ten-year guild carries ten years of rows forever. At P99 scale
  (~10⁵–10⁶ entries) that is fine; it is still a hard floor on database size and on `verify` runtime.
- **Bad, because reversals compose confusingly.** Reversing a reversal is legal and produces a chain
  that is correct and hard to read. The UI has to make that legible, and no schema constraint helps.
- **Bad, because "append-only" is only as strong as the migration review.** The trigger lives in a
  migration; a future migration could drop it. The asserting test is what closes that, and it is a
  test, not a law of physics.

### Reversal cost

Catastrophic and effectively unavailable. Every trust claim the product makes, the importer's undo,
and the entire dispute-resolution story rest on this. Changing it in two years means rewriting
`internal/ledger`, invalidating every snapshot, and telling users that history is negotiable again.
