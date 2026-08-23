-- Idempotency records - the `Idempotency-Key` replay layer (canonical section 7, issue #290).
--
-- Shapes follow db/RECIPES.md. Every statement runs inside one store.Tx on the single-writer pool,
-- which is what makes the claim below a claim: two concurrent duplicates queue at BEGIN rather than
-- racing, so exactly one of them inserts and the rest read a live row.
--
-- Keep this comment ASCII-only. sqlc v1.31.1 computes each query's text span in bytes but truncates
-- by rune count, so a multibyte character in a preceding comment lops that many trailing characters
-- off the generated query string - silent at generate time, a syntax error at runtime.

-- GetIdempotencyRecord reads the record a (principal, key) pair holds, live or expired. Expiry is
-- decided in Go against the injected clock rather than by a `expires_at > ?` predicate here: the
-- caller has to tell "no record" from "an expired record it may reclaim" apart, and a predicate that
-- hid the second would make the reclaim an INSERT that fails the unique index instead.

-- name: GetIdempotencyRecord :one
SELECT
    id, principal_ref, "key", method, path_template, request_hash, state,
    response_status, response_headers_json, response_body, created_at, expires_at
FROM idempotency_record
WHERE principal_ref = ? AND "key" = ?;

-- ClaimIdempotencyRecord takes the (principal, key) pair for a request that is about to execute.
--
-- The ON CONFLICT arm is the EXPIRED-RECLAIM path and nothing else. The caller reads first and only
-- claims when there is no record or the one it found is past its TTL, so a conflict here means a
-- record the reader saw as expired - and the reclaim must overwrite the whole row, response columns
-- included, or a fresh in_flight claim would carry the previous request's recorded response and
-- replay it the moment it completed.

-- name: ClaimIdempotencyRecord :exec
INSERT INTO idempotency_record (
    id, principal_ref, "key", method, path_template, request_hash, state,
    response_status, response_headers_json, response_body, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, '{}', NULL, ?, ?)
ON CONFLICT (principal_ref, "key") DO UPDATE SET
    id                    = excluded.id,
    method                = excluded.method,
    path_template         = excluded.path_template,
    request_hash          = excluded.request_hash,
    state                 = excluded.state,
    response_status       = NULL,
    response_headers_json = '{}',
    response_body         = NULL,
    created_at            = excluded.created_at,
    expires_at            = excluded.expires_at;

-- CompleteIdempotencyRecord records the response the claimed request produced, so every later
-- duplicate replays it instead of executing.
--
-- It is keyed on the record id rather than on (principal, key), and that is what makes it safe to
-- run after the handler returned: an id names the claim THIS request took, so a record that expired
-- and was reclaimed by somebody else in the meantime is not overwritten with a stale response.

-- name: CompleteIdempotencyRecord :exec
UPDATE idempotency_record SET
    state                 = ?,
    response_status       = ?,
    response_headers_json = ?,
    response_body         = ?
WHERE id = ?;

-- AbandonIdempotencyRecord deletes a claim whose request did not produce a recordable response - a
-- 5xx, or a panic. docs/api/idempotency-and-concurrency.md promises the caller's retry executes
-- normally after a 5xx, and absence is how this table says "retry me": a third state would mean the
-- same thing while keeping a row that only ever answers "pretend I am not here".
--
-- Keyed on the id for the same reason CompleteIdempotencyRecord is.

-- name: AbandonIdempotencyRecord :exec
DELETE FROM idempotency_record WHERE id = ?;

-- DeleteExpiredIdempotencyRecords is the retention sweep (domain model section 22: 24 h). Served by
-- ix_idempotency_record_expiry, so it is a range scan over the records that are actually due rather
-- than a walk of the table.

-- name: DeleteExpiredIdempotencyRecords :execrows
DELETE FROM idempotency_record WHERE expires_at <= ?;
