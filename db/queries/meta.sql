-- Instance state in dkp_meta. Shapes follow db/RECIPES.md.
--
-- Every statement that reaches SQLite in this project is generated from a file like this one.
-- `db.Query` and `db.Exec` outside internal/store are grep-banned (gate SQL002), so the only way
-- a new query enters the codebase is by being written here first and reviewed as SQL.

-- name: GetMetaValue :one
SELECT value FROM dkp_meta WHERE key = ?;

-- name: UpsertMetaValue :exec
INSERT INTO dkp_meta (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT (key) DO UPDATE SET
    value      = excluded.value,
    updated_at = excluded.updated_at;
