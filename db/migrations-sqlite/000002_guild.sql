-- +goose Up
-- create "guild" table
CREATE TABLE "guild" ("id" integer NOT NULL, "name" text NOT NULL, "tag" text NOT NULL DEFAULT '', "timezone" text NOT NULL DEFAULT 'UTC', "week_start" integer NOT NULL DEFAULT 1, "points_label" text NOT NULL DEFAULT 'DKP', "points_precision" integer NOT NULL DEFAULT 2, "inactive_after_days" integer NULL, "auto_set_inactive" integer NOT NULL DEFAULT 0, "hide_inactive" integer NOT NULL DEFAULT 0, "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "guild_is_singleton" CHECK (id = 1), CONSTRAINT "guild_week_start_range" CHECK (week_start BETWEEN 0 AND 6), CONSTRAINT "guild_points_precision_range" CHECK (points_precision BETWEEN 0 AND 2), CONSTRAINT "guild_auto_set_inactive_bool" CHECK (auto_set_inactive IN (0, 1)), CONSTRAINT "guild_hide_inactive_bool" CHECK (hide_inactive IN (0, 1))) STRICT;

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
