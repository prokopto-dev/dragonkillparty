# ADR-0023 — `balance_snapshot` is load-bearing, not a droppable cache

**Status:** accepted · **Date:** 2026-08-13 · **Deciders:** owner

## Context and problem statement

A balance is defined as `sum(amount_cp)` over an append-only log, and `balance_snapshot` caches it
per `(pool, account, balance_kind)`. Every document so far has called that cache **droppable** — an
optimisation the product could lose and still answer correctly, only slower. Nobody had measured
"slower". [`V5`](../development/verify-before-phase-0.md) scheduled the measurement before any API
was built precisely so the answer could still change the schema, and twelve strategies plus the
standings UI are about to be built on whichever answer this is.

## Considered options

Measured over a `seed.Perf` ledger — 527,164 entries, 20,461 batches, 280 accounts, all written
through `ledger.Service.Commit` — at 280 members per page. Page counts are from `dbstat`; the
SD-card figure is `warm p99 + pages × 2 ms`, three times pessimistic against the SD Association's
A1 floor of 1,500 random-read IOPS.

| Option | For | Against |
|---|---|---|
| A — keep the cache, keep calling it droppable | No work. Matches every document already written | The claim is false by a factor of 800, and a future reader would act on it |
| B — **keep the cache, call it load-bearing** | 13 pages, ≤ 27 ms modelled, 1 statement; the log stays the only source of truth | Losing the cache becomes a *rebuild*, so the nightly replay is now a dependency rather than a nicety |
| C — drop the cache, serve standings from the SUM | One fewer thing to keep correct; "balances are derived" needs no footnote | 10,412 pages, ~22 s modelled, warm tail 0.67–2.93 s on an NVMe SSD. Misses a 150 ms budget by two orders of magnitude |
| D — widen `ix_snapshot_standings` to cover the drift columns | Saves 7 of the 13 pages | A migration to spend 14 ms of a 150 ms budget that already passes with 5.5× headroom |

## Decision outcome

**Chosen: B, with D explicitly declined.** The cache stays exactly as it is — no new column, no new
index, no change to when it is written. What changes is what the project *says* about it, because
option A's claim is not a harmless simplification: a reader who believes the cache is droppable will
one day drop it, or defer the job that verifies it, and both are now known to be wrong.

The precise statement, which is narrower than "the cache is the truth":

- **The log remains the only source of truth.** `BalanceAsOfSeq` settles a dispute,
  `StandingsFromLedger` recomputes every balance from the log, and the cache is *proven* to equal a
  full fold of 527,164 entries — amount, entry count and as-of-seq, in both directions, by
  `TestPerf_StandingsOverASeededLedger_MeetsItsBudgets`.
- **Losing the cache is a rebuild, not a degradation.** There is no honest fallback path that serves
  the page from the log, so the nightly replay job (ROADMAP Phase 1 item 9) is a **correctness
  dependency of the standings page**, not an optional verification.
- Both query plans are pinned by `EXPLAIN QUERY PLAN` goldens
  (`test/golden/explain/standings_snapshot.txt`, `standings_ledger.txt`), and the budget itself is a
  gate: `make test-perf`, run nightly as `perf / 520k`.

D is declined rather than deferred, and the evidence is kept executable:
`TestStandings_SnapshotProjection_WithoutTheDriftColumns_IsCovering` proves the *existing* index
answers the render-only projection with no table access. If Phase 3 ever needs those 14 ms, the fix
is a narrower `SELECT`, not a migration over the table that carries the cache.

### Consequences

- Good, because the standings page is 1 statement and ≤ 27 ms modelled on the hardware a volunteer
  officer actually runs, with 5.5× headroom.
- Good, because the cache-versus-fold equality is now a test at guild scale rather than an argument,
  and it runs against a dataset the real commit path wrote.
- Good, because declining the migration keeps the append-only tables untouched, and every migration
  near them risks the triggers ([ADR-0008](0008-atlas-authors-goose-applies.md),
  [`V6`](../development/verify-before-phase-0.md)).
- **Bad, because "balances are derived" now needs a paragraph rather than a sentence.** It is still
  true, and it is no longer the whole story, and `docs/concepts/ledger.md` has to spend a section
  saying both halves to an officer who did not ask.
- **Bad, because a job is now load-bearing.** If the nightly replay silently stops running, a drifted
  cache becomes undetectable — the failure mode this project criticises EQdkp Plus for. The job must
  fail loudly and visibly, which is a standard this decision raises for it.
- **Bad, because the verdict is scale-dependent and the scale is an unverified assumption.**
  [`V3`](../development/verify-before-phase-0.md) — 280 accounts, 3,400 raids, ~520k entries — is
  still nobody's measurement of a real guild. If guilds are five times larger the conclusion gets
  stronger, not weaker; if they are ten times smaller, option C becomes viable again and this ADR
  should be re-litigated rather than inherited.

### Reversal cost

Cheap in code and expensive in judgement: dropping the cache is deleting one table, one upsert and
one query — an afternoon — and the standings page then takes seconds on an SD card. Adding the
covering columns later (option D) is one forward `CREATE INDEX` migration, no table rebuild, a day
including its measurement.
