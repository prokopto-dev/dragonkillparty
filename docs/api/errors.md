# Errors

**Audience:** bot author.

Every error is an RFC 9457 problem document with `Content-Type: application/problem+json` and a
correct HTTP status. **There is never a 200 with an error body** — EQdkp did that and every bot author
suffered for it.

## The shape

```json
{ "type": "https://docs.dragonkillparty.org/errors/insufficient_balance",
  "title": "Insufficient balance",
  "status": 409,
  "code": "insufficient_balance",
  "detail": "Benchwarmer can spend 23800 centipoints; this bid requires 40000.",
  "instance": "/api/v1/bid-sessions/01JZ0BSESSCARN000000000001/bids",
  "request_id": "01JZ0REQ4M6P8R000000000001",
  "meta": { "account_id": "01JZ0ACCTBENCH000000000001",
            "balance_centipoints": 105000,
            "active_holds_centipoints": 81200,
            "spendable_centipoints": 23800,
            "required_centipoints": 40000,
            "as_of_seq": 88228 },
  "errors": [] }
```

| Field | Contract |
|---|---|
| `code` | **The thing to branch on.** A closed enum, stable forever within v1 |
| `status` | Always equals the HTTP status |
| `type` | `https://docs.dragonkillparty.org/errors/<code>` — the last segment **is** the code, with no transformation. A Go test asserts every code has a page and an on-site check asserts there are no orphan pages, so the link always resolves |
| `title` | Human-readable, stable-ish. Do not branch on it |
| `detail` | Human-readable, specific, **prose that will change**. Never parse it |
| `request_id` | Echoed in `X-Request-Id` and in the server log. The support workflow is "paste me the request id" |
| `meta` | Machine-readable specifics, per code. **All money is unquoted integer centipoints, here as everywhere** |
| `errors[]` | Field-level detail for validation failures |

**Branch on `code`, never on `type` and never on `detail`.** `type` is a documentation address; it is
what you give a human. `code` is the contract.

### Field-level validation errors

```json
{ "code": "validation_failed", "status": 422,
  "errors": [
    { "location": "body.ticks[3].attendees[7].character_name",
      "code": "unknown_character",
      "message": "No character named Tannkguy on server Blue.",
      "value": "Tannkguy",
      "suggestions": ["Tankguy"] }
  ] }
```

`suggestions` matters more than it looks: name typos are the single most common parser failure, and a
bot that can offer "did you mean Tankguy?" in Discord saves the officer a reconciliation round trip.

## Retry rules, in one place

| Status | Retry? |
|---|---|
| `408`, `429`, `502`, `503`, `504` | **Yes.** Honour `Retry-After`; otherwise exponential backoff with full jitter |
| `500` | Once, with backoff. If it recurs it is a server bug — report the `request_id` |
| `409 idempotency_key_in_flight` | Yes, after `Retry-After` (usually 1 s) |
| `412` | Yes, **after merging** onto `meta.current`. Never blindly with the new etag and the old body |
| Any other `4xx` | **No.** Retrying will not change the answer. Fix the request or surface it to a human |

Reuse the **same** `Idempotency-Key` on every retry of the same logical operation. That is what makes
retrying safe. See [idempotency-and-concurrency](idempotency-and-concurrency.md).

## The catalogue

The enum is closed. New codes may be added for **new** conditions; the code or status of an existing
condition never changes inside v1. Both are enforced by the `oasdiff` gate.

### Authentication and authorization

| Code | HTTP | Occurs when | Do this |
|---|---|---|---|
| `unauthenticated` | 401 | No credential, or a malformed `Authorization` header | Send a valid bearer token. Do not retry |
| `token_invalid` | 401 | Prefix unknown, or the MAC does not match | The token is wrong or was mistyped. Do not retry |
| `token_expired` | 401 | Past `expires_at`. `meta.expired_at`, `meta.token_prefix` | Mint a new token. Alert whoever owns the bot |
| `token_revoked` | 401 | Revoked. `meta.revoked_at`, `meta.token_prefix` | Stop. Do not retry — retrying a revoked token looks like an attack |
| `token_in_query_string` | 401 | A token was sent as a query parameter outside the compat shim | Move it to `Authorization: Bearer` |
| `session_required` | 403 | The operation has no PAT scope: it is session-only (token minting, role edits, backups, import, PII, audit export) | This cannot be done with a token, by design. Tell the human to do it in the browser. See [the capability floor](auth-and-scopes.md#the-capability-floor) |
| `step_up_required` | 403 | A session exists but was not re-authenticated within 5 minutes. `meta.step_up_url` | Browser clients only. Send the user to `meta.step_up_url` |
| `insufficient_scope` | 403 | The token lacks a required scope. `meta.required_scopes`, `meta.token_scopes` | Mint a token with the named scope. The message always says exactly what is missing |
| `permission_denied` | 403 | Scope is fine; the principal's role does not hold the permission. `meta.required_permission` | Effective capability is role ∩ scopes. An admin must widen the role |

### Request shape

| Code | HTTP | Occurs when | Do this |
|---|---|---|---|
| `not_found` | 404 | No such resource, or it is soft-deleted | Do not retry. Re-resolve the id |
| `method_not_allowed` | 405 | | Fix the method |
| `unsupported_media_type` | 415 | Wrong `Content-Type` | Send `application/json` |
| `payload_too_large` | 413 | `meta.max_bytes` | Split the batch or the upload |
| `validation_failed` | 422 | Schema or business validation failed. `errors[]` is populated | Fix the request. Show `errors[].message` and `suggestions` to a human |
| `unknown_field` | 422 | The request body carried a property no schema declares. `meta.field` | Fix the field name. Unknown request properties are rejected rather than ignored, because a typo'd field in bot code otherwise fails silently and writes wrong data |
| `invalid_filter` | 400 | Unknown query parameter or operator suffix. `meta.allowed` | Fix it. This is also what you get for `?since_seq=` outside `/ledger/*`, `/audit` and `/events/replay` |
| `invalid_sort_field` | 400 | Field not sortable here. `meta.allowed` | Fix it |
| `invalid_expand` | 400 | Target not expandable here, or depth > 1. `meta.allowed` | Fix it |
| `cursor_invalid` | 400 | Tampered, truncated, or from an older cursor format | Start the walk over from the beginning |
| `cursor_filter_mismatch` | 400 | The cursor was minted under different filters or a different sort | Start over with the filters you actually want. This exists so you get an error instead of quietly wrong rows |
| `bad_request` | 400 | The request was malformed in a way no more specific code above covers — most often a body that is not valid JSON | Fix the request. This is the fallback, so if you are seeing it and one of the specific 400s would fit better, that is worth reporting |

### Idempotency and concurrency

| Code | HTTP | Occurs when | Do this |
|---|---|---|---|
| `idempotency_key_required` | 400 | A POST that creates domain state arrived without `Idempotency-Key` | Add one. It is not optional |
| `idempotency_key_reused` | 422 | Same key, different body. `meta.original_request_hash` | You have a key-generation bug. Use one key per logical operation |
| `idempotency_key_in_flight` | 409 | An identical request is still executing | Retry after `Retry-After` (usually 1 s) |
| `precondition_required` | 428 | `If-Match` is mandatory here and was absent | `GET` the resource, then retry with its `ETag` |
| `precondition_failed` | 412 | `If-Match` did not match. **`meta.current` holds the current representation** and `meta.current_etag` its etag | Merge onto `meta.current` and retry — one round trip, not two. See [the 412 merge flow](idempotency-and-concurrency.md#the-412-merge-flow) |
| `conflict` | 409 | A uniqueness constraint was violated with no more specific code | Do not retry. Inspect `meta` |

### Roster, ingest and reconciliation

| Code | HTTP | Occurs when | Do this |
|---|---|---|---|
| `unknown_character` | 422 | A character name did not resolve and `on_unresolved` is `fail`. `suggestions[]`, `meta.reconciliation_item_id` | Resolve it via `/reconciliation/{id}/resolve`, or resubmit with `on_unresolved: "quarantine"` so nothing is lost |
| `unknown_item` | 422 | An item name did not resolve. `suggestions[]` | Same. Consider creating an alias so it resolves next time |
| `ambiguous_item` | 422 | Several items match equally well. `meta.candidates[]` | Ask a human, or resubmit with an explicit `item_id` |
| `artifact_unparseable` | 422 | The uploaded bytes are not a format this server knows. `meta.format_guess`, `meta.first_bad_line` | Check `GET /parsers` for supported formats. If the format is real, file a parser bug — do not work around it by guessing a regex |
| `submission_stale` | 409 | The world moved since the preview was computed. `meta.diff_url` | Refetch the submission, review the new preview, commit with the new `ETag` |
| `raid_finalized` | 409 | A write to a finalized raid | `POST /raids/{id}/reopen` with a reason, or post a correction instead |
| `invalid_field_value` | 422 | A custom-field value does not match its definition's `kind` or `options`. `meta.field_def_id`, `meta.kind` | Send a value of the declared type, or ask an officer to change the field definition |

### Ledger

| Code | HTTP | Occurs when | Do this |
|---|---|---|---|
| `insufficient_balance` | 409 | Spendable is below what the operation needs. `meta.balance_centipoints`, `meta.active_holds_centipoints`, `meta.spendable_centipoints`, `meta.required_centipoints`, `meta.as_of_seq` | Surface the numbers. Note that holds, not the balance, are usually the reason |
| `invariant_violation` | 422 | A strategy proposal failed a declared ledger invariant. `meta.invariant`, `meta.detail` | Do not retry. This is a correctness stop, and it is working as intended |
| `ledger_immutable` | 409 | An attempt to mutate a committed batch | Corrections are reversals. `POST /ledger/batches/{id}/reverse` |
| `batch_already_reversed` | 409 | `meta.reversal_batch_id` | Nothing to do — it is already undone. Follow the link |
| `pool_strategy_mismatch` | 422 | The pool's strategy does not support this operation. `meta.strategy_id` | Check `GET /strategies` for what this strategy supports |
| `pool_is_mirroring` | 409 | The pool mirrors an EQdkp install during a parallel run, so only the importer may write to it. `meta.pool_id` | Cut over (Settings → Import → Cut over) to make the pool live. See [parallel run and cutover](../migration/parallel-run-and-cutover.md) |

### Bidding

| Code | HTTP | Occurs when | Do this |
|---|---|---|---|
| `session_state_invalid` | 409 | The transition is not legal from the current state. `meta.state`, `meta.allowed_transitions` | Refetch, then decide. Frequently means someone else already did it |
| `session_closed` | 409 | A bid arrived after `closing`. `meta.closed_at`, `meta.arrival_seq` | Tell the bidder their bid did not land, with the timestamp. This is a documented outcome, not a race |
| `outbid` | 409 | Open-auction mode only. `meta.current_leader_amount_centipoints` | Prompt for a higher bid |
| `invalid_increment` | 422 | Not a legal step above the current price. `meta.min_bid_centipoints`, `meta.increment_centipoints`, `meta.nearest_valid_centipoints` | Round to `nearest_valid_centipoints` and resubmit |
| `hold_conflict` | 409 | Funds are already held by another live session. `meta.holds[]` | Show which sessions hold the points. Do not retry until one settles |
| `bid_not_permitted` | 403 | An eligibility rule denies it — attendance gate, rank, item priority. `meta.rule` | Surface `meta.rule`. An officer decides |
| `sealed_bids_not_revealed` | 403 | A pre-reveal read of sealed amounts without `bid.reveal_early` | Wait for `closing`. Note that a successful early reveal is audited against the officer who did it |
| `session_already_settled` | 409 | A second settle without the original key. `meta.batch_id` | Nothing to do. Follow `meta.batch_id` to the ledger batch |
| `resolution_failed` | 409 | Settle-time revalidation failed inside the committing transaction. `meta.reason` | The session is now `resolution_failed`. An officer re-resolves or overrides. **No award was written at a stale price** |

### Infrastructure

| Code | HTTP | Occurs when | Do this |
|---|---|---|---|
| `rate_limited` | 429 | Bucket exhausted. `Retry-After` plus both `RateLimit` header families | Back off. The headers are on every response, not just this one — watch them and never hit this |
| `setup_required` | 503 | The instance has never completed first-run setup, so every route except `/setup`, `/healthz`, `/readyz` and the static assets refuses | An operator must finish the wizard. There is no half-open state in which an unconfigured instance exposes an API |
| `engine_unsupported` | 501 | The operation needs a database feature the running engine does not have | Not retryable. It exists so the compile-time Postgres target can refuse rather than silently differ; on SQLite in 1.0 you should never see it |
| `service_unavailable` | 503 | Shutting down or migrating. `Retry-After` | Retry after the stated delay. During an upgrade this is normal and brief |
| `internal_error` | 500 | An unhandled server fault | Retry once. Then report the `request_id`. The body never leaks internals — `request_id` is all it carries, and it is enough for the operator to find the log line |

## Where these come from

The catalogue is a hand-authored registry, `openapi/registry/errors.yaml`, which generates the Go
constants, the `code` enum in `openapi.json`, one docs page per code, and the discriminated error
types in both SDKs. **Adding a code without a docs page fails the build**, so the `type` URL in a
problem document always resolves to something written by a human.
