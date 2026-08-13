-- Audit log - who did this, with what authority, and to what. Phase 0 PR 10a.
--
-- Shapes follow db/RECIPES.md. Every statement that reaches SQLite in this project is generated from
-- a file like this one: db.Query and db.Exec outside internal/store are grep-banned (gate SQL002),
-- so the only way a new query enters the codebase is by being written here first and reviewed as SQL.
--
-- WRITE-ONLY at this phase, and deliberately so. The officer-facing forensic view is Phase 2: it
-- needs `audit.read`, and there is no authorization middleware yet, so shipping a reader now would
-- mean shipping an unauthenticated route over the one table that records who did what. What ships is
-- the seq allocator and the insert that ledger.Service.Commit calls in the same transaction as the
-- batch (domain model section 17.1: "committing a ledger batch also writes an audit row").
--
-- Keep every comment in this file ASCII-only. sqlc v1.31.1 computes each query's text span in bytes
-- but truncates by rune count, so a multibyte character (an em dash, a section sign) in a preceding
-- comment lops that many trailing characters off the generated query string. The failure is silent
-- at generate time and shows up as a syntax error only when the query runs.

-- NextAuditSeq allocates the next INSTANCE-WIDE audit sequence number. It MUST run inside store.Tx,
-- for the same reason NextPoolSeq must: the write pool is _txlock=immediate with SetMaxOpenConns(1),
-- so the write transaction is the only writer and max+1 cannot race. ux_audit_seq is the guardrail if
-- that single-writer property is ever lost.
--
-- Instance-wide, where the ledger's seq is per-pool. Gaplessness is what gives the audit hash chain
-- an ordering to hash over (domain model section 17), and it is the reason this is an allocator
-- rather than an AUTOINCREMENT column: event_outbox.event_seq must never reuse a number after a
-- prune, whereas audit_log.seq must never SKIP one. Those are different requirements and they need
-- different mechanisms.
--
-- Like NextPoolSeq this is dialect divergence #1 (db/RECIPES.md): max+1 is not safe on Postgres.

-- name: NextAuditSeq :one
SELECT CAST(COALESCE(max(seq), 0) + 1 AS INTEGER) AS next_seq FROM audit_log;

-- InsertAuditLog appends one row. There is no update and no delete here: trg_audit_log_no_update
-- aborts an UPDATE, and pruning is `dkp audit prune --before`, a Phase 2+ interactive command that
-- writes an audit_gap_marker rather than a silence. No endpoint deletes audit rows at any permission
-- level (domain model section 17).
--
-- prev_hash is NULL only at seq = 1; hash is SHA-256(prev_hash || canonical_json(row without hash)),
-- computed by internal/ledger/hashchain.go and mirrored into dkp_meta('audit_head') in the same
-- transaction.

-- name: InsertAuditLog :exec
INSERT INTO audit_log (
    id, seq, at, actor_kind, actor_label, action, resource_kind, resource_id,
    outcome, ledger_batch_id, prev_hash, hash
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?
);

-- ListAuditRowsAfterSeq is one page of the audit log in seq order, for `dkp verify-ledger` (Phase 1,
-- issue #198). It is the FIRST read of this table, and it is not the officer-facing forensic view
-- the header above defers to Phase 2: it is the chain verifier, it selects no PII the write path did
-- not already put in the row, and its only caller is a CLI command an operator runs against their
-- own database. The Phase 2 route still needs `audit.read` and still does not exist.
--
-- Every column the audit hash covers, plus the two chain columns: the verifier recomputes
-- SHA-256(prev_hash || canonical_json(row without hash)) and compares it to what is stored, so a
-- projection missing a column would be a hash computed over a row that is not the one on disk.
--
-- Keyset, `seq > ?` seeking ux_audit_seq, for the reason the ledger replay reads are keyset: an
-- instance-wide log has no bound, and a :many over all of it is the whole audit log in memory.
-- Start at 0.

-- name: ListAuditRowsAfterSeq :many
SELECT id, seq, at, actor_kind, actor_label, action, resource_kind, resource_id,
       outcome, ledger_batch_id, prev_hash, hash
FROM audit_log
WHERE seq > ?
ORDER BY seq
LIMIT ?;
