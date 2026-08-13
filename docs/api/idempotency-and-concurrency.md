# Idempotency and concurrency

**Audience:** bot author.

Two mechanisms, two failure modes. `Idempotency-Key` stops your retries from writing twice.
`If-Match` stops two officers from overwriting each other.

## Idempotency-Key

**Required on every POST that creates domain state.** Not optional, not best-effort: a missing key on
such a route is `400 idempotency_key_required`.

This is not pedantry. A log parser on an officer's home connection retries constantly, the retry
usually succeeds, and the failure mode is a raid night credited twice that nobody notices for three
weeks — at which point the fix is a reversal and an argument. Making the key mandatory converts a
silent correctness bug into a loud client bug you find on your first test run.

**Where it is required:** `POST` under `/raids/*/ticks`, `/raid-submissions` and its `/commit`,
`/awards`, `/adjustments`, `/ledger/batches/*/reverse`, `/awards/*/reverse`,
`/adjustments/*/reverse`, `/bid-sessions`, `/bid-sessions/*/bids`, `/bid-sessions/*/settle`,
`/decay-runs/*/commit`, `/reconciliation/*/resolve`, `/persons/*/merge`.

**Where it is not:** pure state transitions that create nothing (`/open`, `/close`, `/resolve`,
`/cancel` — use `If-Match` instead), read-only batch endpoints (`/items/resolve`,
`/characters/resolve`), and `POST /artifacts`, which is content-addressed and therefore naturally
idempotent. An architectural test enumerates the registry and asserts this list, so the rule cannot
drift from the implementation.

### Keys are scoped to the principal, never the token

```
idempotency_record(
  scope_key       TEXT PRIMARY KEY,  -- sha256(principal_id ‖ method ‖ path_template ‖ key)
  request_hash    TEXT,              -- sha256 of canonical-JSON body + resolved path params
  state           TEXT,              -- 'in_flight' | 'complete'
  response_status INTEGER,
  response_body   BLOB,
  created_at      INTEGER,
  expires_at      INTEGER            -- +24h
)
```

`principal_id` is the **service account or user — never the token id**. That matters concretely: if
your token is rotated in the middle of a retry sequence, the retry still replays instead of writing a
second time. A token-scoped key would silently break the rotation guarantee documented in
[auth-and-scopes](auth-and-scopes.md).

Two bots cannot collide, because their principals differ. Keys are 16–255 opaque characters; use a
UUIDv4 or a ULID.

### What happens on replay

| Situation | Result |
|---|---|
| First request | The record is inserted `in_flight` **in the same transaction as the mutation**; on commit it becomes `complete` |
| Replay, same body | The original status and body are returned, plus `Idempotency-Replayed: true` |
| Replay, different body | `422 idempotency_key_reused`, with `meta.original_request_hash` |
| Concurrent duplicate still in flight | `409 idempotency_key_in_flight` with `Retry-After: 1` |
| The original failed with a 5xx | The record rolled back with the transaction; your retry executes normally |

Records expire after 24 hours. Reusing a key after that writes again — pick fresh keys per logical
operation, not per bot lifetime.

```http
POST /api/v1/adjustments HTTP/1.1
Idempotency-Key: 01JZ0DKEY00000000000000001
Content-Type: application/json

{"adjustments":[{"account_id":"01JZ0ACCTHEABT000000000001",
                 "pool_id":"01JZ0PNMAN0000000000000001",
                 "value_centipoints":5000,
                 "reason":"Spawn watch — 4h tracking"}]}
```

```http
HTTP/1.1 200 OK
Idempotency-Replayed: true

{ "created": [ … the original response, byte for byte … ] }
```

### Natural idempotency underneath, always

The header is a convenience layer over real uniqueness constraints, never a substitute for them:

| Operation | Underlying constraint |
|---|---|
| A tick parsed from an artifact | unique `(raid_id, content_sha256)` |
| A bid settlement | unique `ledger_batch.source_ref = "bid_session:{id}"` |
| A decay, cap or start-points run | unique `(pool_id, kind, cadence_period)` — the `kind` separates the three families, which share one cadence vocabulary and one table (ADR-0024) |
| An artifact upload | content-addressed by sha256 |
| A raid submission commit | unique `source_ref = "raid_submission:{id}"` |

So a bot that ignores the header entirely still cannot double-post a tick. It gets `200` with
`deduplicated` populated. That property is what makes "just upload your log, we'll sort it out" a
safe thing to tell an officer.

## ETag and If-Match

**Every mutable single resource carries a strong `ETag`** derived from its `version` column:

```
ETag: "01JZ0BSESSCARN000000000001:8"
```

**Collections carry a weak `ETag`** derived from `(max_seq, count)`:

```
ETag: W/"88241-412"
```

Send it back as `If-None-Match` on your next poll and you get `304 Not Modified` with an empty body.
For a Discord bot polling standings every 30 seconds this is the single largest bandwidth saving
available, and it costs two indexed lookups on the server.

### Where If-Match is required

Not merely honoured — **required**, and its absence is an error:

- every bid-session transition: `open`, `close`, `resolve`, `override`, `settle`, `cancel`
- `POST /raids/{id}/finalize` and `/reopen`
- `POST /raid-submissions/{id}/commit`
- `PATCH` of raids, ticks, pools, event types, characters, persons, webhooks and articles
- `PUT` of any whole-collection replacement (`/pools/{id}/event-types`, `/raid-groups/{id}/members`)

Missing → `428 precondition_required`. Mismatched → `412 precondition_failed`.

### The 412 merge flow

**A `412` carries the current representation in `meta.current` and its etag in `meta.current_etag`**,
so you can merge and retry in one round trip instead of two.

```http
POST /api/v1/bid-sessions/01JZ0BSESSCARN000000000001/close HTTP/1.1
If-Match: "01JZ0BSESSCARN000000000001:6"
```

```http
HTTP/1.1 412 Precondition Failed
Content-Type: application/problem+json

{ "type": "https://docs.dragonkillparty.org/errors/precondition_failed",
  "title": "Precondition failed",
  "status": 412,
  "code": "precondition_failed",
  "detail": "Bid session 01JZ0BSESSCARN000000000001 is at version 7; If-Match specified 6.",
  "instance": "/api/v1/bid-sessions/01JZ0BSESSCARN000000000001/close",
  "request_id": "01JZ0REQ2H5K7N000000000001",
  "meta": {
    "current_etag": "\"01JZ0BSESSCARN000000000001:7\"",
    "current": { "id": "01JZ0BSESSCARN000000000001",
                 "state": "closing",
                 "version": 7,
                 "closed_at": "2026-08-02T04:21:20.311882Z",
                 "closed_by": {"display_name": "Bob"},
                 "bid_count": 7 } } }
```

The client algorithm is three steps and needs no extra request:

1. Read `meta.current`. Decide whether your intent is already satisfied.
2. If it is — as here, the session is already `closing` — render *"already closed by Bob"* rather
   than an error. Two officers clicking *Close* at once is a race with a deterministic outcome: one
   gets `200`, the other gets `412` and a good message.
3. If it is not, merge your change onto `meta.current`, retry with `If-Match: meta.current_etag`.

Do **not** retry blindly with the etag from the `412` and the same body. If the state genuinely moved
under you, that turns a detected conflict into a silent overwrite — which is the entire thing
`If-Match` exists to prevent.

## Choosing between them

| You are | Use |
|---|---|
| Creating something and might retry | `Idempotency-Key` |
| Changing something that already exists | `If-Match` |
| Settling a bid session — both, because it transitions *and* writes a ledger batch | both |
| Polling and want to know if anything changed | `If-None-Match` |
