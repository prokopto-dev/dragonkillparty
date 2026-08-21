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

-- Built-in roles (docs/design/01-domain-model.md section 5.1). SEEDED, not reconciled - the domain
-- model calls this table "the seed, not a second catalogue", and the distinction is a control rather
-- than a wording choice: rewriting a built-in role's grants on every boot would silently restore a
-- permission an officer deliberately revoked, which is a security decision being undone by a restart.
-- So internal/authz seeds a built-in role and its grants only when the role row is absent.
--
-- There is no UPDATE and no DELETE here. A built-in role is not renamable and not deletable; the role
-- editor changes grants, which is role_permission's business and lands with it.

-- name: ListRoles :many
SELECT
    id, key, name, name_norm, description, is_builtin, applies_to, sort_order,
    deleted_at, created_at, updated_at
FROM role
ORDER BY sort_order, id;

-- InsertRole writes one role. The seed calls it inside the same transaction that upserts the
-- permission rows, because role_permission references permission(key) and the grants below would
-- otherwise fail the foreign key on a fresh install - which is also why the seed cannot live in the
-- migration beside pool and account: at migration time the permission table is empty.

-- name: InsertRole :exec
INSERT INTO role (
    id, key, name, name_norm, description, is_builtin, applies_to, sort_order,
    deleted_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?);

-- name: InsertRolePermission :exec
INSERT INTO role_permission (role_id, permission_key) VALUES (?, ?);

-- Role assignments (Phase 2 Wave 0e, issue #276) - who holds which role, and how far it reaches.
--
-- InsertRoleAssignment is the statement the first-run bootstrap (issue #264) and the role editor
-- both call, written here now because authz.Check has nothing to read without it: a permission
-- catalogue and a role seed with no assignments authorise nobody, so the check would be a function
-- whose only observed answer is "no". It is the ADR-0028 commitment-3 shape - the statement its
-- endpoint will call verbatim, landing one wave ahead of the endpoint - and not test scaffolding.
--
-- There is no UpdateRoleAssignment and no DeleteRoleAssignment yet. Revocation is
-- suspended_until_at or expires_at, both of which the read below already honours, and the role
-- editor decides between a soft revoke and a hard delete when it ships.

-- name: InsertRoleAssignment :exec
INSERT INTO role_assignment (
    id, subject_kind, subject_id, role_id, scope_type, scope_id,
    suspended_until_at, granted_by, granted_via, expires_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- EffectivePermission is THE authorization read: the one statement the choke point runs per request
-- that requires a permission (docs/design/03-security.md section 4.1).
--
-- IT ANSWERS TWO QUESTIONS IN ONE ROUND TRIP, because the middleware needs both and the second is a
-- column on the row the first joins through anyway. `granted` is whether this subject holds the key;
-- `requires_step_up` is the permission row's own flag, which internal/authz reconciles from the Go
-- catalogue on every boot and which docs/design/01-domain-model.md section 5 makes the value the
-- middleware reads. Reading the flag from the row rather than from the catalogue in memory is what
-- keeps "the database is the authority" true for a patched or partially-upgraded binary.
--
-- NO ROW MEANS FAIL CLOSED, and the WHERE clause is why there are two ways to get there: a key with
-- no permission row at all, and a key whose row is ORPHANED - stamped because the running binary
-- stopped shipping it. Boot reconciliation refuses to start when a registered route declares either
-- (internal/authz.requireKeys), so reaching this state at request time means the database changed
-- under a running process. The caller answers 503 rather than 403: it cannot decide, and a caller
-- who did nothing wrong should not be told they lack a permission nobody can hold.
--
-- THE FOUR CONDITIONS ON AN ASSIGNMENT are each a documented control rather than defensive
-- filtering:
--
--   role.deleted_at IS NULL   a soft-deleted role grants nothing (role.deleted_at is the product's
--                             delete; there is no hard DELETE on the table). This is the one
--                             condition here with no negative fixture, and it is unreachable rather
--                             than untested: no statement in this file sets deleted_at, because
--                             soft-deleting a role is the role editor's operation and it has not
--                             shipped. Issue #286 carries the fixture, to land with it.
--   expires_at                compared against now rather than swept, so a grant stops working at
--                             the instant it expires whether or not a job has run (db/schema.hcl).
--   suspended_until_at        temporary revocation - an officer on leave or under review - which is
--                             why this schema needs no deny rule. `<= now` means a suspension whose
--                             date has passed is over.
--   the scope pair            a global assignment reaches everything; a scoped one reaches only its
--                             own target. scope_id is NULL exactly when scope_type is 'global'
--                             (CHECK role_assignment_scope_shape), so the first branch never needs
--                             to inspect it and the second always can.
--
-- THERE IS NO BRANCH FOR admin.owner, HERE OR IN GO. The owner capability is a permission row
-- granted to a role like any other (docs/design/03-security.md section 4.3): EQdkp Plus's "group id
-- 2 short-circuits the ACL" is a named anti-pattern, and the single place it could be reintroduced
-- cheaply is this statement.
--
-- CAST(EXISTS(...) AS INTEGER) rather than COUNT(*): EXISTS stops at the first matching assignment,
-- and the cast pins the result to the INTEGER both dialects agree on - sqlc's SQLite engine takes a
-- bare boolean expression's Go type from an affinity a predicate does not have. It is the same
-- reason ResolveSession casts `u.deleted_at IS NOT NULL`.

-- name: EffectivePermission :one
SELECT
    p.requires_step_up,
    CAST(EXISTS (
        SELECT 1
        FROM role_assignment ra
        JOIN role r ON r.id = ra.role_id
        JOIN role_permission rp ON rp.role_id = ra.role_id
        WHERE ra.subject_kind = sqlc.arg(subject_kind)
          AND ra.subject_id = sqlc.arg(subject_id)
          AND rp.permission_key = p.key
          AND r.deleted_at IS NULL
          AND (ra.expires_at IS NULL OR ra.expires_at > CAST(sqlc.arg(now) AS INTEGER))
          AND (ra.suspended_until_at IS NULL
               OR ra.suspended_until_at <= CAST(sqlc.arg(now) AS INTEGER))
          AND (ra.scope_type = 'global'
               OR (ra.scope_type = sqlc.arg(scope_type) AND ra.scope_id = sqlc.arg(scope_id)))
    ) AS INTEGER) AS granted
FROM permission p
WHERE p.key = sqlc.arg(permission_key) AND p.orphaned_at IS NULL;
