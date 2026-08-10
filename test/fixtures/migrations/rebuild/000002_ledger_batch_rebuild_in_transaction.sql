-- dkp:fixture deliberately-broken — never repair this file.
--
-- The CANARY twin of 000002_ledger_batch_rebuild.sql: exactly what Atlas generates for a change to
-- ledger_batch that ALTER TABLE cannot express, applied the ordinary way. Test fixture only; it must
-- never be moved into db/migrations-sqlite/.
--
-- The only difference from its correct twin is the ABSENCE of the NO TRANSACTION annotation, and
-- TestFixtures_RebuildPairs_DifferOnlyByTheDeclaredStatements holds the two files to that: every
-- other statement, in the same order, byte for byte after comments and whitespace are normalised
-- away. That is what makes this a controlled experiment rather than an anecdote — the annotation is
-- the independent variable and nothing else moves. (It is not quoted in prose anywhere here: goose
-- treats any comment line carrying its annotation marker as a directive, so a file that spelled the
-- directive out in a comment would fail to parse instead of failing the way this one does.)
--
-- Applying it to a POPULATED database fails:
--
--   constraint failed: FOREIGN KEY constraint failed (787)
--
-- because goose wraps the migration in a transaction, SQLite silently ignores `PRAGMA foreign_keys`
-- inside one, and so `DROP TABLE ledger_batch` runs with foreign keys ENFORCED while ledger_entry
-- still references it. It passes every fresh-install check on the way there, because an empty
-- ledger_batch has no children to violate anything.
--
-- Never repair this file. It is the reproduction of the finding in issue #35, and the proof that
-- the NO TRANSACTION line in its twin is what does the work rather than a superstition somebody
-- copied forward. If it ever starts succeeding, either goose stopped using a transaction or SQLite
-- changed what the pragma does inside one — both are deliberate rewrites of the rule and of both
-- fixtures, not a reason to edit this file into agreement.

-- +goose Up
-- disable the enforcement of foreign-keys constraints
--
-- This is a NO-OP. It is what Atlas emits and what a real generated migration will contain, and
-- inside goose's transaction SQLite ignores it entirely — which is the whole finding.
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

-- The trigger re-creation is present here, exactly as in the correct twin. It is never reached: the
-- DROP above raises first. Keeping it identical is what leaves the missing directive as the only
-- difference between the two files.
CREATE TRIGGER trg_ledger_batch_no_update BEFORE UPDATE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;
CREATE TRIGGER trg_ledger_batch_no_delete BEFORE DELETE ON ledger_batch
  BEGIN SELECT RAISE(ABORT, 'ledger_batch is append-only'); END;

-- +goose Down
-- Forward-only, as every migration in this project is. See db/migrations-sqlite/000001_init.sql.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
