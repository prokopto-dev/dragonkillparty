# Webhooks

**Audience:** bot author running on a public host, integrator pointing at a Discord incoming webhook.

Webhooks are the right choice when your endpoint is already reachable over HTTPS. If it is not, use
[SSE](realtime.md) instead — no public URL, no certificate, no signature verification, five lines of
code.

## Register an endpoint

```bash
curl -s "$DKP_URL/api/v1/webhooks" -X POST \
     -H "Authorization: Bearer $DKP_TOKEN" \
     -H "Content-Type: application/json" \
     -H "Idempotency-Key: $(uuidgen)" \
     -d '{"url":"https://bot.example.org/dkp",
          "description":"Castle Steward loot feed",
          "events":["bid_session.settled","award.created","award.reversed"],
          "topics":["bids:*","guild"]}'
```

```json
{ "id": "01JZ0WHKDSCRD0000000000001",
  "url": "https://bot.example.org/dkp",
  "events": ["bid_session.settled", "award.created", "award.reversed"],
  "topics": ["bids:*", "guild"],
  "state": "active",
  "secret": "whsec_8f2c4a7e5b1d8036c2f4a6e8b0d2f4a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8",
  "created_at": "2026-08-02T11:04:22.310884Z" }
```

**`secret` appears exactly once.** It is never retrievable. Store it before you close the terminal.

Requires scope `webhooks:manage`. The target URL is validated against the outbound-request policy:
private address ranges are refused by default, and relaxing that policy is a session + step-up
operation so a leaked token cannot use webhooks to pivot into your LAN.

`POST /webhooks/{id}/test` sends a synthetic `ping` delivery. Do that first.

## Delivery format

```http
POST /dkp HTTP/1.1
Host: bot.example.org
Content-Type: application/json
User-Agent: DragonKillParty/1.0.0 (+https://docs.dragonkillparty.org/api/webhooks)
X-DKP-Event: bid_session.settled
X-DKP-Delivery: 01JZ0DEEVRY000000000000001
X-DKP-Attempt: 1
X-DKP-Event-Sequence: 8821352
X-DKP-Webhook-Id: 01JZ0WHKDSCRD0000000000001
X-DKP-Signature: t=1785644484,v1=8b1c4e2a6b8d0f2a4c6e8b0d2f4a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8f0a

{ "topic": "bid:01JZ0BSESSCARN000000000001",
  "event_type": "bid_session.settled",
  "event_seq": 8821352,
  "resource": "/api/v1/bid-sessions/01JZ0BSESSCARN000000000001",
  "occurred_at": "2026-08-02T04:21:24.771003Z",
  "server_time": "2026-08-02T04:21:24.779446Z" }
```

**The body carries ids, not documents.** Refetch `resource` to act on the event. See
[realtime](realtime.md#frames-carry-ids-never-documents) for why.

**One exception.** `bid_session.opened` and `bid_session.settled` additionally embed a `snapshot`
object, because a Discord bot must render *something* into a channel even if the API is briefly
unreachable. It is labelled `"snapshot_is_advisory": true` and it is for display only — refetch
before you act on it, and never write anything derived from it back.

```json
{ "topic": "bid:01JZ0BSESSCARN000000000001",
  "event_type": "bid_session.settled",
  "event_seq": 8821352,
  "resource": "/api/v1/bid-sessions/01JZ0BSESSCARN000000000001",
  "occurred_at": "2026-08-02T04:21:24.771003Z",
  "server_time": "2026-08-02T04:21:24.779446Z",
  "snapshot_is_advisory": true,
  "snapshot": { "item_name": "Blade of Carnage",
                "winner_display_name": "Tankguy",
                "price_centipoints": 28500,
                "price_display": "285.00" } }
```

## Verifying the signature

Stripe-shaped, because that is the format every bot author has already implemented once.

```
X-DKP-Signature: t=<unix seconds>,v1=<hex hmac>[,v1=<hex hmac>]
```

The signed payload is `"<t>.<raw request body>"`. The MAC is `HMAC-SHA256(secret, payload)`, hex
encoded.

```python
import hashlib, hmac, time

def verify(raw_body: bytes, header: str, secret: str, tolerance: int = 300) -> bool:
    pairs = [p.split("=", 1) for p in header.split(",")]
    ts = int(next(v for k, v in pairs if k == "t"))
    if abs(time.time() - ts) > tolerance:          # replay window
        return False
    signed = f"{ts}.".encode() + raw_body          # raw bytes, never re-serialised
    expected = hmac.new(secret.encode(), signed, hashlib.sha256).hexdigest()
    return any(hmac.compare_digest(expected, v) for k, v in pairs if k == "v1")
```

Four rules, all of them load-bearing:

1. **Sign the raw bytes**, before any JSON parse or re-serialise. Re-encoding changes whitespace and
   breaks the MAC.
2. **Compare in constant time.** `==` on a MAC leaks it a byte at a time.
3. **Reject `|now − t| > 300 s`.** Without the timestamp check a captured delivery can be replayed at
   any point in the future.
4. **Accept any matching `v1`.** During secret rotation two `v1` values are sent (see below).

Both SDKs ship this as `dkp.webhooks.verify(body, header, secret)`. Use it rather than writing your
own.

**Secret rotation without downtime.** `POST /webhooks/{id}/rotate-secret {"overlap_seconds": 3600}`
returns a new secret and emits **both** signatures for the overlap window. Deploy the new secret
whenever you like inside that window; neither ordering drops a delivery.

## Delivery guarantees

**At-least-once, with a monotonic `X-DKP-Event-Sequence`.** Not exactly-once — that is not
achievable — and not ordered across topics.

- **Dedupe on `X-DKP-Delivery`.** It is stable across every retry of the same event.
- You may safely drop anything whose `event_seq` is at or below your high-water mark for that topic.
- Within one topic, deliveries are attempted in `event_seq` order. A failed delivery does **not**
  block later ones: head-of-line blocking would mean one broken raid webhook stalls the bid board.
- Your endpoint must be idempotent. It will receive the same delivery twice eventually.

**Retry ladder.** Five attempts with full jitter, then dead-letter:

| Attempt | Delay after the previous |
|---|---|
| 1 | immediate |
| 2 | ~10 s |
| 3 | ~60 s |
| 4 | ~5 min |
| 5 | ~30 min |
| — | then ~2 h, then dead-letter |

- Success is any `2xx`.
- **`410 Gone` auto-disables the webhook** and notifies the admin. Return it when you have retired an
  endpoint on purpose.
- **`429` from your endpoint** honours your `Retry-After` and does not count against the attempt
  budget.
- Timeouts: 10 s to connect, 20 s total. Return `2xx` fast and process asynchronously; do not do work
  inside the request.

## When your endpoint is broken

It will be. The first thing every bot author does is break their endpoint, so this is self-service.

```bash
curl -s "$DKP_URL/api/v1/webhooks/01JZ0WHKDSCRD0000000000001/deliveries?state=failed" \
     -H "Authorization: Bearer $DKP_TOKEN"
```

```json
{ "items": [
    { "id": "01JZ0DEEVRY000000000000001",
      "event_type": "bid_session.settled",
      "event_seq": 8821352,
      "state": "dead_letter",
      "attempts": 6,
      "last_attempt_at": "2026-08-02T06:52:11.004332Z",
      "last_response_status": 500,
      "last_response_body_head": "Traceback (most recent call last):\n  File \"bot.py\", line 88…",
      "next_attempt_at": null }
  ],
  "next_cursor": null, "has_more": false }
```

The last 2 KB of your own response body is kept, which is usually the whole diagnosis. Replay from
the dead-letter queue when you have fixed it:

```bash
curl -s "$DKP_URL/api/v1/webhooks/01JZ0WHKDSCRD0000000000001/deliveries/redeliver" -X POST \
     -H "Authorization: Bearer $DKP_TOKEN" \
     -H "Content-Type: application/json" \
     -H "Idempotency-Key: $(uuidgen)" \
     -d '{"delivery_ids":["01JZ0DEEVRY000000000000001"]}'
```

The admin UI has the same list with one-click redeliver and bulk replay. A `webhook.delivery_failed`
meta-event fires on the `guild` topic so an officer learns the endpoint is broken without reading
logs.

**Retention:** 7 days of delivery records, 30 days of dead-letter records, pruned by a periodic job.

## Event catalogue

Declared in the same OpenAPI document under the 3.1 `webhooks` field, so there is one document to
read rather than a spec plus a wiki page. The catalogue is generated from
`openapi/registry/events.yaml`; adding an event without a docs entry fails the build.

| Event | Topics | Fires when |
|---|---|---|
| `raid.created` · `raid.updated` · `raid.finalized` · `raid.reopened` | `raid:{id}`, `raids:*` | Raid lifecycle |
| `raid.tick.created` · `raid.tick.updated` · `raid.tick.deleted` | `raid:{id}` | An attendance snapshot is recorded or corrected |
| `raid.kill.recorded` | `raid:{id}` | A kill credit is attached |
| `raid_submission.previewed` · `.committed` · `.failed` | `guild` | Parser ingest lifecycle |
| `award.created` · `award.reversed` | `raid:{id}`, `person:{id}` | Loot awarded or reversed |
| `adjustment.created` · `adjustment.reversed` | `person:{id}` | |
| `ledger.batch.committed` | `pool:{id}` | Every ledger write. The firehose for a mirror or spreadsheet sync |
| `decay.run.completed` | `pool:{id}` | A decay, cap or start-points batch posted |
| `bid_session.opened` | `bid:{id}`, `bids:*` | Carries an advisory `snapshot` |
| `bid_session.bid_placed` · `.bid_retracted` | `bid:{id}` | Sealed amounts are withheld before reveal |
| `bid_session.outbid` | `bid:{id}`, `person:{id}` | Opt-in per member via notification preferences |
| `bid_session.extended` | `bid:{id}`, `bids:*` | Anti-snipe; refetch for the new `closes_at` |
| `bid_session.closing` · `.resolved` · `.settled` · `.reversed` · `.cancelled` · `.rot` · `.resolution_failed` | `bid:{id}`, `bids:*` | `settled` carries an advisory `snapshot` |
| `character.claim_requested` · `character.claimed` · `character.renamed` | `person:{id}`, `guild` | |
| `person.merged` | `guild` | |
| `signup.changed` | `calendar:{id}` | |
| `reconciliation.item_queued` | `guild` | Officer-actionable queue grew |
| `dispute.opened` · `dispute.resolved` | `guild`, `person:{id}` | |
| `import.progress` · `import.completed` · `import.failed` | `import:{id}` | |
| `article.published` · `comment.created` · `comment.flagged` · `shout.posted` | `cms` | Portal surface |
| `application.submitted` · `application.decided` | `guild` | Recruitment |
| `webhook.delivery_failed` | `guild` | Meta-event: your endpoint is broken |
| `ping` | — | `POST /webhooks/{id}/test` only |

**Wildcard topic subscriptions are required, not optional** — `bids:*`, `raids:*`, `persons:*` — because
a bot cannot know a bid-session ULID before the session exists.

Event names are `resource.past_tense_verb`, lowercase `snake_case`, identical in the database, on the
wire and in the spec. **Adding an event type is additive; changing an existing payload's shape or
removing an event type is breaking** and is caught by the `oasdiff` gate.
