-- A CORRECT SQLite 12-step table rebuild of a ledger table. Test fixture only; it must never be
-- moved into db/migrations-sqlite/.
--
-- It stands in for the migration shape Phase 1 will eventually need and that .claude/rules/
-- migrations.md calls the one shape which silently destroys the append-only guarantee: SQLite's
-- ALTER TABLE cannot add a table-level CHECK, so a change like the one below becomes create-copy-
-- drop-rename. The DROP TABLE takes every index and EVERY TRIGGER attached to the old table with
-- it, and re-creates nothing. Atlas re-creates the indexes because it can see them in schema.hcl;
-- the community edition cannot express a trigger at all, so the trigger re-creation at the bottom
-- of this file is a hand-edit under case 1 of the rule, and forgetting it is invisible in review.
--
-- This fixture DOES re-create them. Its forgetful twin, 000002_ledger_entry_rebuild_no_triggers.sql,
-- is identical except that the two CREATE TRIGGER statements are missing; the two exist as a matched
-- pair so `diff` shows exactly what a real migration would have to get wrong. That relationship is
-- enforced by TestFixtures_RebuildPairs_DifferOnlyByTheDeclaredStatements — edit one of these files
-- and you edit both, in the same commit, or the negative control stops controlling for the same
-- rebuild and the positive test's assertions stop being about anything.
--
-- The rebuild targets ledger_entry, the CHILD of the ledger's two tables, and that is deliberate.
-- goose runs each migration inside a transaction, and `PRAGMA foreign_keys` is silently ignored
-- inside one — so the `off`/`on` pair Atlas emits around a rebuild is a no-op here and the DROP
-- TABLE really does run with foreign keys enforced. Dropping a child is safe under that; dropping
-- ledger_batch, which ledger_entry references, would raise a constraint violation instead and fail
-- loudly. The silent failure mode is the one worth testing.

-- +goose Up
-- disable the enforcement of foreign-keys constraints
--
-- Kept because this is what Atlas emits and what a real generated migration will contain. It has no
-- effect inside goose's transaction; see the header. It is NOT what makes the drop below safe.
PRAGMA foreign_keys = off;
-- create "new_ledger_entry" table
CREATE TABLE "new_ledger_entry" ("id" text NOT NULL, "batch_id" text NOT NULL, "pool_id" text NOT NULL, "seq" integer NOT NULL, "account_id" text NOT NULL, "character_id" text NULL, "balance_kind" text NOT NULL, "amount_cp" integer NOT NULL, "item_id" text NULL, "item_award_id" text NULL, "raid_id" text NULL, "tick_id" text NULL, "metadata_json" text NOT NULL DEFAULT '{}', "note" text NOT NULL DEFAULT '', PRIMARY KEY ("id"), CONSTRAINT "ledger_entry_batch" FOREIGN KEY ("batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_entry_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_entry_account" FOREIGN KEY ("account_id") REFERENCES "account" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_entry_amount_nonzero" CHECK (amount_cp <> 0), CONSTRAINT "ledger_entry_note_length" CHECK (length(note) <= 500)) STRICT;
-- copy rows from old table "ledger_entry" to new temporary table "new_ledger_entry"
INSERT INTO "new_ledger_entry" ("id", "batch_id", "pool_id", "seq", "account_id", "character_id", "balance_kind", "amount_cp", "item_id", "item_award_id", "raid_id", "tick_id", "metadata_json") SELECT "id", "batch_id", "pool_id", "seq", "account_id", "character_id", "balance_kind", "amount_cp", "item_id", "item_award_id", "raid_id", "tick_id", "metadata_json" FROM "ledger_entry";
-- drop "ledger_entry" table after copying rows
DROP TABLE "ledger_entry";
-- rename temporary table "new_ledger_entry" to "ledger_entry"
ALTER TABLE "new_ledger_entry" RENAME TO "ledger_entry";
-- create index "ix_entry_balance" to table: "ledger_entry"
CREATE INDEX "ix_entry_balance" ON "ledger_entry" ("pool_id", "account_id", "balance_kind", "seq", "amount_cp");
-- create index "ix_entry_batch" to table: "ledger_entry"
CREATE INDEX "ix_entry_batch" ON "ledger_entry" ("batch_id");
-- create index "ix_entry_stmt" to table: "ledger_entry"
CREATE INDEX "ix_entry_stmt" ON "ledger_entry" ("account_id", "pool_id", "seq");
-- create index "ix_entry_item" to table: "ledger_entry"
CREATE INDEX "ix_entry_item" ON "ledger_entry" ("item_id") WHERE item_id IS NOT NULL;
-- enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

-- HAND-APPENDED under .claude/rules/migrations.md case 1 (append-only triggers).
--
-- The rebuild above dropped these with the old table. Re-creating them here, in the same file and
-- after the rename, is what the rule requires of any migration that rebuilds a table carrying a
-- trigger. The bodies are copied verbatim from db/migrations-sqlite/000003_ledger.sql — including
-- the RAISE message, which is what an operator reads and what
-- internal/ledger/trigger_test.go asserts on.
CREATE TRIGGER trg_ledger_entry_no_update BEFORE UPDATE ON ledger_entry
  BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;
CREATE TRIGGER trg_ledger_entry_no_delete BEFORE DELETE ON ledger_entry
  BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;

-- +goose Down
-- Forward-only, as every migration in this project is. See db/migrations-sqlite/000001_init.sql.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
