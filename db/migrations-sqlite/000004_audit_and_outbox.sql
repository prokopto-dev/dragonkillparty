-- +goose Up
-- create "audit_log" table
CREATE TABLE "audit_log" ("id" text NOT NULL, "seq" integer NOT NULL, "at" integer NOT NULL, "actor_kind" text NOT NULL, "actor_label" text NOT NULL DEFAULT '', "action" text NOT NULL, "resource_kind" text NOT NULL, "resource_id" text NULL, "outcome" text NOT NULL, "ledger_batch_id" text NULL, "prev_hash" blob NULL, "hash" blob NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "audit_log_batch" FOREIGN KEY ("ledger_batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "audit_log_actor_kind_enum" CHECK (actor_kind IN ('user', 'service_account', 'system', 'boot', 'import', 'anonymous')), CONSTRAINT "audit_log_outcome_enum" CHECK (outcome IN ('success', 'denied', 'error'))) STRICT;
-- create index "ux_audit_seq" to table: "audit_log"
CREATE UNIQUE INDEX "ux_audit_seq" ON "audit_log" ("seq");
-- create index "ix_audit_at" to table: "audit_log"
CREATE INDEX "ix_audit_at" ON "audit_log" ("at" DESC);
-- create "event_outbox" table
CREATE TABLE "event_outbox" ("event_seq" integer NOT NULL PRIMARY KEY AUTOINCREMENT, "id" text NOT NULL, "topic" text NOT NULL, "event_type" text NOT NULL, "resource_ref" text NOT NULL, "created_at" integer NOT NULL) STRICT;
-- create index "ix_outbox_topic" to table: "event_outbox"
CREATE INDEX "ix_outbox_topic" ON "event_outbox" ("topic", "event_seq");


-- HAND-APPENDED under .claude/rules/migrations.md case 1 (append-only triggers).
--
-- Atlas community cannot express a trigger at all, so this one is hand-written here, exactly as the
-- ledger's four are in 000003_ledger.sql. The fresh-install fingerprint
-- (test/golden/migrations/fresh_install_fingerprint.txt) covers sqlite_schema rows of every type,
-- triggers included, and is the backstop that notices if a future 12-step rebuild drops it.
--
-- UPDATE only, and the ABSENT delete trigger is the deliberate half. An audit row is prunable by
-- retention (domain model section 17: dkp audit prune --before, which leaves an audit_gap_marker
-- scar rather than a silence), so a no-delete trigger would have to be dropped in order to run the
-- prune -- and a guardrail that gets dropped during normal operation is not a guardrail. What must
-- never happen is an audit row being EDITED: that is how a forensic record becomes fiction, and it
-- is what this trigger forbids. The ledger's tables get BOTH triggers because a ledger row is never
-- removed at all; the asymmetry is the difference section 17.1 tabulates, not an oversight.
--
-- TestTriggers_MutatingAuditLog_Raises asserts it fires, so the guardrail cannot be silently
-- regressed by a future migration.
CREATE TRIGGER trg_audit_log_no_update BEFORE UPDATE ON audit_log
  BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
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
