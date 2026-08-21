-- +goose Up
-- disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- create index "ix_snapshot_account" to table: "balance_snapshot"
CREATE INDEX "ix_snapshot_account" ON "balance_snapshot" ("account_id");
-- create "new_pool_config_change" table
CREATE TABLE "new_pool_config_change" ("id" text NOT NULL, "pool_id" text NOT NULL, "changed_at" integer NOT NULL, "changed_by" text NULL, "from_strategy_id" text NOT NULL, "from_strategy_version" text NOT NULL, "from_config_json" text NOT NULL, "to_strategy_id" text NOT NULL, "to_strategy_version" text NOT NULL, "to_config_json" text NOT NULL, "reason" text NOT NULL DEFAULT '', "migration_batch_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "pool_config_change_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "pool_config_change_changed_by" FOREIGN KEY ("changed_by") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, CONSTRAINT "pool_config_change_batch" FOREIGN KEY ("migration_batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION) STRICT;
-- copy rows from old table "pool_config_change" to new temporary table "new_pool_config_change"
INSERT INTO "new_pool_config_change" ("id", "pool_id", "changed_at", "changed_by", "from_strategy_id", "from_strategy_version", "from_config_json", "to_strategy_id", "to_strategy_version", "to_config_json", "reason", "migration_batch_id") SELECT "id", "pool_id", "changed_at", "changed_by", "from_strategy_id", "from_strategy_version", "from_config_json", "to_strategy_id", "to_strategy_version", "to_config_json", "reason", "migration_batch_id" FROM "pool_config_change";
-- drop "pool_config_change" table after copying rows
DROP TABLE "pool_config_change";
-- rename temporary table "new_pool_config_change" to "pool_config_change"
ALTER TABLE "new_pool_config_change" RENAME TO "pool_config_change";
-- create index "ix_pcc_pool" to table: "pool_config_change"
CREATE INDEX "ix_pcc_pool" ON "pool_config_change" ("pool_id", "changed_at" DESC);
-- create index "ix_pcc_changed_by" to table: "pool_config_change"
CREATE INDEX "ix_pcc_changed_by" ON "pool_config_change" ("changed_by");
-- create "new_decay_run" table
CREATE TABLE "new_decay_run" ("id" text NOT NULL, "pool_id" text NOT NULL, "kind" text NOT NULL, "cadence_period" text NOT NULL, "scheduled_for_at" integer NOT NULL, "executed_at" integer NULL, "state" text NOT NULL DEFAULT 'planned', "dry_run_result_json" text NOT NULL DEFAULT '{}', "config_snapshot_json" text NOT NULL DEFAULT '{}', "ledger_batch_id" text NULL, "triggered_by" text NULL, "error" text NOT NULL DEFAULT '', "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "decay_run_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "decay_run_batch" FOREIGN KEY ("ledger_batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "decay_run_triggered_by" FOREIGN KEY ("triggered_by") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, CONSTRAINT "decay_run_kind_enum" CHECK (kind IN ('decay', 'cap', 'start_points')), CONSTRAINT "decay_run_state_enum" CHECK (state IN ('planned', 'preview', 'committed', 'skipped', 'failed')), CONSTRAINT "decay_run_times_nonneg" CHECK (scheduled_for_at >= 0 AND created_at >= 0 AND updated_at >= 0)) STRICT;
-- copy rows from old table "decay_run" to new temporary table "new_decay_run"
INSERT INTO "new_decay_run" ("id", "pool_id", "kind", "cadence_period", "scheduled_for_at", "executed_at", "state", "dry_run_result_json", "config_snapshot_json", "ledger_batch_id", "triggered_by", "error", "created_at", "updated_at") SELECT "id", "pool_id", "kind", "cadence_period", "scheduled_for_at", "executed_at", "state", "dry_run_result_json", "config_snapshot_json", "ledger_batch_id", "triggered_by", "error", "created_at", "updated_at" FROM "decay_run";
-- drop "decay_run" table after copying rows
DROP TABLE "decay_run";
-- rename temporary table "new_decay_run" to "decay_run"
ALTER TABLE "new_decay_run" RENAME TO "decay_run";
-- create index "ux_decay_period" to table: "decay_run"
CREATE UNIQUE INDEX "ux_decay_period" ON "decay_run" ("pool_id", "kind", "cadence_period");
-- create index "ix_decay_pool" to table: "decay_run"
CREATE INDEX "ix_decay_pool" ON "decay_run" ("pool_id", "scheduled_for_at");
-- create index "ix_decay_triggered_by" to table: "decay_run"
CREATE INDEX "ix_decay_triggered_by" ON "decay_run" ("triggered_by");
-- create "new_role_assignment" table
CREATE TABLE "new_role_assignment" ("id" text NOT NULL, "subject_kind" text NOT NULL, "subject_id" text NOT NULL, "role_id" text NOT NULL, "scope_type" text NOT NULL DEFAULT 'global', "scope_id" text NULL, "suspended_until_at" integer NULL, "granted_by" text NULL, "granted_via" text NOT NULL DEFAULT 'manual', "expires_at" integer NULL, "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "role_assignment_role" FOREIGN KEY ("role_id") REFERENCES "role" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "role_assignment_granted_by" FOREIGN KEY ("granted_by") REFERENCES "app_user" ("id") ON UPDATE NO ACTION ON DELETE SET NULL, CONSTRAINT "role_assignment_subject_kind_enum" CHECK (subject_kind IN ('user', 'service_account')), CONSTRAINT "role_assignment_scope_type_enum" CHECK (scope_type IN ('global', 'pool', 'raid_group')), CONSTRAINT "role_assignment_granted_via_enum" CHECK (granted_via IN ('manual', 'invitation', 'discord_sync', 'import', 'bootstrap')), CONSTRAINT "role_assignment_scope_shape" CHECK ((scope_type = 'global') = (scope_id IS NULL))) STRICT;
-- copy rows from old table "role_assignment" to new temporary table "new_role_assignment"
INSERT INTO "new_role_assignment" ("id", "subject_kind", "subject_id", "role_id", "scope_type", "scope_id", "suspended_until_at", "granted_by", "granted_via", "expires_at", "created_at", "updated_at") SELECT "id", "subject_kind", "subject_id", "role_id", "scope_type", "scope_id", "suspended_until_at", "granted_by", "granted_via", "expires_at", "created_at", "updated_at" FROM "role_assignment";
-- drop "role_assignment" table after copying rows
DROP TABLE "role_assignment";
-- rename temporary table "new_role_assignment" to "role_assignment"
ALTER TABLE "new_role_assignment" RENAME TO "role_assignment";
-- create index "ux_role_assign" to table: "role_assignment"
CREATE UNIQUE INDEX "ux_role_assign" ON "role_assignment" ("subject_kind", "subject_id", "role_id", "scope_type", (COALESCE(scope_id, '')));
-- create index "ix_role_assign_subject" to table: "role_assignment"
CREATE INDEX "ix_role_assign_subject" ON "role_assignment" ("subject_kind", "subject_id");
-- create index "ix_role_assign_role" to table: "role_assignment"
CREATE INDEX "ix_role_assign_role" ON "role_assignment" ("role_id");
-- create index "ix_role_assign_granted_by" to table: "role_assignment"
CREATE INDEX "ix_role_assign_granted_by" ON "role_assignment" ("granted_by");
-- drop index "ix_session_user_active" from table: "session"
DROP INDEX "ix_session_user_active";
-- create index "ix_session_user" to table: "session"
CREATE INDEX "ix_session_user" ON "session" ("user_id", "expires_at");
-- create index "ix_session_identity" to table: "session"
CREATE INDEX "ix_session_identity" ON "session" ("identity_id");
-- create index "ix_service_account_owner" to table: "service_account"
CREATE INDEX "ix_service_account_owner" ON "service_account" ("owner_user_id");
-- create index "ix_service_account_creator" to table: "service_account"
CREATE INDEX "ix_service_account_creator" ON "service_account" ("created_by");
-- drop index "ix_api_token_sa" from table: "api_token"
DROP INDEX "ix_api_token_sa";
-- create index "ix_api_token_service_account" to table: "api_token"
CREATE INDEX "ix_api_token_service_account" ON "api_token" ("service_account_id");
-- create index "ix_api_token_creator" to table: "api_token"
CREATE INDEX "ix_api_token_creator" ON "api_token" ("created_by");
-- create index "ix_api_token_revoker" to table: "api_token"
CREATE INDEX "ix_api_token_revoker" ON "api_token" ("revoked_by");
-- create index "ix_feed_token_user" to table: "feed_token"
CREATE INDEX "ix_feed_token_user" ON "feed_token" ("user_id");
-- enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;

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
