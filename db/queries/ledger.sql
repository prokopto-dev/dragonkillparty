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

-- UpsertBalanceSnapshot maintains the balance cache, ADDITIVELY, in the same transaction as the
-- batch write (PR 10). The primary key is (pool_id, account_id, balance_kind) - the same key
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
-- from the cache rather than from the log. It is the query V5 budgets
-- (docs/development/verify-before-phase-0.md): 4 SQL statements and p99 150 ms at 280 members over a
-- 520k-entry ledger, on SD-card-class storage.
--
-- V5 answered, and the answer is ADR-0023: this read is 13 pages where StandingsFromLedger below is
-- 10,412, so the cache is LOAD-BEARING rather than droppable. The log is still the only source of
-- truth and a dispute is still settled by BalanceAsOfSeq - what the measurement removed is the
-- fallback. Do not give this read a recompute-from-the-log path "for safety": that path is 22
-- seconds.
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

-- The REPLAY reads - Phase 1, issue #198. `dkp verify-ledger` walks the whole ledger from genesis
-- and recomputes it: the per-pool hash chain, and balance_snapshot from a fold over every entry.
--
-- ALL FOUR ARE KEYSET-PAGED, and at 520,000 entries that is not a style preference. A `:many` query
-- materialises its whole result set as a Go slice, so a single `SELECT * FROM ledger_entry` would
-- put the entire ledger in memory at once on the smallest machine this product targets - a
-- Raspberry Pi with the database on an SD card. Paged, the verifier's memory is one page plus one
-- accumulator per (account, balance kind), which is a few hundred rows at guild scale whether the
-- log holds ten batches or ten million. Offset would be the other way to page and is banned
-- (.claude/rules/store-and-sql.md): it drifts, and every one of these has a unique key to seek on.
--
-- Every one of them is a SELECT. The verifier reads and never writes: the two tables are
-- append-only, and the cache rebuild the docs describe (`--rebuild`) is a separate job that does not
-- ship here.

-- ListPoolIDs enumerates the pools, in id order. The ledger hash chain is PER POOL, so a verifier
-- has to know the whole set - reading only the default pool would report a clean ledger while an
-- entire second pool's chain was broken.

-- name: ListPoolIDs :many
SELECT id FROM pool ORDER BY id;

-- ListBatchesAfterSeq is one page of a pool's batches in seq order, for the replay. It returns every
-- column the batch hash covers plus the two chain columns, because the verifier recomputes
-- SHA-256(prev_hash || canonical_json(batch without hash) || canonical_json(entries)) and compares it
-- to what is stored - a projection missing a column would be a hash computed over a batch that is
-- not the one on disk.
--
-- The cursor is `seq > ?` with ORDER BY seq, seeking ux_batch_seq(pool_id, seq): the unique index
-- makes seq its own tiebreak, so no second cursor column is needed here. Start at 0.

-- name: ListBatchesAfterSeq :many
SELECT id, pool_id, seq, kind, strategy_id, strategy_version, config_snapshot_json, rng_seed,
       source, source_ref, actor_user_id, actor_token_id, actor_is_beneficiary, reason,
       reverses_batch_id, effective_at, recorded_at, effective_day, idempotency_key,
       entry_count, net_amount_cp, prev_hash, hash
FROM ledger_batch
WHERE pool_id = ? AND seq > ?
ORDER BY seq
LIMIT ?;

-- ListEntriesByBatch reads one batch's entries, in ID ORDER, which is the order the batch hash is
-- computed over (docs/design/01-domain-model.md section 9.6). Sorting in SQL rather than in Go is
-- not an optimisation - the hash input is defined as `entries ORDER BY id`, so the order is part of
-- the attestation and belongs next to the read that produces it. internal/ledger sorts again anyway,
-- because BatchHash must not depend on its caller having read the rows in any particular order.
--
-- By batch_id, not by a seq range: ix_entry_batch(batch_id) makes this an index seek, and a batch
-- holds at most ~70 entries, so the page is bounded by the domain rather than by a LIMIT.

-- name: ListEntriesByBatch :many
SELECT id, batch_id, pool_id, seq, account_id, character_id, balance_kind, amount_cp,
       item_id, item_award_id, raid_id, tick_id, metadata_json
FROM ledger_entry
WHERE batch_id = ?
ORDER BY id;

-- ListSnapshotsAfter is one page of a pool's cached balances, walked in primary-key order so the
-- verifier can compare them against its fold without holding the whole cache in memory.
--
-- The cursor is a ROW VALUE - `(account_id, balance_kind) > (?, ?)` - which SQLite has supported
-- since 3.15 and which db/RECIPES.md prescribes over the `a > ? OR (a = ? AND b > ?)` expansion: it
-- is one seek into the WITHOUT ROWID primary key rather than a predicate the planner has to unpick.
-- balance_snapshot's PK is (pool_id, account_id, balance_kind), so this walks the table itself.
-- Start at ('', '') - the empty string sorts before every ULID.
--
-- The two cursor columns are NAMED (sqlc.arg) rather than positional, because sqlc names a
-- positional parameter after the column it is compared to: both halves of the row value would come
-- back as AccountID and AccountID_2, and a caller passing the balance kind to a field called
-- AccountID_2 is a caller one edit away from passing them in the wrong order.

-- name: ListSnapshotsAfter :many
SELECT account_id, balance_kind, amount_cp, as_of_seq, entry_count
FROM balance_snapshot
WHERE pool_id = sqlc.arg(pool_id)
  AND (account_id, balance_kind) > (sqlc.arg(cursor_account_id), sqlc.arg(cursor_balance_kind))
ORDER BY account_id, balance_kind
LIMIT sqlc.arg(page_limit);

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
