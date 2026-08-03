# Canonical endpoint shapes

Copy from here. Everything below is the shape the architectural tests, the middleware and the SDK
generators already expect — a bespoke variant costs you a red CI run at best and a wrong SDK at
worst.

Full worked example: [`internal/api/EXAMPLE_ENDPOINT.md`](../../../internal/api/EXAMPLE_ENDPOINT.md).

---

## Operation declaration

```go
huma.Register(api, huma.Operation{
    OperationID:   "createRaidTick",              // public API — never rename
    Method:        http.MethodPost,
    Path:          "/api/v1/raids/{raid_id}/ticks",
    Summary:       "Record an attendance tick",
    Tags:          []string{"raids"},
    Security: []map[string][]string{
        {"pat": {"raids:write"}},                 // family:verb
        {"session": {}},
    },
    Metadata:      map[string]any{"x-dkp-permission": "raid.tick.create"}, // resource.action
    DefaultStatus: http.StatusCreated,
    Errors: []int{
        http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound,
        http.StatusConflict, http.StatusUnprocessableEntity,
    },
}, handler)
```

`Security` lists the schemes that may be used, not the ones that must. Session-only operations omit
the `pat` entry entirely — that is how token-mint, role edits, backup download, bulk PII read and
import commit are expressed (canonical §6: **no scope at all**, session + step-up only).

## Input structs

### Read with an ETag

```go
type GetRaidInput struct {
    RaidID      string `path:"raid_id" format:"ulid"`
    IfNoneMatch string `header:"If-None-Match"`   // optional; enables 304
}
```

### Creating POST

```go
type CreateRaidTickInput struct {
    RaidID         string `path:"raid_id" format:"ulid"`
    IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"16" maxLength:"255"`
    Body struct {
        Ticks []TickDTO `json:"ticks" minItems:"1" maxItems:"200"`
    }
}
```

Missing key → `400 idempotency_key_required`. Same key with a different body → `422
idempotency_key_reused`. Concurrent duplicate → `409 idempotency_key_in_flight` with `Retry-After`.
Uniqueness is `(principal_id, key)` where the principal is the **service account or user**, never the
token — so a token rotated mid-retry still replays (canonical §7).

### State transition or PATCH

```go
type FinalizeRaidInput struct {
    RaidID  string `path:"raid_id" format:"ulid"`
    IfMatch string `header:"If-Match" required:"true"`
    Body    struct {
        Reason string `json:"reason" maxLength:"280"`
    }
}
```

Missing where mandatory → `428 precondition_required`. Mismatch → `412 precondition_failed` carrying
`meta.current` and `meta.current_etag`, so a bot merges and retries in one round trip.

### Collection

```go
type ListRaidsInput struct {
    Cursor      string `query:"cursor"`
    Limit       int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
    Sort        string `query:"sort" default:"-started_at"`
    StateIn     string `query:"state__in"`          // enumerated filter, never a DSL
    StartedGTE  string `query:"started_at__gte" format:"date-time"`
}
```

Filters are explicit typed parameters with the operator suffixes `__ne`, `__gte`, `__lte`, `__in`,
`__contains`, `__isnull`. A generic query DSL cannot be expressed in OpenAPI, so every generated SDK
would degrade to `filter: string`.

## Output structs

```go
type RaidOutput struct {
    ETag string `header:"ETag"`
    Body RaidDTO
}

type ListRaidsOutput struct {
    Body struct {
        Items      []RaidDTO `json:"items"`
        NextCursor *string   `json:"next_cursor"`   // null at the end
        HasMore    bool      `json:"has_more"`      // authoritative
    }
}
```

Use the shared envelope helper rather than re-declaring this struct per resource. `has_more` is
authoritative; `next_cursor` is `null` at the end. Offset is not offered anywhere — it drifts under
concurrent inserts and is the source of the duplicate-and-skip bugs in every bot that has ever polled
EQdkp.

The cursor is base64url of `{v, k:[sort keys], id:[tiebreak ULID], f:[hash of filter+sort]}`,
HMAC-signed. Changing filters while reusing a cursor → `400 cursor_filter_mismatch`, never silently
wrong results.

## Wire-format rules

| Concept | On the wire | Never |
|---|---|---|
| Money | Unquoted JSON integer, field suffix `_centipoints` | Floats, quoted numerals, decimal strings — **including inside `meta`** |
| Money for display | A sibling `price_display: "350.00"` computed with the guild's rounding config | Rounding in the client |
| Time | RFC 3339 UTC, microsecond precision, always `Z`: `"2026-08-02T03:41:12.482113Z"` | Local time, second precision, epoch integers |
| Deadlines | Also carry `server_time` in the same response so clients render `closes_at − server_time` | Trusting the client clock |
| Ids | 26-char Crockford base32 ULID, `format: ulid` | Integers; `legacy_id` appears only on imported entities and only via the compat shim |
| Ordering | `seq` (per-pool, ledger) or `event_seq` (global, outbox) | Ordering by timestamp |
| Enums | Lowercase `snake_case`, identical in the DB `CHECK`, the JSON and the OpenAPI | Uppercase state names, a translation layer |
| Field names | `snake_case` | `camelCase` |

Realistic maxima are ~10¹¹ centipoints, four orders of magnitude below `MAX_SAFE_INTEGER`, so
unquoted integers are safe for every JavaScript consumer.

## Error responses

RFC 9457 `application/problem+json`. **Never HTTP 200 with an error body** — EQdkp did that and every
bot author suffered.

```json
{
  "type": "https://docs.dragonkillparty.org/errors/insufficient_balance",
  "title": "Insufficient balance",
  "status": 409,
  "code": "insufficient_balance",
  "detail": "Account Tankguy has 1200 spendable centipoints; this bid requires 35000.",
  "instance": "/api/v1/bid-sessions/01JZ8Q.../bids",
  "request_id": "01JZ8QKB4N7Y3F0S6M2W9D5H1T",
  "meta": { "spendable_centipoints": 1200, "required_centipoints": 35000, "as_of_seq": 88213 },
  "errors": []
}
```

`errors[]` is populated for validation failures, with `location`, `code`, `message`, `value` and
`suggestions[]`. Name typos are the top parser failure; a bot that can auto-suggest saves the officer
a reconciliation round.

The enum is closed and lives in `internal/api/errors.go`, generated from `errors.yaml`. **Adding a
code is a spec change and needs a docs page** the `type` URL resolves to.

### Codes you will most likely need

| Code | HTTP |
|---|---|
| `unauthenticated`, `token_invalid`, `token_expired`, `token_revoked`, `token_in_query_string` | 401 |
| `insufficient_scope`, `permission_denied`, `sealed_bids_not_revealed`, `bid_not_permitted` | 403 |
| `not_found` | 404 |
| `idempotency_key_required`, `cursor_invalid`, `cursor_filter_mismatch`, `invalid_sort_field`, `invalid_filter`, `invalid_expand` | 400 |
| `precondition_failed` | 412 |
| `payload_too_large` | 413 |
| `unsupported_media_type` | 415 |
| `validation_failed`, `unknown_character`, `unknown_item`, `ambiguous_item`, `idempotency_key_reused`, `invalid_increment`, `invariant_violation`, `pool_strategy_mismatch`, `artifact_unparseable` | 422 |
| `precondition_required` | 428 |
| `conflict`, `idempotency_key_in_flight`, `insufficient_balance`, `hold_conflict`, `outbid`, `session_closed`, `session_state_invalid`, `session_already_settled`, `resolution_failed`, `ledger_immutable`, `batch_already_reversed`, `raid_finalized`, `submission_stale` | 409 |
| `rate_limited` | 429 |
| `engine_unsupported` | 501 |
| `service_unavailable` | 503 |
| `internal_error` | 500 |

> **Resolved.** The `If-Match` mismatch code is `precondition_failed` — the spelling in
> [`docs/api/errors.md`](../../../docs/api/errors.md) and in `internal/api/EXAMPLE_ENDPOINT.md`.
> `etag_mismatch` was an earlier draft's spelling and is not a member of the enum. `errors.yaml`
> remains the source of truth; if it ever disagrees with this table, the table is the bug.

## Rate-limit headers

Emitted on **every** response, not just 429s. Both families, because every existing bot library
already parses the `X-RateLimit-*` triplet:

```
RateLimit: "default";r=487;t=41
RateLimit-Policy: "default";q=600;w=60
X-RateLimit-Limit: 600
X-RateLimit-Remaining: 487
X-RateLimit-Reset: 41
```

Buckets: `read` 600/min · `write` 120/min · `bids` 300/min per token and 20/s per session ·
`ingest` 20/min and 25 MB/min · `compat` 60/min · unauthenticated 60/min per IP. **No token is ever
exempt** — a higher ceiling, never immunity, because a runaway admin script is exactly the failure
that takes down a Raspberry Pi.

## Auth transport

`Authorization: Bearer dkp_pat_…` only. Query-string tokens are rejected with `401
token_in_query_string` — **except** on the compat shim, which accepts `?atoken=` because that is what
existing bots send. Session cookie is exactly `__Host-dkp_session`.

## What SSE and webhooks carry

`{topic, event_type, event_seq, resource, occurred_at, server_time}` and nothing more — the outbox
sequence is `event_seq`, never `seq` (canonical §4). The consumer
refetches through the same REST endpoint the SPA uses. This is why there is exactly one
representation of every resource and why the event layer needs no schema versioning.

The one frozen exception: webhook payloads for `bid_session.opened` and `bid_session.settled` embed a
`snapshot` object labelled `"snapshot_is_advisory": true`. Do not add a third.
