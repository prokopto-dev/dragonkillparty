// Package seed generates synthetic guild data for development and for measurement.
//
// Phase 1, issue #190. The first profile is Perf: a realistic-guild-scale ledger — 280 accounts,
// 3,400 raids, ~520,000 ledger entries — at the scale item V3 of
// docs/development/verify-before-phase-0.md claims a real P99 guild reaches. It exists so the
// standings and balance reads can be measured against a ledger the size they will actually meet,
// before an API is built on top of the answer (item V5).
//
// EVERY ROW GOES THROUGH ledger.Service.Commit. That is the single most important property of this
// package and the reason it is slower than a generator that bulk-inserts. A dataset written by a
// side door proves nothing about the door the product uses: it would skip the invariant engine, the
// hash chain, the per-pool seq allocator and — fatally for V5 — the synchronous balance_snapshot
// upsert, which is the very cache the measurement exists to judge. A snapshot-versus-fold property
// checked against a snapshot that a test wrote by hand is circular. Checked against one the commit
// path wrote, it is a finding.
//
// The consequence to know before you reach for this: generating the full Perf profile is tens of
// seconds and produces a database of a few hundred megabytes. It is not a fixture you clone per
// test. `make test-perf` builds it once and measures against it; `make seed` builds it into a
// developer's database.
//
// WHAT IS SYNTHETIC AND WHAT IS NOT. The SHAPE is real: real batch kinds, real entry provenance,
// real attendance rosters that differ raid to raid, real zero-sum splits through ledger.Allocate
// with its largest-remainder tiebreak, real per-account running balances in the cache. The
// ECONOMICS are not: tick values, item prices and decay debits are drawn from a seeded PRNG rather
// than simulated from a guild's rules, and no attempt is made to model what a raider's balance
// "should" be. That is the right trade for a performance profile, where the row counts, the index
// shapes and the value distributions drive every number and the underlying DKP policy drives none
// of them. A profile that models economics is seed.Demo's job (Phase 3), and it is a different job.
//
// DETERMINISM. Given the same Profile, the walk emits byte-identical batch CONTENT: the same
// accounts, the same amounts, the same order, every run. What is NOT reproducible is the row ids
// and the hash chain, because core.Generator draws its ULID entropy from crypto/rand — so two runs
// of the same profile agree on every balance and disagree on every primary key. Assert on balances.
//
// LAW 2. Nothing here holds a *sql.DB or writes raw SQL: accounts are inserted through the
// generated InsertAccount on the store.Queries contract, inside store.Tx, and everything else goes
// through the ledger service. LAW 3 is untouched — this package consumes strategy.BatchProposal as
// a data shape and adds nothing to internal/strategy, which stays pure.
package seed
