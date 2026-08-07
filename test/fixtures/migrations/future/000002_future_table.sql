-- A VALID migration that a "next release" carries and the current one does not. Test fixture only;
-- it must never be moved into db/migrations-sqlite/.
--
-- Its whole job is to get a database to a schema version above what a given binary understands, so
-- that the downgrade refusal can be tested against a real database rather than a stubbed version
-- number. Testing the refusal by hand-stamping a number would prove the comparison works and prove
-- nothing about the case that actually happens: an officer rolling an image back after a real
-- upgrade really did apply a real migration.
--
-- Unlike 000002_broken_integrity.sql, this one is deliberately CORRECT. It must apply cleanly and
-- pass both PRAGMA integrity_check and PRAGMA foreign_key_check, or the test it supports would go
-- green through the failure path instead of the downgrade path.

-- +goose Up
CREATE TABLE "future_thing" (
    "id"         text    NOT NULL,
    "created_at" integer NOT NULL,
    PRIMARY KEY ("id")
) STRICT;

-- +goose Down
-- Forward-only, as every migration in this project is. See db/migrations-sqlite/000001_init.sql.
SELECT RAISE(ABORT, 'DKP migrations are forward-only; restore /data/backups/pre-<ver>-*.db.zst');
