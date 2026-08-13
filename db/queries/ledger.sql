-- Ledger reads and the snapshot upsert - Phase 0 PR 9. Shapes follow db/RECIPES.md.
--
-- Every statement that reaches SQLite in this project is generated from a file like this one:
-- db.Query and db.Exec outside internal/store are grep-banned (gate SQL002), so the only way a new
-- query enters the codebase is by being written here first and reviewed as SQL.
--
-- PR 9 was READ and HELPER only; PR 10a adds the WRITE half. InsertLedgerBatch and InsertLedgerEntry
-- below are the only INSERTs the product has into the two append-only tables, and they are called
-- from exactly one place: ledger.Service.Commit, inside one store.Tx. There is deliberately no
-- UPDATE and no DELETE for either table, in this file or anywhere else - the append-only triggers
-- would abort them, and a correction is a reversal batch (canonical section 10).
--
-- Keep every comment in this file ASCII-only. sqlc v1.31.1 computes each query's text span in bytes
-- but truncates by rune count, so a multibyte character (an em dash, a section sign) in a preceding
-- comment lops that many trailing characters off the generated query string. The failure is silent
-- at generate time and shows up as a syntax error only when the query runs.

-- BalanceAsOfSeq is the definitional balance query, and the single most important read in the
-- product. A balance is a SUM over an append-only log, positioned by seq - NEVER by timestamp,
-- because a backdated effective_at must not change what a past balance WAS.
--
-- It reads e.seq directly, with NO join to ledger_batch: the seq is denormalised onto every entry
-- precisely so this query is served entirely from ix_entry_balance
-- (pool_id, account_id, balance_kind, seq, amount_cp) with no table access. The EXPLAIN QUERY PLAN
-- golden (test/golden/explain/ledger_balance.txt) asserts that covering-index plan, because the day
-- it starts scanning is the day standings gets slow and nobody notices.
--
-- Aggregate with sum(), never the float-returning alternative SQLite offers (canonical C3 / MONEY002):
-- that one returns a float and would silently convert the ledger to floating point. sum() over zero
-- rows is NULL, hence COALESCE(..., 0).

-- name: BalanceAsOfSeq :one
SELECT CAST(COALESCE(sum(amount_cp), 0) AS INTEGER) AS amount_cp
FROM ledger_entry
WHERE pool_id = ? AND account_id = ? AND balance_kind = ? AND seq <= ?;

-- MaxPoolSeq is the current head seq for a pool: the ?4 argument BalanceAsOfSeq needs to compute a
-- CURRENT balance ("as of the latest seq"). COALESCE to 0 for an empty pool, so a pool with no
-- batches yet reports head 0 rather than NULL.

-- name: MaxPoolSeq :one
SELECT CAST(COALESCE(max(seq), 0) AS INTEGER) AS max_seq FROM ledger_batch WHERE pool_id = ?;

-- NextPoolSeq allocates the next per-pool sequence number. It MUST run inside store.Tx (the write
-- pool is _txlock=immediate with SetMaxOpenConns(1), so it is the only writer and max+1 is a correct
-- allocator). ux_batch_seq(pool_id, seq) is the guardrail if the single-writer property is ever lost.
--
-- This is dialect divergence #1 (db/RECIPES.md): on Postgres max+1 is NOT safe under real
-- concurrency and becomes a locked counter row or an advisory lock. Do not copy this pattern.

-- name: NextPoolSeq :one
SELECT CAST(COALESCE(max(seq), 0) + 1 AS INTEGER) AS next_seq FROM ledger_batch WHERE pool_id = ?;

-- UpsertBalanceSnapshot maintains the droppable balance cache, ADDITIVELY, in the same transaction
-- as the batch write (PR 10). The primary key is (pool_id, account_id, balance_kind) - the same key
-- the WITHOUT ROWID table is built on - and on conflict the amount and the entry count are ADDED to
-- the existing row, not replaced: the caller passes this batch's delta (the SUM and COUNT of just its
-- entries), so the running total accumulates. as_of_seq and updated_at are advanced to the new head.
-- A batch has at most ~70 entries, so this is a sub-millisecond indexed write under the single writer.

-- name: UpsertBalanceSnapshot :exec
INSERT INTO balance_snapshot (pool_id, account_id, balance_kind, amount_cp, as_of_seq, entry_count, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (pool_id, account_id, balance_kind) DO UPDATE SET
    amount_cp   = amount_cp   + excluded.amount_cp,
    entry_count = entry_count + excluded.entry_count,
    as_of_seq   = excluded.as_of_seq,
    updated_at  = excluded.updated_at;

-- StandingsFromSnapshot is THE standings read: every account's balance in one pool, highest first,
-- from the droppable cache rather than from the log. It is the query V5 budgets
-- (docs/development/verify-before-phase-0.md): 4 SQL statements and p99 150 ms at 280 members over a
-- 520k-entry ledger, on SD-card-class storage.
--
-- ix_snapshot_standings is (pool_id, balance_kind, amount_cp DESC), and balance_snapshot is
-- WITHOUT ROWID, so the index's trailing key columns are the primary key - which is what makes
-- `ORDER BY amount_cp DESC, account_id ASC` an index walk rather than a sort. The account_id
-- tiebreak is NOT decoration: two accounts on the same balance would otherwise come back in an
-- order the planner is free to change between releases, and a page boundary that lands between them
-- would skip or repeat a member. The EXPLAIN QUERY PLAN golden
-- (test/golden/explain/standings_snapshot.txt) asserts the walk stays a walk.
--
-- NO CURSOR PARAMETER, deliberately. The cursor predicate belongs with the endpoint that pages
-- (Phase 3), and inventing its shape here would freeze a pagination contract nothing has reviewed.
-- What this query does owe that endpoint is the ORDER BY above: the cursor it grows must tiebreak on
-- account_id in the same direction, or the page boundary is not stable.

-- name: StandingsFromSnapshot :many
SELECT account_id, amount_cp, as_of_seq, entry_count
FROM balance_snapshot
WHERE pool_id = ? AND balance_kind = ?
ORDER BY amount_cp DESC, account_id ASC
LIMIT ?;

-- StandingsFromLedger is the SAME answer computed the definitional way: one grouped SUM over every
-- entry in the pool up to a seq, with no cache involved. It is the arm V5 measures
-- StandingsFromSnapshot against, and it is what a replay or a verification job reads.
--
-- It is one statement either way, so the interesting number is never the statement count - it is the
-- work. This one aggregates every entry ever written (520k rows at guild scale) where the snapshot
-- read touches one row per account (280). Both plans are pinned by goldens; the measured gap between
-- them is the whole of V5's answer.
--
-- It returns the entry COUNT alongside the sum because balance_snapshot caches both, and a drift
-- check that compared only the sums would pass on a cache that had folded the wrong number of
-- entries into the right total. count(*) costs nothing here: ix_entry_balance already covers every
-- column this reads, so the count is of index rows already being walked.
--
-- Aggregate with sum(), never the float-returning alternative SQLite offers (canonical C3 /
-- MONEY002) - that one would silently convert the ledger to floating point. The CAST pins the
-- aggregate to INTEGER for sqlc, which cannot infer an affinity for an expression that belongs to
-- no table.

-- name: StandingsFromLedger :many
SELECT account_id,
       CAST(COALESCE(sum(amount_cp), 0) AS INTEGER) AS amount_cp,
       CAST(count(*) AS INTEGER) AS entry_count
FROM ledger_entry
WHERE pool_id = ? AND balance_kind = ? AND seq <= ?
GROUP BY account_id
ORDER BY amount_cp DESC, account_id ASC
LIMIT ?;

-- GetAccount reads one account by id. It backs the "system accounts are addressable by id"
-- acceptance test and the account reader in internal/ledger.

-- name: GetAccount :one
SELECT id, kind, person_id, system_key, label, created_at, updated_at
FROM account WHERE id = ?;

-- GetSystemAccount reads one system account by its system_key ('residue', 'guild_bank', ...). It is
-- how a service or a test resolves the four seeded accounts by name rather than by their ULID.

-- name: GetSystemAccount :one
SELECT id, kind, person_id, system_key, label, created_at, updated_at
FROM account WHERE system_key = ?;

-- InsertAccount creates one balance holder. The four SYSTEM accounts are seeded by the migration and
-- never inserted through here; this is the person half, and its first caller is internal/seed - a
-- ledger of 520k entries needs 280 accounts to hang them on, and the commit path's
-- EntriesReferenceLiveAccounts invariant looks every one of them up.
--
-- The two paired CHECKs on the table (account_person_shape, account_system_shape) mean the caller
-- cannot get the kind/person_id/system_key combination wrong quietly: a person with a system_key, or
-- a system account with no key, is a constraint violation rather than a row nobody notices. Both
-- nullable columns are supplied explicitly for the reason InsertLedgerBatch names every column -
-- a value the database invented is a value the caller never saw.

-- name: InsertAccount :exec
INSERT INTO account (id, kind, person_id, system_key, label, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- InsertLedgerBatch writes the batch header. Every column is supplied by the caller, including seq
-- (allocated by NextPoolSeq in the same transaction) and hash (computed over the batch and its
-- entries by internal/ledger/hashchain.go). Nothing is defaulted at the database, because a value
-- the database invented is a value the hash did not cover.
--
-- Note what is NOT here: no UPDATE. ledger_batch is append-only, enforced by trg_ledger_batch_no_update
-- and by TestTriggers_MutatingLedger_Raises. If this insert fails, the whole transaction rolls back
-- and no partial batch survives (TestCommit_FaultInjectedMidWrite_LeavesNothing).

-- name: InsertLedgerBatch :exec
INSERT INTO ledger_batch (
    id, pool_id, seq, kind, strategy_id, strategy_version, config_snapshot_json, rng_seed,
    source, source_ref, actor_user_id, actor_token_id, actor_is_beneficiary, reason,
    reverses_batch_id, effective_at, recorded_at, effective_day, idempotency_key,
    entry_count, net_amount_cp, prev_hash, hash
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?,
    ?, ?, ?, ?
);

-- InsertLedgerEntry writes one account's share of one batch. pool_id and seq are DENORMALISED from
-- the batch on purpose: carrying them on the entry is what lets BalanceAsOfSeq be answered from
-- ix_entry_balance with no join (see that query's comment). The caller must write the batch's own
-- pool_id and seq here - a mismatch would make the balance index disagree with the batch.
--
-- amount_cp carries CHECK (amount_cp <> 0). A zero entry is noise that breaks entry_count reasoning,
-- so a caller that computed a zero share drops the entry rather than writing it; ledger.Allocate
-- filters zero allocations for exactly this reason.

-- name: InsertLedgerEntry :exec
INSERT INTO ledger_entry (
    id, batch_id, pool_id, seq, account_id, character_id, balance_kind, amount_cp,
    item_id, item_award_id, raid_id, tick_id, metadata_json
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?
);

-- GetBatchByIdempotencyKey is the replay lookup. Every mutating POST that creates domain state
-- carries an Idempotency-Key (canonical invariant); a bot that retries must land on the FIRST batch
-- rather than committing a second one, because a duplicated raid tick or a double-charged bid is the
-- top support burden this product exists to remove.
--
-- The key is (pool_id, idempotency_key) and it is served by the partial unique index ux_batch_idem,
-- so this is an index lookup rather than a scan. It is deliberately NOT scoped by token: a token
-- rotated between the first attempt and the retry must still replay, which is why the uniqueness is
-- on the pool and the key and never on actor_token_id (domain model section 15 says the same of
-- idempotency_key.principal_ref: "NEVER 'token:<ulid>': rotation mid-retry must replay").
-- TestCommit_DuplicateIdempotencyKey_ReturnsFirstBatch rotates the token between the two calls.

-- name: GetBatchByIdempotencyKey :one
SELECT id, seq, entry_count, net_amount_cp, prev_hash, hash
FROM ledger_batch
WHERE pool_id = ? AND idempotency_key = ?;

-- GetLedgerBatch reads one batch by id. It backs the reversal-linkage check: before a reversal is
-- written, the commit path loads the batch it claims to reverse and requires it to be in the SAME
-- POOL, because balances are per-pool and a cross-pool reversal undoes nothing while still marking
-- its target reversed.
--
-- The database cannot answer this on its own, which is why the read exists. The self-FK proves the
-- target EXISTS and ux_batch_reverses proves it is reversed at most once; neither can express "and
-- it is in this pool", and neither can express "only a batch of kind 'reversal' may carry this
-- pointer at all". Both of those are checked in Go, inside the transaction, against this row.

-- name: GetLedgerBatch :one
SELECT id, pool_id, seq, kind FROM ledger_batch WHERE id = ?;
