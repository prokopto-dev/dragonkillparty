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
