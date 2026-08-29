-- +goose Up
-- create "bootstrap_token" table
CREATE TABLE "bootstrap_token" ("id" text NOT NULL, "token_hash" blob NOT NULL, "expires_at" integer NOT NULL, "attempts" integer NOT NULL DEFAULT 0, "consumed_at" integer NULL, "created_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "bootstrap_token_attempts_non_negative" CHECK (attempts >= 0)) STRICT;
-- create index "ux_bootstrap_token_hash" to table: "bootstrap_token"
CREATE UNIQUE INDEX "ux_bootstrap_token_hash" ON "bootstrap_token" ("token_hash");
-- create index "ix_bootstrap_token_created" to table: "bootstrap_token"
CREATE INDEX "ix_bootstrap_token_created" ON "bootstrap_token" ("created_at");

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
