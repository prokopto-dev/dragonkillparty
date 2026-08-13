-- +goose Up
-- create "pool_config_change" table
CREATE TABLE "pool_config_change" ("id" text NOT NULL, "pool_id" text NOT NULL, "changed_at" integer NOT NULL, "changed_by" text NULL, "from_strategy_id" text NOT NULL, "from_strategy_version" text NOT NULL, "from_config_json" text NOT NULL, "to_strategy_id" text NOT NULL, "to_strategy_version" text NOT NULL, "to_config_json" text NOT NULL, "reason" text NOT NULL DEFAULT '', "migration_batch_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "pool_config_change_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "pool_config_change_batch" FOREIGN KEY ("migration_batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION) STRICT;
-- create index "ix_pcc_pool" to table: "pool_config_change"
CREATE INDEX "ix_pcc_pool" ON "pool_config_change" ("pool_id", "changed_at" DESC);
-- create "decay_run" table
CREATE TABLE "decay_run" ("id" text NOT NULL, "pool_id" text NOT NULL, "cadence_period" text NOT NULL, "scheduled_for_at" integer NOT NULL, "executed_at" integer NULL, "state" text NOT NULL DEFAULT 'planned', "dry_run_result_json" text NOT NULL DEFAULT '{}', "config_snapshot_json" text NOT NULL DEFAULT '{}', "ledger_batch_id" text NULL, "triggered_by" text NULL, "error" text NOT NULL DEFAULT '', "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "decay_run_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "decay_run_batch" FOREIGN KEY ("ledger_batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "decay_run_state_enum" CHECK (state IN ('planned', 'preview', 'committed', 'skipped', 'failed')), CONSTRAINT "decay_run_times_nonneg" CHECK (scheduled_for_at >= 0 AND created_at >= 0 AND updated_at >= 0)) STRICT;
-- create index "ux_decay_period" to table: "decay_run"
CREATE UNIQUE INDEX "ux_decay_period" ON "decay_run" ("pool_id", "cadence_period");
-- create index "ix_decay_pool" to table: "decay_run"
CREATE INDEX "ix_decay_pool" ON "decay_run" ("pool_id", "scheduled_for_at");

-- +goose Down
-- Forward-only. This project ships no down migrations, ever: a down migration is code that runs
-- exactly once, in an emergency, on data that cannot be reproduced, written months earlier by
-- someone who never tested it against your database. Recovery is restoring the snapshot taken
-- immediately before this migration ran:
--
--     /data/backups/pre-<version>-<timestamp>.db.zst
--
-- The statement below aborts if goose is ever asked to run this block. Note that SQLite discards
-- RAISE()'s message outside a trigger body and reports "RAISE() may only be used within a
-- trigger-program" instead, so the path above — not the string below — is what an operator can
-- actually read.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
