-- A deliberately-illegal query used only by the total()-ban test. SQLite's total() returns a
-- floating-point result where sum() returns an integer, so it silently converts the centipoint
-- ledger to floating point — no error, just a balance wrong by a fraction of a point for years
-- (canonical §1). The repo-gate MONEY002 fires on the offending call in the SELECT below when this
-- file is scanned under db/. Do not "fix" it to sum(); the violation is the point of the fixture.
-- name: BadBalance :one
SELECT total(amount_cp) AS balance FROM ledger_entry WHERE account_id = ?;
