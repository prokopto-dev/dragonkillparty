-- dkp:fixture deliberately-broken — never repair this file.
--
-- The FORGETFUL twin of 000002_ledger_entry_rebuild.sql. Test fixture only; it must never be moved
-- into db/migrations-sqlite/.
--
-- Identical to that file except that the two CREATE TRIGGER statements at the bottom are missing —
-- which is precisely the mistake a real migration makes, because Atlas cannot see a trigger, does
-- not emit one, and therefore never reminds anyone. Diff the two files: the whole of the append-only
-- guarantee is the part that is absent here.
--
-- "Identical except" is enforced, not asserted in prose:
-- TestFixtures_RebuildPairs_DifferOnlyByTheDeclaredStatements (test/repo/fixture_pairs_test.go)
-- strips those two statements from the correct file and requires the remainder to equal this one,
-- statement for statement. The comments differ and are meant to; nothing else may.
--
-- It exists to be the NEGATIVE CONTROL for
-- TestMigrate_FullStack_LedgerDataSurvivesUpgrade. A test that asserts "the triggers still fire
-- after an upgrade" is worth nothing unless something proves the assertion can fail, and the only
-- honest proof is a migration that really does lose them. Applying this one leaves a database where
-- ledger history is editable and NOTHING has gone red — no error, no warning, a clean
-- PRAGMA integrity_check and a clean PRAGMA foreign_key_check. That is the whole finding, made
-- reproducible.
--
-- Never repair this file. If a change makes it stop losing the triggers, the negative control has
-- stopped controlling for anything and the paired test will say so.

-- +goose Up
-- disable the enforcement of foreign-keys constraints
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

-- And here is where trg_ledger_entry_no_update and trg_ledger_entry_no_delete are NOT re-created.

-- +goose Down
-- Forward-only, as every migration in this project is. See db/migrations-sqlite/000001_init.sql.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
