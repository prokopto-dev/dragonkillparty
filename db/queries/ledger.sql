-- Ledger reads and the snapshot upsert - Phase 0 PR 9. Shapes follow db/RECIPES.md.
--
-- Every statement that reaches SQLite in this project is generated from a file like this one:
-- db.Query and db.Exec outside internal/store are grep-banned (gate SQL002), so the only way a new
-- query enters the codebase is by being written here first and reviewed as SQL.
--
-- PR 9 is READ and HELPER only. There is deliberately NO batch or entry INSERT here: the commit
-- service that writes them is PR 10, and batch/entry inserts appear only in tests (raw or generated)
-- until then. What ships now is the balance query, the seq allocator, the snapshot upsert and the
-- account/system-account readers - the surface a service reads through, before any service writes.
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
