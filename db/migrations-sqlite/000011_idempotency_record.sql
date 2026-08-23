-- +goose Up
-- create "idempotency_record" table
CREATE TABLE "idempotency_record" ("id" text NOT NULL, "principal_ref" text NOT NULL, "key" text NOT NULL, "method" text NOT NULL, "path_template" text NOT NULL, "request_hash" blob NOT NULL, "state" text NOT NULL, "response_status" integer NULL, "response_headers_json" text NOT NULL DEFAULT '{}', "response_body" blob NULL, "created_at" integer NOT NULL, "expires_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "idempotency_record_state_enum" CHECK (state IN ('in_flight', 'complete'))) STRICT;
-- create index "ux_idempotency_record_principal_key" to table: "idempotency_record"
CREATE UNIQUE INDEX "ux_idempotency_record_principal_key" ON "idempotency_record" ("principal_ref", "key");
-- create index "ix_idempotency_record_expiry" to table: "idempotency_record"
CREATE INDEX "ix_idempotency_record_expiry" ON "idempotency_record" ("expires_at");

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
