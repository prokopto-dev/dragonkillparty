# ADR-0024 — One cadence-run table, and `kind` is inside the unique index

**Status:** accepted · **Date:** 2026-08-13 · **Deciders:** owner
**Amends:** [ADR-0002](0002-append-only-ledger.md) — its idempotency key gains `kind`. Nothing else in
that record changes.
**Resolves:** [#206](https://github.com/prokopto-dev/dragonkillparty/issues/206), the unresolved box
in `.claude/rules/decay-and-jobs.md` §3.

## Context and problem statement

Two normative statements, both current, that cannot both hold.

**All three cadence families key on the same pair.** `docs/design/00-canonical-conventions.md` §10,
*as it read before this decision*: "Decay is posted, not computed — explicit batches with idempotency
key `(pool_id, cadence_period)`", and `docs/api/idempotency-and-concurrency.md` extended it to "a
decay, cap or start-points run".

**And there is exactly one run table.** `docs/design/01-domain-model.md` §12.3 defines `decay_run`
and nothing else, and its index then read `UNIQUE(pool_id, cadence_period)`.

So a `cap` run for `2026-W31` violates an index that exists to stop a *repeat*. The failure is
silent and indistinguishable from success: an idempotent job that hits a uniqueness violation on its
own key is *supposed* to conclude "already done, nothing to do" and exit 0. A guild with both a
weekly decay and a weekly cap gets decay, then a cap that thinks it already ran — every week, with a
green job dashboard, until someone reconciles balances by hand. That is the class of defect this
project cites EQdkp Plus for, produced by the mechanism chosen to prevent it.

## Considered options

| Option | For | Against |
|---|---|---|
| A — `kind` inside the unique index: `UNIQUE(pool_id, kind, cadence_period)` | One column. One table, one state machine, one dashboard, one job path for all three families | The table's name becomes a slight lie: `decay_run` holds cap and start-points runs too |
| B — One table per family: `decay_run`, `cap_run`, `start_points_run` | Truest to the current DDL and to the name | Triples the surface for one concept; the periodic-job code becomes three near-copies, which is where the next divergence comes from |
| C — `decay_run` is decay-only; cap and start-points are idempotent on their batch key alone | Cheapest; no schema change at all | They then have no preview state, no `dry_run_result_json`, no `failed` row and no error text — and P6/P7 ("applying the cap twice produces one batch", "`start_points` applies exactly once per account") are exactly the properties that want a durable record of the attempt |

## Decision outcome

**Chosen: A.** `decay_run` gains `kind TEXT NOT NULL` and `ux_decay_period` becomes
`UNIQUE(pool_id, kind, cadence_period)`.

- **The vocabulary is a catalogue, not a literal** — `internal/decay/kinds`, beside `state`, so the
  CHECK is generated (canonical §5) and a family has a symbol rather than a string. A *misspelled*
  family is the worse defect of the two: it collides with nothing, takes its own slot in the index,
  and lets the real run for that period proceed — so the period decays twice.
- **No `DEFAULT`**, where `state` has one. There is no default family; defaulting to `'decay'` would
  let a cap job that forgot the column take decay's slot.
- **The three values are also `ledger_batch` kinds**, and the two catalogues stay independent:
  fourteen batch kinds against three cadence families, so importing one from the other would make
  adding a fifteenth batch kind look like adding a fourth family. `TestDecayKinds_Kinds_AreLedgerBatchKinds`
  asserts the agreement from `internal/ledger`, which can see both.
- **The batch's `idempotency_key` carries the kind too**, from the same identity through one helper
  (`.claude/rules/decay-and-jobs.md` §3). Two layers, deliberately: `ux_decay_period` stops a second
  *run row*, `ux_batch_idem` stops a second *batch*.

### Consequences

- Good, because the collision is now expressible as a test: a cap and a decay run for one period are
  both admitted, and a repeat within a family is still refused.
- Good, because one table means the preview → commit lifecycle, the dry-run document, the failure
  record and the officer dashboard are written once for three families.
- Good, because `#193` and `#194` are unblocked without each inventing an answer.
- **Bad, because `decay_run` now holds rows that are not decay.** The name is wrong and stays wrong:
  renaming a table is SQLite's 12-step rebuild for a word, and the rule file, the domain model and
  the API path (`/decay-runs`) would all move with it. A comment on the table carries the caveat.
- **Bad, because it moved a key that seven documents state.** The domain model is the schema
  authority, so a schema that disagreed with it would be the defect rather than the record — §12.3,
  canonical §10, `docs/api/idempotency-and-concurrency.md`, `02-api-design.md`, `04-testing.md`'s P9,
  the choosing-a-DKP-system guide and ADR-0002's phrasing are all amended in the same change. A key
  written down in that many places is one that will be quoted from memory in the eighth.
- **Bad, because the index is wider**, three columns rather than two. Immaterial at this row count
  (one run per pool per period per family), and stated so it is not rediscovered as a surprise.

### Reversal cost

Small, in either direction, and cheaper than it looks: `kind` is an `ADD COLUMN` and the index is a
`DROP INDEX` + `CREATE UNIQUE INDEX` — no table rebuild, so no trigger risk. Moving to option B later
is a data migration that splits rows by `kind`, which the column makes mechanical rather than
guesswork; moving to option C is dropping rows whose `kind` is not `'decay'`.
