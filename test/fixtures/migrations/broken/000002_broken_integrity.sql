-- A DELIBERATELY BROKEN MIGRATION. It is a test fixture and must never be moved into
-- db/migrations-sqlite/.
--
-- It exists to prove that TestMigrate_BrokenMigration_RestoresByteIdentical actually fires. A
-- restore path that has never been observed restoring anything is decoration, and this is the one
-- piece of the product where "we think it works" is not good enough: it runs unattended, on an
-- officer's only copy of ten years of guild data, at whatever hour they happened to restart.
--
-- The failure it induces is not synthetic. Turning off CHECK enforcement to make a backfill "just
-- go through" is a real thing people do, it is exactly what .claude/rules/migrations.md warns
-- against, and its signature is the nastiest kind: every statement succeeds, goose records the
-- migration as applied, the database opens fine, queries return rows — and PRAGMA integrity_check
-- reports a constraint violation that will never repair itself.
--
-- Do NOT "fix" this file. If a change makes this migration stop failing integrity_check, the test
-- that depends on it silently stops testing anything, which is worse than the test not existing.

-- +goose Up
CREATE TABLE "broken_ledger" (
    "id"        text    NOT NULL,
    "amount_cp" integer NOT NULL CHECK (amount_cp <> 0),
    PRIMARY KEY ("id")
) STRICT;

-- The backfill that ruins it. With ignore_check_constraints on, SQLite accepts a row the CHECK
-- forbids; integrity_check finds it afterwards and reports "CHECK constraint failed in
-- broken_ledger".
PRAGMA ignore_check_constraints = ON;
INSERT INTO "broken_ledger" ("id", "amount_cp") VALUES ('01JQZ0000000000000000BROKE', 0);
PRAGMA ignore_check_constraints = OFF;

-- +goose Down
-- Forward-only, as every migration in this project is. See db/migrations-sqlite/000001_init.sql.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
