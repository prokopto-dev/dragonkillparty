# Auctions and bid sessions

**Status:** bid sessions land in Phase 6. This page is the specification the implementation must
satisfy. State names, modes and error codes below are the canonical wire values.

The platform owns the balance, the clock and the rules. Your Discord bot becomes a terminal instead of
a second source of truth, which is why two bots and the web UI can run the same auction without
disagreeing about who is winning.

EQdkp Plus has no auction concept at all — every guild bolts on a bot that keeps its own state. That is
the gap this closes.

## Modes

| Mode | Bids visible | Winner pays |
|---|---|---|
| `auction_open` | Yes, live | Their own bid |
| `auction_sealed_first` | No, until `closing` | Their own bid |
| `auction_sealed_second` | No, until `closing` | The runner-up plus one increment |

Second-price sealed is the recommended default. Under first price the rational move is to bid your
whole bank, so everyone does and the auction measures bank size instead of desire. Under second price,
bidding what the item is actually worth to you is optimal.

## The states

```
draft ──open──► open ◄──────────────┐
                 │                  │ (bid inside the anti-snipe window)
                 │                  │
                 ├──────────────► extended
                 │                  │
              close / timer         │
                 ▼                  │
              closing ◄─────────────┘
                 │
             resolve
                 ▼
             resolved ──settle──► settled ──reverse──► reversed
                 │                    ▲
                 │                    └── retry is a no-op, not a second charge
                 ├── no valid bids ──► rot
                 └── precondition failed ──► resolution_failed
```

| State | Means | Ledger touched |
|---|---|---|
| `draft` | Rules chosen, invisible to bidders. Queue five items before you pull. | No |
| `open` | Accepting bids | No |
| `extended` | Anti-snipe fired; `closes_at` moved out. Returns to `open`. | No |
| `closing` | A hard 1–3 second freeze. No new bids. **Sealed bids are revealed on entry.** | No |
| `resolved` | Winner, price and tie-break reason computed. An officer may override here. | **No** |
| `settled` | The award is committed and the ledger batch is written | **Yes — only here** |
| `rot` | No valid bids | Depends on your rot policy |
| `resolution_failed` | The winner's balance moved between resolve and settle | No |
| `reversed` | A compensating batch was posted. The session and its bids stay visible. | Yes |

The `closing` freeze exists so "did my bid land?" has a deterministic answer. A bid arriving during it
gets `409 session_closed` with the server's `closed_at` and the request's arrival sequence — a
documented outcome, not a race.

## Holds: why you cannot spend the same points twice

When a bid is accepted, a **hold** is placed for its amount inside the same transaction that validated
it.

```
spendable = balance(as of the current sequence) − Σ active holds
```

Two auctions open, 400 points in the bank, 400 bid in each: under the default `strict` hold policy the
second bid is rejected with `409 insufficient_balance`, and the problem body tells you the balance, the
holds, the spendable amount and the required amount. Losing bidders' holds are released at `resolved`;
the winner's is converted at `settled`.

| `hold_policy` | Behaviour |
|---|---|
| `strict` (default) | The hold blocks the second bid |
| `soft` | Allow it, warn, resolve at settle in session-open order |
| `none` | No holds. Only sane for a pool that allows negative balances. |

## Anti-snipe

A valid bid inside `anti_snipe_window_secs` of `closes_at` pushes `closes_at` out by
`anti_snipe_extend_secs`, up to `max_extensions`. Every board and every Discord embed updates
simultaneously because the extension is an event, not a client-side timer.

Bounding the extension count matters: without `max_extensions`, two stubborn bidders can extend a
120-second auction until the raid ends.

## Tie-breaks

An ordered, configurable chain, evaluated deterministically and **written onto the resolution** so you
can paste the reason into chat:

1. Highest bid
2. Earliest bid sequence
3. Highest attendance percentage in the window
4. Highest remaining balance after the purchase
5. Fewest items won in the window
6. Seeded random — the seed is persisted, so the result is reproducible
7. Officer decision, reason mandatory

Steps 1–6 are automatic. Step 7 exists so the chain always terminates.

## Running one

```bash
# open
curl -sX POST "$DKP_URL/api/v1/bid-sessions" \
  -H "Authorization: Bearer $DKP_TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{
    "pool_id": "01JZ0POOLVELIOUSMAIN000001",
    "raid_id": "01JZ8QMB6G8K0M2P4R6T8V0X2Z",
    "item_id": "01JZ0ITMCLOAKOFFLAMES0001",
    "mode": "auction_sealed_second",
    "min_bid_centipoints": 10000,
    "increment_centipoints": 500,
    "duration_seconds": 120,
    "anti_snipe_window_secs": 20,
    "anti_snipe_extend_secs": 20,
    "max_extensions": 3,
    "hold_policy": "strict",
    "auto_open": true
  }'
```

Amounts are **unquoted integer centipoints**: `10000` is 100.00 points. There are no floats and no
decimal strings anywhere on the wire.

Then, in order:

| Step | Call | Header |
|---|---|---|
| Bid | `POST /bid-sessions/{id}/bids` | `Idempotency-Key` |
| Retract | `POST /bid-sessions/{id}/bids/{bid}/retract` | `Idempotency-Key` |
| Close | `POST /bid-sessions/{id}/close` | `If-Match` |
| Resolve | `POST /bid-sessions/{id}/resolve` | `If-Match` |
| Override | `POST /bid-sessions/{id}/override` | `If-Match`, reason required |
| Settle | `POST /bid-sessions/{id}/settle` | `Idempotency-Key`, `If-Match` |

A retraction is a **new row** marked as retracting the earlier one. Bids are append-only for the same
reason ledger entries are: "he changed his bid" has to be answerable.

Retrying `settle` with the same key returns the identical body with `Idempotency-Replayed: true`. A
second settle without the key returns `409 session_already_settled` naming the batch that already
exists.

## Errors you will actually hit

| Code | Status | Means |
|---|---|---|
| `session_closed` | 409 | The bid arrived during `closing` or after |
| `outbid` | 409 | Someone beat you between your read and your write |
| `invalid_increment` | 409 | The bid is not `min_bid + k × increment` above the leader |
| `insufficient_balance` | 409 | Balance minus active holds is below the bid |
| `session_already_settled` | 409 | Settle was called twice without the idempotency key |
| `resolution_failed` | 409 | The winner's balance moved between resolve and settle |

Every error is `application/problem+json` with a stable machine `code`, a `request_id`, and a `type`
URL that resolves to a real page. The API **never** returns 200 with an error body — that is the EQdkp
behaviour every bot author suffered.

`resolution_failed` is not a bug. It means a decay run or another settlement landed in between, and the
system refused to award at a stale price. An officer chooses: charge to negative, fall through to the
next bidder, or cancel.

## Watching it live

Bots open **one** SSE connection and subscribe to `bids:*` — a bot cannot know a session ULID before
the session exists.

```bash
curl -N "$DKP_URL/api/v1/events/stream?topics=bids:*" \
  -H "Authorization: Bearer $DKP_TOKEN" \
  -H "Accept: text/event-stream"
```

Frames carry `{topic, event_type, event_seq, resource, occurred_at, server_time}` and nothing else.
**The stream never carries authoritative state**: you refetch the resource. That is why the bid board
in the web UI and the embed in your Discord channel cannot disagree.

Every response and every frame carries `server_time`, and the session carries the authoritative
`closes_at`. Render the countdown from those, never from the bot's own clock.

Full transport details: [Realtime](../api/realtime.md).

## Keeping sealed bids sealed

| Control | Mechanism |
|---|---|
| No read path exposes an amount before `closing` | Excluded from the REST payload, the SSE frame and the officer UI by construction |
| An officer reading early | Requires `bid.reveal_early` and the read is audit-logged |
| Amounts in logs | `slog` never records a bid amount before reveal |
| Guilds that do not trust their own officers | Optional commit–reveal: the bot publishes `hash(amount‖nonce)` while open, the plaintext at `closing` |

## Next

- [Choosing a DKP system](choosing-a-dkp-system.md) — first price versus second price, with numbers
- [Loot and reconciliation](loot-and-reconciliation.md) — what happens after settlement
- [Discord bot quickstart](../integrations/discord-bot-quickstart.md) — a bot that does all of this
- [Idempotency and concurrency](../api/idempotency-and-concurrency.md) — `Idempotency-Key` and `If-Match`
