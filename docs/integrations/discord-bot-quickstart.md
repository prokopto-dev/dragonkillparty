# Discord bot quickstart

**Status:** the API lands in Phase 2, bid sessions and SSE in Phase 6. Nothing here runs yet — this is
the contract a bot will be written against. The Python is illustrative, not a tested example; once the
API exists, every snippet on this page is transcluded from `examples/discord-bot/`, which is built and
smoke-tested in CI.

**There is no first-party Discord bot in 1.0.** It is a named post-1.0 item, and the webhook catalogue
and token scopes are frozen now specifically so that bot needs no server change when it arrives.
Meanwhile, this is how you build your own — and the fact that you can is the point of the product.

## What you are building

A bot that:

1. Answers `!dkp` with a member's balance and attendance.
2. Posts a live bid board when an auction opens and edits it in place as bids land.
3. Places a bid when a member reacts or types `!bid 350`.

Roughly 200 lines. The platform owns the balance, the clock and the rules, so your bot never keeps a
running total and never has to reconcile with anybody.

## 1. Mint a scoped token

Ask your admin. Minting is a **session plus step-up** operation done in the browser or on the server
console — no token can mint another token, which is the deliberate fix for EQdkp's single `api_key`
that impersonated the first superadmin.

```bash
dkp service-account create --name guild-bot --owner officer@example.org
dkp token mint --service-account guild-bot \
               --scopes dkp:read,roster:read,bids:read,bids:manage,events:subscribe \
               --expires 365d
```

```
dkp_pat_a91f3c2b_kXn2rQ7vT4wE8yU1iO5pA3sD6fG9hJ2kL0zX7cV4bN1m

Shown once. Never retrievable. Prefix a91f3c2b is safe to log.
```

| Scope | Needed for |
|---|---|
| `dkp:read` | Balances, standings, attendance |
| `roster:read` | Resolving a Discord user to a person |
| `bids:read` | Reading sessions and boards |
| `bids:manage` | Opening sessions, placing and retracting bids |
| `events:subscribe` | The event stream |

Mint the narrowest set that works. A bot that only answers `!dkp` needs `dkp:read` and nothing else.
Note what is deliberately absent: `dkp:adjust`. A bid bot must be able to charge for an item and must
**not** be able to hand out points.

```bash
export DKP_URL=https://dkp.example.org
export DKP_TOKEN=dkp_pat_a91f3c2b_...
```

Never put the token in a URL. Query-string tokens are rejected with `401 token_in_query_string`,
because query strings land in proxy logs, browser history and `Referer` headers.

## 2. Confirm what you can do

```bash
curl -s "$DKP_URL/api/v1/me" -H "Authorization: Bearer $DKP_TOKEN"
```

`effective_permissions` in the response is **role permissions ∩ token scopes**. If something you
expected is missing, that intersection is the whole explanation. Log the token's `prefix` at boot so
your bot identifies itself without logging a credential.

## 3. Read standings

```python
import os, requests

BASE  = os.environ["DKP_URL"] + "/api/v1"
AUTH  = {"Authorization": "Bearer " + os.environ["DKP_TOKEN"]}

def standings(pool_id, limit=50):
    items, cursor = [], None
    while True:
        params = {"limit": limit}
        if cursor:
            params["cursor"] = cursor
        page = requests.get(f"{BASE}/pools/{pool_id}/standings",
                            params=params, headers=AUTH).json()
        items += page["items"]
        if not page["has_more"]:
            return items
        cursor = page["next_cursor"]
```

Pagination is **cursor only**, in the body envelope: `{items, next_cursor, has_more}`. There are no
`Link` headers and no offsets — the ledger is append-heavy, and offsets duplicate and skip rows under
concurrent writes.

Amounts are **unquoted integer centipoints**. `43000` is 430.00 points. Divide by 100 for display and
never store it as a float.

```python
def fmt(centipoints):
    return f"{centipoints // 100}.{centipoints % 100:02d}"
```

For `!dkp`, show the attendance numerator and denominator rather than only the percentage — "36 of 45
ticks" is checkable and "80%" is not:

```python
att = requests.get(f"{BASE}/persons/{person_id}/attendance",
                   params={"window": "30d", "pool": pool_id, "metric": "ticks"},
                   headers=AUTH).json()
```

## 4. Subscribe to bid events

One connection, multiplexed topics. Subscribe to `bids:*` — a bot cannot know a session's ULID before
the session exists.

```bash
curl -N "$DKP_URL/api/v1/events/stream?topics=bids:*,guild" \
  -H "Authorization: Bearer $DKP_TOKEN" -H "Accept: text/event-stream"
```

```
: heartbeat

id: 8821352
event: bid_session.bid_placed
data: {"topic":"bid:01JZ8QN26G…","event_type":"bid_session.bid_placed",
data: "event_seq":8821352,"resource":"/api/v1/bid-sessions/01JZ8QN26G…",
data: "occurred_at":"2026-08-02T03:41:12.482113Z",
data: "server_time":"2026-08-02T03:41:12.489002Z"}
```

**The stream never carries authoritative state.** A frame tells you *what changed and where*; you
refetch the resource. That is deliberate: there is exactly one representation of a bid session in the
system, so your Discord embed and the web bid board cannot drift apart, and the event layer needs no
schema versioning.

```python
def on_frame(frame):
    session = requests.get(os.environ["DKP_URL"] + frame["resource"], headers=AUTH).json()
    edit_discord_embed(session)     # one message, edited in place
```

| Behaviour | What to do |
|---|---|
| Heartbeat comment every 15 s | Nothing. It keeps idle proxies from closing the connection. |
| Reconnect | Send `Last-Event-ID` with the last `id` you saw; the outbox replays 24 hours. |
| `event: resync` then a close | You fell behind the 256-event buffer. Reconnect with `Last-Event-ID` and you get a deterministic catch-up. |
| Topics you cannot read | Silently dropped from your subscription; the connect frame lists `subscribed` and `denied`, so you find out immediately rather than waiting for events that never come. |
| Countdown | Render `closes_at − server_time + local_elapsed`. Both are in every response and every frame. Never use your own clock. |

### Webhooks instead

If your bot already runs on a public HTTPS host, webhooks are the better fit. Signature is
`HMAC-SHA256(secret, "<t>.<raw body>")`, compared in constant time, rejecting anything where
`|now − t| > 300 s`. Delivery is at-least-once — dedupe on `X-DKP-Delivery`. Both channels read the
same outbox rows, so neither knows anything the other does not.

Polling `GET /events/replay?since_seq=…` is also a legitimate, documented path. At guild scale a
two-second poll costs less than the SSE keepalives.

## 5. Place a bid

```python
import uuid

def place_bid(session_id, character_id, centipoints):
    r = requests.post(
        f"{BASE}/bid-sessions/{session_id}/bids",
        headers={**AUTH,
                 "Idempotency-Key": str(uuid.uuid4()),
                 "Content-Type": "application/json"},
        json={"character_id": character_id,
              "amount_centipoints": centipoints,
              "expected_state": "open",
              "source": "discord"})
    if r.status_code == 201:
        return r.json()
    problem = r.json()          # application/problem+json
    raise BidRejected(problem["code"], problem["detail"], problem.get("meta"))
```

Three rules your bot must obey:

| Rule | Why |
|---|---|
| `Idempotency-Key` on every state-creating POST | Bots retry. A duplicate key returns the original `201`, not a second bid. Generate the key **before** the first attempt and reuse it for every retry of that logical action. |
| Never compute the balance yourself | `GET /accounts/{id}/spendable?pool=…` returns balance, holds, spendable and `as_of_seq`. Holds are what stop the same points winning two simultaneous auctions. |
| Handle the `409`s as outcomes, not errors | They are the normal texture of a live auction. |

| Code | Tell the member |
|---|---|
| `outbid` | Someone beat them between read and write. Re-render the board. |
| `invalid_increment` | The minimum acceptable bid; it is in `meta`. |
| `insufficient_balance` | `meta` carries balance, active holds, spendable and required — show all four. |
| `session_closed` | `meta.closed_at` and their arrival sequence. A documented outcome, not a race. |

Every error is `application/problem+json` with a stable machine `code`, a `request_id` for support,
and a `type` URL resolving to a real page. The API never returns `200` with an error body.

### Acting for a member

In 1.0 the bot is the actor and the member is the subject: put the member's `character_id` in the body.
The ledger batch and the audit row both record your token, so "who placed that bid" is answerable
without any extra mechanism.

Full delegation — `X-DKP-On-Behalf-Of`, where the member is the actor and the bot is only transport —
is a 1.2 item. It needs per-person grants, a three-way scope intersection and its own error codes,
which is a subsystem rather than a header. **Design your bot so the member's identity travels in the
body**, and delegation becomes an added header later rather than a rewrite.

## 6. Run the auction

Only the last step touches the ledger.

| Step | Call | Header |
|---|---|---|
| Open | `POST /bid-sessions` | `Idempotency-Key` |
| Close | `POST /bid-sessions/{id}/close` | `If-Match` |
| Resolve | `POST /bid-sessions/{id}/resolve` | `If-Match` |
| Settle | `POST /bid-sessions/{id}/settle` | `Idempotency-Key`, `If-Match` |

`resolve` computes the winner, the price and the **tie-break reason** — paste that reason into the
channel, it is exactly what stops the argument. Nothing has moved yet, so an officer can still
override with a mandatory reason.

`settle` is the only transition that writes to the ledger, and it is safe to retry: the same key
returns the identical body with `Idempotency-Replayed: true`.

`If-Match` carries the `ETag` you last read. A `412` means the session changed underneath you —
refetch, look at `meta.current`, retry. Do not loop without refetching.

See [Auctions and bid sessions](../guides/auctions.md) for the full state machine.

## Migrating an existing bot

If your bot already drives EQdkp's `api.php`, point it at this instance's compat shim and it works on
day one, `?atoken=` and all. The shim is deprecated from the day it ships: the admin UI counts days
since first use and names the token prefixes still calling it, so you can see which bot to move. Turn
it off with `DKP_COMPAT_ENABLED=false` once nothing calls it.

## Checklist before you ship

- [ ] Token in an environment variable, never in the repository, never in a URL
- [ ] The token's prefix logged at boot; the secret logged nowhere
- [ ] `Idempotency-Key` generated once per logical action and reused across retries
- [ ] Balances read from the API, never cached across a raid
- [ ] SSE reconnect sends `Last-Event-ID`; `resync` handled
- [ ] Countdowns rendered from `server_time` and `closes_at`
- [ ] `409` and `412` treated as outcomes with useful messages, not as crashes
- [ ] Rate-limit headers honoured — back off rather than hammering
- [ ] A token rotation plan; rotation returns a new secret with an overlap window, so it costs no downtime

## Next

- [API getting started](../api/getting-started.md) · [Auth and scopes](../api/auth-and-scopes.md)
- [Idempotency and concurrency](../api/idempotency-and-concurrency.md) · [Pagination and sync](../api/pagination-and-sync.md)
- [Realtime](../api/realtime.md) — SSE, webhooks and polling in full
- [Auctions and bid sessions](../guides/auctions.md) — the state machine your bot drives
