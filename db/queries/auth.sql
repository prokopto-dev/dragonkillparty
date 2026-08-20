-- Identity and credentials - the cookie-or-bearer resolution path and the rows it reads
-- (docs/design/01-domain-model.md section 4, docs/design/03-security.md sections 3 and 6).
--
-- Shapes follow db/RECIPES.md. Every statement that reaches SQLite in this project is generated from
-- a file like this one: db.Query and db.Exec outside internal/store are grep-banned (gate SQL002),
-- so the only way a new query enters the codebase is by being written here first and reviewed as
-- SQL.
--
-- TWO RESOLVES, ONE PER CREDENTIAL CLASS, AND BOTH ARE ONE INDEXED LOOKUP. That is the hot path of
-- every authenticated request: a cookie resolves by the SHA-256 of its secret against
-- ux_session_token, and a bearer resolves by its public 8-character prefix against
-- ux_api_token_prefix followed by one constant-time compare in Go. ADR-0011 accepted a database
-- round trip per request as the price of instant revocation; it is one round trip, not a scan.
--
-- NEITHER RESOLVE FILTERS ON expires_at OR revoked_at, and that is deliberate. The row comes back
-- whole and internal/auth decides, because the middleware has to tell "no such credential" from
-- "expired" from "revoked" from "the account is disabled" in its LOGS while returning the same 401
-- to the caller either way. A WHERE clause that hid the row would leave the server unable to answer
-- the only question that matters during an incident: was this token used, and when.
--
-- NO DELETE STATEMENTS, AND NO REVOKE STATEMENTS EITHER. Revocation is one UPDATE on a row this
-- path already reads, and it lands with the endpoints that perform it - token mint/rotate/revoke and
-- sign-out are session-and-step-up operations from a later wave (canonical section 6's capability
-- floor). A mutation with no caller is a method the Postgres target has to implement for nothing.
--
-- Keep this comment ASCII-only. sqlc v1.31.1 computes each query's text span in bytes but truncates
-- by rune count, so a multibyte character (an em dash, a section sign) in a preceding comment lops
-- that many trailing characters off the generated query string. The failure is silent at generate
-- time and shows up as a syntax error only when the query runs.

-- InsertAppUser writes one human login. The caller normalises username_norm and email_norm in Go
-- (NFKC + casefold + strip ' ` -): core SQLite has no NFKC and lower() is ASCII-only, so a
-- normalisation done in SQL would let a homoglyph of an officer's name be a second account that
-- looks identical in every list.

-- name: InsertAppUser :exec
INSERT INTO app_user (
    id, username, username_norm, email, email_norm, email_verified_at, display_name,
    timezone, locale, state, session_epoch, last_login_at, failed_logins, locked_until_at,
    mfa_totp_secret_enc, mfa_enrolled_at, mfa_required, deleted_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, NULL, ?, NULL, NULL, ?, 0, NULL, 0, NULL, NULL, NULL, 0, NULL, ?, ?);

-- InsertUserIdentity writes one credential for a user. password_hash is the argon2id PHC string
-- internal/auth produces; NULL means this identity cannot authenticate with a password, which is
-- what the EQdkp importer writes because legacy hashes are never migrated.

-- name: InsertUserIdentity :exec
INSERT INTO user_identity (
    id, user_id, provider, provider_key, subject, password_hash, password_algo, password_set_at,
    must_reset, access_token_enc, refresh_token_enc, token_expires_at, scopes, profile_json,
    last_used_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, '', '{}', NULL, ?, ?);

-- name: InsertSession :exec
INSERT INTO session (
    id, user_id, token_hash, identity_id, session_epoch, created_at, last_seen_at,
    expires_at, absolute_expires_at, revoked_at, ip, user_agent, mfa_satisfied_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?);

-- ResolveSession is the cookie half of the auth path: one lookup on ux_session_token, joined to the
-- user because every session check needs the account's state and its session epoch in the same
-- round trip.
--
-- THE EPOCH COMPARISON IS THE CALLER'S. Both epochs come back and internal/auth requires them equal,
-- which is what makes "sign out everywhere" one UPDATE on the user row rather than an UPDATE over
-- every session. Doing the comparison in SQL would turn a bumped epoch into "no such session", and
-- the log line that says a session was killed by an epoch bump is the one that explains a mass
-- logout to the officer who caused it.

-- THE SESSION ROW ARRIVES WHOLE, through sqlc.embed, and the user's four columns arrive beside it.
-- That is not a style preference: sqlc's SQLite engine loses a column's nullability across a JOIN
-- and types every nullable one as interface{}, which AGENTS.md bans in a domain signature and which
-- would put a runtime type assertion on the auth hot path. Embedding hands back the generated
-- Session model, whose nullable columns are already *int64 under emit_pointers_for_null_types.
--
-- user_deleted is a PREDICATE rather than the timestamp, for the same reason: the resolver only asks
-- whether the account is gone, and a boolean expression is a column sqlc types without help.

-- name: ResolveSession :one
SELECT
    sqlc.embed(s),
    u.state AS user_state,
    u.session_epoch AS user_session_epoch,
    u.username AS username,
    CAST(u.deleted_at IS NOT NULL AS INTEGER) AS user_deleted
FROM session s
JOIN app_user u ON u.id = s.user_id
WHERE s.token_hash = ?;

-- TouchSession advances last_seen_at and slides the idle expiry, both THROTTLED by the caller and
-- guarded here.
--
-- THE GUARD IS WHAT MAKES IT SAFE TO CALL, not an optimisation: `last_seen_at < ?` means a burst of
-- concurrent requests on one session produces at most one write, on SQLite's single writer, which is
-- the connection raid-night awards are queued on. internal/auth calls this only when the stored
-- value is already older than its throttle interval, so the common case is no statement at all.
--
-- expires_at NEVER PASSES absolute_expires_at. The idle window slides on use (14 days, 30 with
-- "remember me"); the absolute ceiling does not, so a session held open by a polling script still
-- ends. min() is SQLite's two-argument scalar minimum, not the aggregate.

-- name: TouchSession :exec
UPDATE session
SET last_seen_at = sqlc.arg(now),
    expires_at   = min(sqlc.arg(idle_expires_at), absolute_expires_at)
WHERE id = sqlc.arg(id)
  AND last_seen_at < sqlc.arg(touch_before);

-- name: InsertServiceAccount :exec
INSERT INTO service_account (
    id, name, name_norm, description, owner_user_id, state, created_by, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- InsertAPIToken writes one PAT row. The plaintext secret is NEVER stored and never recoverable:
-- token_hash is HMAC-SHA256(pepper, secret) and the caller has already discarded everything else.
-- Its callers are the mint endpoint (a later wave) and the tests that prove the bearer path resolves
-- what the minting primitive produces - which is the point of it existing before its endpoint does.

-- name: InsertAPIToken :exec
INSERT INTO api_token (
    id, prefix, token_hash, service_account_id, name, scopes, pepper_kid, expires_at,
    last_used_at, last_used_ip, rate_limit_rpm, created_by, revoked_at, revoked_by,
    revoke_reason, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, '', ?, ?, NULL, NULL, '', ?);

-- ResolveAPIToken is the bearer half: one lookup on the PUBLIC prefix, joined to the service account
-- for its state and its human owner.
--
-- IT RETURNS token_hash, and the comparison is a constant-time one in Go. Comparing in SQL would be
-- a byte-by-byte comparison whose timing is observable, and matching on the hash instead of the
-- prefix would move the secret into the query - and into every statement log and slow-query trace
-- that ever reads one.

-- name: ResolveAPIToken :one
SELECT
    sqlc.embed(t),
    a.state AS service_account_state,
    a.owner_user_id AS owner_user_id,
    a.name AS service_account_name
FROM api_token t
JOIN service_account a ON a.id = t.service_account_id
WHERE t.prefix = ?;

-- TouchAPIToken records that a token was used, throttled and guarded exactly as TouchSession is.
--
-- last_used_ip IS NOT WRITTEN HERE. Behind the reverse proxy this project recommends every request
-- arrives from 127.0.0.1, and there is no DKP_TRUSTED_PROXIES yet (issue #98), so the only address
-- available is either useless or attacker-controlled. A column that says '' is honest; one that says
-- whatever X-Forwarded-For claimed is a lie in the screen an officer reads after a leak.

-- name: TouchAPIToken :exec
UPDATE api_token
SET last_used_at = sqlc.arg(now)
WHERE id = sqlc.arg(id)
  AND (last_used_at IS NULL OR last_used_at < sqlc.arg(touch_before));
