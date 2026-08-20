-- +goose Up
-- create "permission" table
CREATE TABLE "permission" ("key" text NOT NULL, "category" text NOT NULL, "label" text NOT NULL, "description" text NOT NULL, "is_dangerous" integer NOT NULL DEFAULT 0, "requires_step_up" integer NOT NULL DEFAULT 0, "orphaned_at" integer NULL, "sort_order" integer NOT NULL DEFAULT 0, PRIMARY KEY ("key"), CONSTRAINT "permission_is_dangerous_bool" CHECK (is_dangerous IN (0, 1)), CONSTRAINT "permission_requires_step_up_bool" CHECK (requires_step_up IN (0, 1))) WITHOUT ROWID, STRICT;
-- create "role" table
CREATE TABLE "role" ("id" text NOT NULL, "key" text NULL, "name" text NOT NULL, "name_norm" text NOT NULL, "description" text NOT NULL DEFAULT '', "is_builtin" integer NOT NULL DEFAULT 0, "applies_to" text NOT NULL DEFAULT 'both', "sort_order" integer NOT NULL DEFAULT 0, "deleted_at" integer NULL, "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "role_applies_to_enum" CHECK (applies_to IN ('user', 'service_account', 'both')), CONSTRAINT "role_is_builtin_bool" CHECK (is_builtin IN (0, 1))) STRICT;
-- create index "ux_role_key" to table: "role"
CREATE UNIQUE INDEX "ux_role_key" ON "role" ("key") WHERE key IS NOT NULL;
-- create index "ux_role_name" to table: "role"
CREATE UNIQUE INDEX "ux_role_name" ON "role" ("name_norm") WHERE deleted_at IS NULL;
-- create "role_permission" table
CREATE TABLE "role_permission" ("role_id" text NOT NULL, "permission_key" text NOT NULL, PRIMARY KEY ("role_id", "permission_key"), CONSTRAINT "role_permission_role" FOREIGN KEY ("role_id") REFERENCES "role" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "role_permission_permission" FOREIGN KEY ("permission_key") REFERENCES "permission" ("key") ON UPDATE NO ACTION ON DELETE NO ACTION) WITHOUT ROWID, STRICT;
-- create "role_assignment" table
CREATE TABLE "role_assignment" ("id" text NOT NULL, "subject_kind" text NOT NULL, "subject_id" text NOT NULL, "role_id" text NOT NULL, "scope_type" text NOT NULL DEFAULT 'global', "scope_id" text NULL, "suspended_until_at" integer NULL, "granted_by" text NULL, "granted_via" text NOT NULL DEFAULT 'manual', "expires_at" integer NULL, "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "role_assignment_role" FOREIGN KEY ("role_id") REFERENCES "role" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "role_assignment_subject_kind_enum" CHECK (subject_kind IN ('user', 'service_account')), CONSTRAINT "role_assignment_scope_type_enum" CHECK (scope_type IN ('global', 'pool', 'raid_group')), CONSTRAINT "role_assignment_granted_via_enum" CHECK (granted_via IN ('manual', 'invitation', 'discord_sync', 'import', 'bootstrap')), CONSTRAINT "role_assignment_scope_shape" CHECK ((scope_type = 'global') = (scope_id IS NULL))) STRICT;
-- create index "ux_role_assign" to table: "role_assignment"
CREATE UNIQUE INDEX "ux_role_assign" ON "role_assignment" ("subject_kind", "subject_id", "role_id", "scope_type", (COALESCE(scope_id, '')));
-- create index "ix_role_assign_subject" to table: "role_assignment"
CREATE INDEX "ix_role_assign_subject" ON "role_assignment" ("subject_kind", "subject_id");

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
