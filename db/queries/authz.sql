-- Permission catalogue reconciliation - the boot path that projects internal/authz/catalogue.go
-- into the permission table (canonical section 6, docs/design/01-domain-model.md section 5).
--
-- Shapes follow db/RECIPES.md. Every statement that reaches SQLite in this project is generated
-- from a file like this one: db.Query and db.Exec outside internal/store are grep-banned (gate
-- SQL002), so the only way a new query enters the codebase is by being written here first and
-- reviewed as SQL.
--
-- FOUR STATEMENTS AND NO "DELETE FROM permission". That is the whole design of the reconciliation
-- and not an omission: role_permission is FK-constrained to permission(key), so deleting a key a
-- newer binary stopped shipping would either fail against the grants that reference it or - with a
-- cascade - silently strip capability from every role that held it, permanently, on a DOWNGRADE.
-- OrphanPermission stamps the row instead, UpsertPermission clears the stamp when the key comes
-- back, and no code path in this product removes a permission row.
--
-- THE SET IS SMALL AND THE WHOLE-TABLE READ IS DELIBERATE. There are fifty-eight keys, read once per
-- boot, so ListPermissions returns every row and the diff against the catalogue happens in Go where
-- it can be unit-tested. A sqlc.slice() over the catalogue would move that set logic into SQL for no
-- measurable gain on a table this size.
--
-- Keep this comment ASCII-only. sqlc v1.31.1 computes each query's text span in bytes but truncates
-- by rune count, so a multibyte character (an em dash, a section sign) in a preceding comment lops
-- that many trailing characters off the generated query string. The failure is silent at generate
-- time and shows up as a syntax error only when the query runs.

-- name: ListPermissions :many
SELECT
    key, category, label, description, is_dangerous, requires_step_up,
    orphaned_at, sort_order
FROM permission
ORDER BY sort_order, key;

-- name: GetPermission :one
SELECT
    key, category, label, description, is_dangerous, requires_step_up,
    orphaned_at, sort_order
FROM permission
WHERE key = ?;

-- UpsertPermission writes one catalogue row. It ALWAYS clears orphaned_at: reaching this statement
-- means the running binary ships the key, which is exactly the condition orphaned_at records the
-- absence of, so an upgrade that restores a key restores its row to live in the same statement that
-- refreshes its label.

-- name: UpsertPermission :exec
INSERT INTO permission (
    key, category, label, description, is_dangerous, requires_step_up,
    orphaned_at, sort_order
) VALUES (?, ?, ?, ?, ?, ?, NULL, ?)
ON CONFLICT (key) DO UPDATE SET
    category         = excluded.category,
    label            = excluded.label,
    description      = excluded.description,
    is_dangerous     = excluded.is_dangerous,
    requires_step_up = excluded.requires_step_up,
    orphaned_at      = NULL,
    sort_order       = excluded.sort_order;

-- OrphanPermission marks a key the running binary no longer ships. The `orphaned_at IS NULL` guard
-- makes it idempotent and keeps the FIRST timestamp: the interesting fact is when the key stopped
-- being shipped, not when the instance was last restarted, and every boot after a downgrade would
-- otherwise rewrite it.

-- name: OrphanPermission :exec
UPDATE permission
SET orphaned_at = ?
WHERE key = ? AND orphaned_at IS NULL;
