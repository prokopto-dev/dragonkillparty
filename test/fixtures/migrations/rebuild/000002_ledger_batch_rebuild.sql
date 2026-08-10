-- A CORRECT SQLite 12-step table rebuild of ledger_batch — the PARENT of the ledger's two tables.
-- Test fixture only; it must never be moved into db/migrations-sqlite/.
--
-- The one line that makes it work is the NO TRANSACTION annotation below, and it is not decoration.
-- Read .claude/rules/migrations.md, "Rebuilding a table that has children", before copying this
-- file. (The annotation is deliberately not quoted anywhere in this comment block: goose's parser
-- treats ANY comment line carrying its annotation marker as a directive, so writing the directive
-- out in prose makes the migration fail to parse.)
--
-- Atlas wraps every 12-step rebuild in `PRAGMA foreign_keys = off` … `on`. SQLite SILENTLY IGNORES
-- that pragma inside a transaction, and goose runs each migration in one — so on the ordinary path
-- the pragma is a no-op, `DROP TABLE ledger_batch` runs with foreign keys enforced, ledger_entry
-- still references it, and the migration raises `FOREIGN KEY constraint failed (787)` on every
-- POPULATED database while passing every fresh-install check. `PRAGMA defer_foreign_keys` does not
-- help: deferred enforcement counts violating operations, it does not re-scan at COMMIT, so
-- dropping the parent and re-creating it with identical rows never decrements the counter.
--
-- NO TRANSACTION takes goose's transaction away, which is what lets the pragma take effect. The
-- cost is per-migration atomicity, and the reason that cost is affordable here is that the boot path
-- does not depend on it: internal/migrate snapshots with VACUUM INTO before applying anything and
-- restores the snapshot on any failure. The recovery story is the snapshot, not the transaction.
--
-- Its canary twin, 000002_ledger_batch_rebuild_in_transaction.sql, is this file with the NO
-- TRANSACTION annotation removed and nothing else changed. It fails, on purpose, and
-- TestMigrate_FullStack_AtlasShapedBatchRebuildFailsOnPopulatedData is what stops that from
-- becoming a claim nobody has watched come true.

-- +goose NO TRANSACTION
-- +goose Up
-- disable the enforcement of foreign-keys constraints
--
-- Load-bearing here, unlike in the ledger_entry fixture next door: with no transaction around this
-- migration the pragma actually takes effect, and it is the only thing that lets the DROP below
-- remove a table four ledger_entry rows point at.
PRAGMA foreign_keys = off;
-- create "new_ledger_batch" table
CREATE TABLE "new_ledger_batch" ("id" text NOT NULL, "pool_id" text NOT NULL, "seq" integer NOT NULL, "kind" text NOT NULL, "strategy_id" text NOT NULL, "strategy_version" text NOT NULL, "config_snapshot_json" text NOT NULL DEFAULT '{}', "rng_seed" integer NULL, "source" text NOT NULL, "source_ref" text NULL, "actor_user_id" text NULL, "actor_token_id" text NULL, "actor_is_beneficiary" integer NOT NULL DEFAULT 0, "reason" text NOT NULL DEFAULT '', "reverses_batch_id" text NULL, "effective_at" integer NOT NULL, "recorded_at" integer NOT NULL, "effective_day" text NOT NULL, "idempotency_key" text NULL, "entry_count" integer NOT NULL, "net_amount_cp" integer NOT NULL, "prev_hash" blob NULL, "hash" blob NOT NULL, "memo" text NOT NULL DEFAULT '', PRIMARY KEY ("id"), CONSTRAINT "ledger_batch_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_batch_reverses" FOREIGN KEY ("reverses_batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_batch_kind_enum" CHECK (kind IN ('attendance', 'award', 'adjustment', 'decay', 'cap', 'start_points', 'zero_sum_credit', 'reversal', 'correction', 're_attribution', 'migration', 'import', 'seed', 'write_off')), CONSTRAINT "ledger_batch_source_enum" CHECK (source IN ('web', 'api', 'discord', 'parser', 'import', 'system')), CONSTRAINT "ledger_batch_actor_is_beneficiary_bool" CHECK (actor_is_beneficiary IN (0, 1)), CONSTRAINT "ledger_batch_entry_count_positive" CHECK (entry_count > 0), CONSTRAINT "ledger_batch_times_nonneg" CHECK (recorded_at >= 0 AND effective_at >= 0), CONSTRAINT "ledger_batch_memo_length" CHECK (length(memo) <= 500)) STRICT;
-- copy rows from old table "ledger_batch" to new temporary table "new_ledger_batch"
INSERT INTO "new_ledger_batch" ("id", "pool_id", "seq", "kind", "strategy_id", "strategy_version", "config_snapshot_json", "rng_seed", "source", "source_ref", "actor_user_id", "actor_token_id", "actor_is_beneficiary", "reason", "reverses_batch_id", "effective_at", "recorded_at", "effective_day", "idempotency_key", "entry_count", "net_amount_cp", "prev_hash", "hash") SELECT "id", "pool_id", "seq", "kind", "strategy_id", "strategy_version", "config_snapshot_json", "rng_seed", "source", "source_ref", "actor_user_id", "actor_token_id", "actor_is_beneficiary", "reason", "reverses_batch_id", "effective_at", "recorded_at", "effective_day", "idempotency_key", "entry_count", "net_amount_cp", "prev_hash", "hash" FROM "ledger_batch";
-- drop "ledger_batch" table after copying rows
DROP TABLE "ledger_batch";
-- rename temporary table "new_ledger_batch" to "ledger_batch"
ALTER TABLE "new_ledger_batch" RENAME TO "ledger_batch";
-- create index "ux_batch_seq" to table: "ledger_batch"
CREATE UNIQUE INDEX "ux_batch_seq" ON "ledger_batch" ("pool_id", "seq");
-- create index "ux_batch_srcref" to table: "ledger_batch"
CREATE UNIQUE INDEX "ux_batch_srcref" ON "ledger_batch" ("pool_id", "source_ref") WHERE source_ref IS NOT NULL;
-- create index "ux_batch_idem" to table: "ledger_batch"
CREATE UNIQUE INDEX "ux_batch_idem" ON "ledger_batch" ("pool_id", "idempotency_key") WHERE idempotency_key IS NOT NULL;
-- create index "ux_batch_reverses" to table: "ledger_batch"
CREATE UNIQUE INDEX "ux_batch_reverses" ON "ledger_batch" ("reverses_batch_id") WHERE reverses_batch_id IS NOT NULL;
-- create index "ix_batch_effective" to table: "ledger_batch"
CREATE INDEX "ix_batch_effective" ON "ledger_batch" ("pool_id", "effective_at");
-- create index "ix_batch_kind" to table: "ledger_batch"
CREATE INDEX "ix_batch_kind" ON "ledger_batch" ("pool_id", "kind", "seq");
-- create index "ix_batch_selfdeal" to table: "ledger_batch"
CREATE INDEX "ix_batch_selfdeal" ON "ledger_batch" ("actor_is_beneficiary", "recorded_at") WHERE actor_is_beneficiary = 1;
-- enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

-- HAND-APPENDED under .claude/rules/migrations.md case 1 (append-only triggers).
--
-- The rebuild above dropped these with the old table. Re-creating them here, in the same file and
-- after the rename, is what the rule requires of any migration that rebuilds a table carrying a
-- trigger. The bodies are copied verbatim from db/migrations-sqlite/000003_ledger.sql — including
-- the RAISE message, which is what an operator reads and what
-- internal/ledger/trigger_test.go asserts on.
CREATE TRIGGER trg_ledger_batch_no_update BEFORE UPDATE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;
CREATE TRIGGER trg_ledger_batch_no_delete BEFORE DELETE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;

-- +goose Down
-- Forward-only, as every migration in this project is. See db/migrations-sqlite/000001_init.sql.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
