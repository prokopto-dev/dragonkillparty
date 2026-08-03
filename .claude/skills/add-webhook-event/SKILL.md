---
name: add-webhook-event
description: Add an outbound event to the realtime catalogue — a new webhook event type and SSE topic. Use whenever a state change should notify bots or the SPA, or when a Discord integration needs to react to something the platform does.
argument-hint: "[resource.past_tense_verb]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(make gen), Bash(make test), Bash(make check)
---

# Add a webhook / SSE event

One catalogue generates the outbox topic constants, the OpenAPI `webhooks` block, the SSE topic list
and `docs/api/webhooks.md`. Adding an event in one place and not the others produces an event bots
receive but cannot discover, or one documented but never emitted.

Event types are **additive-only** inside v1. Adding one is a minor change; changing an existing
payload's shape or removing a type is breaking.

---

## Steps

### 1. Confirm a new event type is needed

| Question | If yes |
|---|---|
| Does an existing event already fire at this moment? | Subscribe to it. `ledger.batch.committed` is the firehose; most "I need to know when points change" requests are already served. |
| Is this a new *topic* on an existing event? | Add the topic to the existing type, not a new type. |
| Would a consumer have to correlate two of your new events to learn one fact? | Emit one event for the fact. |

### 2. Name it

`resource.past_tense_verb`, lowercase `snake_case`: `bid_session.settled`, `raid.finalized`,
`person.merged`. The name is public API — bots filter on it and it never gets renamed.

Pick the topic from the existing families: `guild`, `raid:{id}`, `person:{id}`, `pool:{id}`,
`bid:{id}`, `calendar:{id}`, `import:{id}`. Subscription wildcards (`bids:*`, `raids:*`,
`persons:*`) are **required** — a bot cannot know a bid-session ULID before the session exists.

### 3. Add it to the catalogue

One Go const block. `make gen` writes it into:

- the `event_outbox` topic constants,
- the OpenAPI 3.1 `webhooks` top-level field (so a bot author reads one document, not a spec plus a
  wiki page),
- the SSE topic list,
- `docs/api/webhooks.md`.

Never hand-edit any of those four.

### 4. Emit inside the state-change transaction

The event row is written to `event_outbox` **in the same transaction as the state change**. A tailer
delivers it. Emitting after commit means a crash between the two loses the event, and "the bid
settled but Discord never heard" is a guild-splitting bug.

`event_seq` is the **global** outbox sequence. It is not `seq`, which is the per-pool ledger position
(canonical §4). Both were called `seq` in the source design; conflating them breaks every bot's
resume logic.

### 5. Carry a reference, never a document

```json
{ "topic": "bid:01JZ8Q…", "event_type": "bid_session.settled", "event_seq": 8821352,
  "resource": "/api/v1/bid-sessions/01JZ8Q…",
  "occurred_at": "2026-08-02T03:41:12.482113Z", "server_time": "2026-08-02T03:41:12.499001Z" }
```

The consumer refetches through the same REST endpoint the SPA uses. Consequences: exactly one
representation of every resource; the "realtime payload drifted from the REST payload" defect cannot
occur; the event layer needs no schema versioning; and the bot's follow-up `GET` is served warm with
an `ETag`.

**The only exception is frozen**: webhook payloads for `bid_session.opened` and
`bid_session.settled` embed a `snapshot` object flagged `"snapshot_is_advisory": true`, for bots that
must render something when the API is briefly unreachable. **Do not add a third.**

Sealed bid amounts are absent from every frame before reveal, and any pre-reveal officer read is
itself audit-logged.

### 6. `make gen`

Rewrites `openapi/openapi.json` and both SDKs. Commit the diffs.

### 7. Tests

| Test | Asserts |
|---|---|
| Emission | The event row lands in `event_outbox` in the same transaction, and rolls back with it |
| Delivery | A registered webhook receives it, signed, within the retry ladder |
| Signature | `HMAC-SHA256(secret, "<t>.<raw body>")` hex, constant-time compare, `\|now − t\| > 300 s` rejected |
| Rotation | Two `v1` signatures during the overlap window; both verify |
| Dedupe | Redelivery with the same `X-DKP-Delivery` is a no-op for a correct consumer |
| SSE | The frame appears on the topic and on the wildcard; `Last-Event-ID` replays it |
| Ordering | Within one topic, deliveries are attempted in `seq` order; a failure does not block later ones |

Head-of-line blocking is a bug here, not a feature: one broken raid webhook must not stall the bid
board.

### 8. Notification preference, if member-facing

Member-visible events (outbid, claim approved, dispute opened) get a `notification_type` row so
members opt in per event. Do not punt this to a bot the guild has to write.

### 9. Docs

- `docs/api/webhooks.md` — the event table row, generated.
- `docs/api/realtime.md` if the event changes transport guidance. SSE is the documented **default**
  for Discord bots, ahead of webhooks: most volunteer-run bots have no public HTTPS endpoint.
- A line in `docs/api-changelog.md`.

### 10. `make check`

---

## Delivery contract, for reference

| Property | Value |
|---|---|
| Guarantee | At-least-once, monotonic `X-DKP-Sequence`. Not exactly-once, not ordered across topics. |
| Retries | 5 attempts at ~10 s, 60 s, 5 m, 30 m, 2 h with full jitter, then dead-letter |
| Success | Any 2xx |
| `410 Gone` | Auto-disable the webhook and notify the admin |
| `429` | Honour `Retry-After`; the attempt does not count |
| Timeouts | 10 s connect, 20 s total |
| Retention | 7 days of delivery records, 30 days of dead-letter records |

---

## Stop and ask if

- **The event would carry authoritative state.** It must not. See step 5.
- **The event needs a payload field that is not in the REST representation.** Then the REST
  representation is missing something — use `/add-endpoint`.
- **You want to change or remove an existing event type.** That is breaking: `!breaking-api` label, a
  changelog line, and a human decision.
- **The event fires more than once per state change**, or fires on a read. Both make consumers
  double-count.
