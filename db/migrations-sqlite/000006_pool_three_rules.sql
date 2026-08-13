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
-- The three rule slots supersede the singular pool.strategy_id (ADR-0026, #213), so an existing
-- pool's one rule moves into the slot that rule ANSWERS -- not into earn for everything. Writing
-- every id to earn_strategy_id is wrong for three of the four shipped strategies: fixed_price was a
-- perfectly valid singular pool strategy and declares RuleSpend, so an upgraded fixed-price pool
-- would resolve to ErrWrongRuleKind and could not be read at all. A migration that produces a
-- configuration the product then refuses is the "works on a fresh install, breaks on upgrade" class,
-- aimed at every guild that had already configured a pool.
--
-- THE IDS ARE SPELLED OUT, and that is correct here where a CHECK constraint would not be. A
-- migration is a point-in-time artefact: it runs once, against the rows that exist when it runs, and
-- no pool can hold a strategy that shipped after it. db/schema.hcl refuses a CHECK on these columns
-- because a CHECK constrains the FUTURE; this constrains nothing, and the mapping is exactly the
-- catalogue as it stood when the column was superseded.
--
-- AN ID IN NO LIST IS LEFT UNCONFIGURED, deliberately. 'zero_sum' is the case, and it is what
-- 000003 seeds the default pool with: no shipped strategy answers to it, so there is no slot it
-- could occupy, and writing it anywhere would make PoolConfig.Resolve fail with ErrUnknownStrategy
-- and the whole pool unreadable. Three empty slots resolve cleanly to "this pool has no rules yet",
-- which is the honest state for a pool nobody has configured and the one the Phase 2 setup flow
-- fills in.
--
-- IDEMPOTENT and safe on a POPULATED database, which are the two hard rules for a backfill. Every
-- statement requires all three slots to still be empty, so a re-run is a no-op, the three cannot
-- fight over one row, and a pool an officer has since configured by hand is left alone. They write
-- only to the pool table, which is mutable: no ledger_batch or ledger_entry row is touched, and the
-- append-only triggers have nothing to abort.
--
-- A FRESH INSTALL RUNS THEM TOO, three migrations after 000003 seeds the default pool, so a fresh
-- database and an upgraded one end with the same row -- which is the property that matters here.

-- Earn rules.
UPDATE pool
SET earn_strategy_id = strategy_id,
    earn_config_json  = strategy_config_json
WHERE strategy_id IN ('tick')
  AND earn_strategy_id = '' AND spend_strategy_id = '' AND over_time_strategy_id = '';

-- Spend rules.
UPDATE pool
SET spend_strategy_id = strategy_id,
    spend_config_json  = strategy_config_json
WHERE strategy_id IN ('fixed_price')
  AND earn_strategy_id = '' AND spend_strategy_id = '' AND over_time_strategy_id = '';

-- Over-time rules: the cadence families, which post through decay_run (ADR-0024).
UPDATE pool
SET over_time_strategy_id = strategy_id,
    over_time_config_json  = strategy_config_json
WHERE strategy_id IN ('cap', 'start_points')
  AND earn_strategy_id = '' AND spend_strategy_id = '' AND over_time_strategy_id = '';

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
