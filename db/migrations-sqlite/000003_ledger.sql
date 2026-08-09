-- +goose Up
-- create "pool" table
CREATE TABLE "pool" ("id" text NOT NULL, "name" text NOT NULL, "name_norm" text NOT NULL, "strategy_id" text NOT NULL, "strategy_version" text NOT NULL, "balance_kinds" text NOT NULL DEFAULT 'dkp', "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id")) STRICT;
-- create index "ux_pool_name" to table: "pool"
CREATE UNIQUE INDEX "ux_pool_name" ON "pool" ("name_norm");
-- create "account" table
CREATE TABLE "account" ("id" text NOT NULL, "kind" text NOT NULL, "person_id" text NULL, "system_key" text NULL, "label" text NOT NULL, "created_at" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "account_kind_enum" CHECK (kind IN ('person', 'system')), CONSTRAINT "account_system_key_enum" CHECK (system_key IS NULL OR system_key IN ('guild_bank', 'residue', 'write_off', 'import_opening')), CONSTRAINT "account_person_shape" CHECK ((kind = 'person') = (person_id IS NOT NULL)), CONSTRAINT "account_system_shape" CHECK ((kind = 'system') = (system_key IS NOT NULL))) STRICT;
-- create index "ux_account_person" to table: "account"
CREATE UNIQUE INDEX "ux_account_person" ON "account" ("person_id") WHERE person_id IS NOT NULL;
-- create index "ux_account_system" to table: "account"
CREATE UNIQUE INDEX "ux_account_system" ON "account" ("system_key") WHERE system_key IS NOT NULL;
-- create "ledger_batch" table
CREATE TABLE "ledger_batch" ("id" text NOT NULL, "pool_id" text NOT NULL, "seq" integer NOT NULL, "kind" text NOT NULL, "strategy_id" text NOT NULL, "strategy_version" text NOT NULL, "config_snapshot_json" text NOT NULL DEFAULT '{}', "rng_seed" integer NULL, "source" text NOT NULL, "source_ref" text NULL, "actor_user_id" text NULL, "actor_token_id" text NULL, "actor_is_beneficiary" integer NOT NULL DEFAULT 0, "reason" text NOT NULL DEFAULT '', "reverses_batch_id" text NULL, "effective_at" integer NOT NULL, "recorded_at" integer NOT NULL, "effective_day" text NOT NULL, "idempotency_key" text NULL, "entry_count" integer NOT NULL, "net_amount_cp" integer NOT NULL, "prev_hash" blob NULL, "hash" blob NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "ledger_batch_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_batch_reverses" FOREIGN KEY ("reverses_batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_batch_kind_enum" CHECK (kind IN ('attendance', 'award', 'adjustment', 'decay', 'cap', 'start_points', 'zero_sum_credit', 'reversal', 'correction', 're_attribution', 'migration', 'import', 'seed', 'write_off')), CONSTRAINT "ledger_batch_source_enum" CHECK (source IN ('web', 'api', 'discord', 'parser', 'import', 'system')), CONSTRAINT "ledger_batch_actor_is_beneficiary_bool" CHECK (actor_is_beneficiary IN (0, 1)), CONSTRAINT "ledger_batch_entry_count_positive" CHECK (entry_count > 0), CONSTRAINT "ledger_batch_times_nonneg" CHECK (recorded_at >= 0 AND effective_at >= 0)) STRICT;
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
-- create "ledger_entry" table
CREATE TABLE "ledger_entry" ("id" text NOT NULL, "batch_id" text NOT NULL, "pool_id" text NOT NULL, "seq" integer NOT NULL, "account_id" text NOT NULL, "character_id" text NULL, "balance_kind" text NOT NULL, "amount_cp" integer NOT NULL, "item_id" text NULL, "item_award_id" text NULL, "raid_id" text NULL, "tick_id" text NULL, "metadata_json" text NOT NULL DEFAULT '{}', PRIMARY KEY ("id"), CONSTRAINT "ledger_entry_batch" FOREIGN KEY ("batch_id") REFERENCES "ledger_batch" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_entry_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_entry_account" FOREIGN KEY ("account_id") REFERENCES "account" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "ledger_entry_amount_nonzero" CHECK (amount_cp <> 0)) STRICT;
-- create index "ix_entry_balance" to table: "ledger_entry"
CREATE INDEX "ix_entry_balance" ON "ledger_entry" ("pool_id", "account_id", "balance_kind", "seq", "amount_cp");
-- create index "ix_entry_batch" to table: "ledger_entry"
CREATE INDEX "ix_entry_batch" ON "ledger_entry" ("batch_id");
-- create index "ix_entry_stmt" to table: "ledger_entry"
CREATE INDEX "ix_entry_stmt" ON "ledger_entry" ("account_id", "pool_id", "seq");
-- create index "ix_entry_item" to table: "ledger_entry"
CREATE INDEX "ix_entry_item" ON "ledger_entry" ("item_id") WHERE item_id IS NOT NULL;
-- create "balance_snapshot" table
CREATE TABLE "balance_snapshot" ("pool_id" text NOT NULL, "account_id" text NOT NULL, "balance_kind" text NOT NULL, "amount_cp" integer NOT NULL, "as_of_seq" integer NOT NULL, "entry_count" integer NOT NULL, "updated_at" integer NOT NULL, PRIMARY KEY ("pool_id", "account_id", "balance_kind"), CONSTRAINT "balance_snapshot_pool" FOREIGN KEY ("pool_id") REFERENCES "pool" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, CONSTRAINT "balance_snapshot_account" FOREIGN KEY ("account_id") REFERENCES "account" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION) WITHOUT ROWID, STRICT;
-- create index "ix_snapshot_standings" to table: "balance_snapshot"
CREATE INDEX "ix_snapshot_standings" ON "balance_snapshot" ("pool_id", "balance_kind", "amount_cp" DESC);

-- HAND-APPENDED under .claude/rules/migrations.md case 1 (append-only triggers).
--
-- Atlas community cannot express a trigger at all, so these four are hand-written here and the
-- fresh-install fingerprint (test/golden/migrations/fresh_install_fingerprint.txt) is the backstop
-- that notices if a future 12-step table rebuild silently drops them. They are the database half of
-- the append-only invariant: a ledger_batch or ledger_entry row is NEVER updated or deleted, in Go,
-- in SQL, or in a migration. A correction is a reversal batch, never an edit to history.
-- TestTriggers_MutatingLedger_Raises (internal/ledger/trigger_test.go) asserts all four fire, so the
-- guardrail itself cannot be silently regressed.
CREATE TRIGGER trg_ledger_batch_no_update BEFORE UPDATE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;
CREATE TRIGGER trg_ledger_batch_no_delete BEFORE DELETE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;
CREATE TRIGGER trg_ledger_entry_no_update BEFORE UPDATE ON ledger_entry
  BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;
CREATE TRIGGER trg_ledger_entry_no_delete BEFORE DELETE ON ledger_entry
  BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;

-- HAND-APPENDED under .claude/rules/migrations.md case 4 (data backfill / seed).
--
-- The default pool and the four system accounts. The ledger cannot exist without a pool, and the
-- four system accounts (residue, guild_bank, write_off, import_opening) are the ledger-addressable
-- non-human targets that make zero-sum splits, rot handling and write-offs expressible and keep the
-- Conserved invariant verifiable (.claude/rules/ledger-and-strategy.md).
--
-- The ids are DETERMINISTIC ULID constants, defined once in internal/ledger/account.go and reused
-- verbatim here -- that single source of truth is what makes the "addressable by id" tests stable
-- and the fingerprint reproducible. The timestamps are a fixed epoch (2024-01-01T00:00:00Z in
-- micros) so a re-run of this migration produces byte-identical rows.
--
-- Pool BEFORE accounts is mandatory, not cosmetic: nothing here references the pool, but keeping the
-- parent-before-child order is the habit that stops a future edit that DOES add a FK from failing
-- under foreign_keys=ON. The accounts are kind='system' with person_id=NULL, which satisfies the two
-- paired CHECKs on the account table.
INSERT INTO pool (id, name, name_norm, strategy_id, strategy_version, balance_kinds, created_at, updated_at)
VALUES ('00000000000000000000DKPP00', 'Default', 'default', 'zero_sum', '0.0.0', 'dkp', 1704067200000000, 1704067200000000);

INSERT INTO account (id, kind, person_id, system_key, label, created_at, updated_at) VALUES
  ('0000000000000000DKPACCTRES', 'system', NULL, 'residue',        'Residue',        1704067200000000, 1704067200000000),
  ('0000000000000000DKPACCTBNK', 'system', NULL, 'guild_bank',     'Guild Bank',     1704067200000000, 1704067200000000),
  ('0000000000000000DKPACCTWRF', 'system', NULL, 'write_off',      'Write-off',      1704067200000000, 1704067200000000),
  ('0000000000000000DKPACCTMPN', 'system', NULL, 'import_opening', 'Import Opening',  1704067200000000, 1704067200000000);

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
