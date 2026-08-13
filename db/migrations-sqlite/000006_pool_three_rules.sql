-- +goose Up
-- add column "earn_strategy_id" to table: "pool"
ALTER TABLE "pool" ADD COLUMN "earn_strategy_id" text NOT NULL DEFAULT '';
-- add column "earn_config_json" to table: "pool"
ALTER TABLE "pool" ADD COLUMN "earn_config_json" text NOT NULL DEFAULT '{}';
-- add column "spend_strategy_id" to table: "pool"
ALTER TABLE "pool" ADD COLUMN "spend_strategy_id" text NOT NULL DEFAULT '';
-- add column "spend_config_json" to table: "pool"
ALTER TABLE "pool" ADD COLUMN "spend_config_json" text NOT NULL DEFAULT '{}';
-- add column "over_time_strategy_id" to table: "pool"
ALTER TABLE "pool" ADD COLUMN "over_time_strategy_id" text NOT NULL DEFAULT '';
-- add column "over_time_config_json" to table: "pool"
ALTER TABLE "pool" ADD COLUMN "over_time_config_json" text NOT NULL DEFAULT '{}';

-- HAND-APPENDED under .claude/rules/migrations.md case 4 (data backfill).
--
-- The three rule slots supersede the singular pool.strategy_id (ADR-0026, #213). A pool's one rule
-- becomes its EARN rule, because that is the question a DKP pool cannot leave unanswered: points have
-- to come from somewhere before either of the other two rules has anything to act on.
--
-- IT PRESERVES THE ID VERBATIM RATHER THAN INVENTING A COMPOSITION. The default pool ships with
-- strategy_id = 'zero_sum', which no shipped strategy answers to; writing 'tick' and 'fixed_price'
-- here would hand every existing install a DKP system nobody chose, in the columns the ledger then
-- attributes batches to. An id that resolved to nothing before resolves to nothing after, and
-- strategy.ByID says so by name -- which is a refusal an officer can act on where a silent
-- reconfiguration is not. Choosing the composition is the setup flow's job, in Phase 2.
--
-- IDEMPOTENT and safe on a POPULATED database, which are the two hard rules for a backfill. The WHERE
-- clause makes a second run a no-op, so migration-on-boot can be interrupted, restored and re-run --
-- and '' is the same "no rule" sentinel strategy.PoolConfig.Resolve reads, so a pool that has since
-- been configured is left alone. It writes only to the mutable pool table: no ledger_batch or
-- ledger_entry row is touched, and the append-only triggers have nothing to abort.
--
-- A FRESH INSTALL RUNS IT TOO, three migrations after 000003 seeds the default pool, so a fresh
-- database and an upgraded one end with the same row. That is the property that matters here --
-- "works on a fresh install, breaks on upgrade" is the most damaging bug class for this audience.
UPDATE pool
SET earn_strategy_id = strategy_id,
    earn_config_json  = strategy_config_json
WHERE earn_strategy_id = '';

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
