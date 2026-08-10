-- dkp:fixture deliberately-broken — never repair this file.
--
-- A migration that DROPS a ledger table and does not put it back. Test fixture only; it must never
-- be moved into db/migrations-sqlite/.
--
-- It exists because it is the way THROUGH the append-only trigger check, and the check is only worth
-- having if this is closed. Every trigger on a table that does not exist is vacuously present — that
-- exemption is deliberate and load-bearing, since a fresh install applies migration 000001 long
-- before migration 000003 creates the ledger, and demanding trg_ledger_entry_no_update at that point
-- would fail every new install. Take the table away afterwards and the same exemption reports a
-- perfectly healthy database:
--
--   * PRAGMA integrity_check passes — no page is corrupt;
--   * PRAGMA foreign_key_check passes — ledger_entry is the CHILD, so removing it dangles nothing;
--   * no trigger is "missing", because neither of ledger_entry's two has a table to be missing from;
--   * and every ledger entry the guild ever recorded is gone.
--
-- The boot path closes it by comparing the TABLE SET before each migration with the set after it, so
-- a table that was there and now is not is a failure whatever its triggers say.
-- TestMigrate_DroppedLedgerTable_BootRefusesAndRestores is that test, and this file is the migration
-- it refuses.
--
-- Never repair this file. A 12-step rebuild legitimately drops a ledger table and re-creates it
-- under the same name inside one migration; adding that re-creation here would turn this into a
-- rebuild fixture and leave the hole it exists to prove is closed untested.

-- +goose Up
DROP TABLE "ledger_entry";

-- +goose Down
-- Forward-only, as every migration in this project is. See db/migrations-sqlite/000001_init.sql.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
