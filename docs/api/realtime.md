# Realtime updates

**Audience:** bot author, SPA developer.

Three transports, one source of truth. **Use SSE.** Use webhooks if your bot already runs on a public
host. Poll if neither fits — polling is a supported design, not a consolation prize.

## Pick a transport

| | SSE | Webhooks | Polling |
|---|---|---|---|
| Needs a public HTTPS URL | No | **Yes** | No |
| Works behind NAT, on a home connection, with no certificate | **Yes** | No | **Yes** |
| Latency | ~50 ms | ~1 s | your interval |
| Survives a bot restart without loss | Yes — `Last-Event-ID` replay | Yes — retry queue | Yes — `since_seq` |
| Failure is visible to you | **Immediately** — the connection drops | Only in the delivery log | Immediately |
| Debuggable with `curl` | **`curl -N`** | Awkward | Trivial |

**SSE is the documented default for Discord bots, ahead of webhooks.** That inverts the usual advice
and it is deliberate. A volunteer-run Discord bot typically has no public HTTPS endpoint, no domain,
no certificate and often no port forwarding — it makes outbound connections to Discord's gateway and
nothing else. *(This is an assumption about the P99 bot population, not a measurement. It is being
checked with pilot guilds; if it is wrong, the recommendation changes and nothing else does.)*
Telling that author "expose an HTTPS endpoint and verify HMAC signatures" is a wall. Telling them
"open one connection with your token" is a five-line change.

All three transports read the same `event_outbox` rows, written **inside the same transaction as the
state change**. No channel knows anything another does not, and no channel can deliver an event for a
transaction that rolled back.

## Frames carry ids, never documents

Every frame — SSE or webhook — is the same six fields and nothing more:

```json
{ "topic": "bid:01JZ0BSESSCARN000000000001",
  "event_type": "bid_session.bid_placed",
  "event_seq": 8821352,
  "resource": "/api/v1/bid-sessions/01JZ0BSESSCARN000000000001",
  "occurred_at": "2026-08-02T04:19:41.884002Z",
  "server_time": "2026-08-02T04:19:41.889112Z" }
```

You refetch through the same REST endpoint the SPA uses. This eliminates a whole bug class:

- there is exactly **one** representation of a bid session in the system, so the realtime payload
  cannot drift from the REST payload;
- the event layer needs no schema versioning, so adding a field to a resource is never a breaking
  event change;
- your `GET` after an event is served from a warm cache with an `ETag`, so the refetch is nearly
  free.

The single exception lives in [webhooks](webhooks.md): two events carry an advisory `snapshot` for
endpoints that must render something even when the API is briefly unreachable. It is explicitly
labelled advisory and must never be acted on.

## Connecting

```bash
curl -N "$DKP_URL/api/v1/events/stream?topics=bids:*,raid:01JZ0RDTHENGHT000000000001,guild" \
     -H "Authorization: Bearer $DKP_TOKEN" \
     -H "Accept: text/event-stream"
```

Requires scope `events:subscribe`, plus the read permission for each topic you ask for.

The first frame tells you what you actually got:

```
retry: 3000

event: connected
data: {"subscribed":["bids:*","raid:01JZ0RDTHENGHT000000000001","guild"],
data: "denied":[],"head_event_seq":8821340,
data: "server_time":"2026-08-02T04:19:00.104332Z"}

: heartbeat

id: 8821352
event: bid_session.bid_placed
data: {"topic":"bid:01JZ0BSESSCARN000000000001","event_type":"bid_session.bid_placed",
data: "event_seq":8821352,"resource":"/api/v1/bid-sessions/01JZ0BSESSCARN000000000001",
data: "occurred_at":"2026-08-02T04:19:41.884002Z","server_time":"2026-08-02T04:19:41.889112Z"}
```

**Per-topic authorization is evaluated at subscribe time**, not per frame. A topic you cannot read is
dropped from the subscription set and named in `denied`, so you learn immediately instead of waiting
forever for events that will never come. Authorization is re-evaluated when your token is revoked or
its scopes change, and the stream closes if it no longer qualifies.

**One connection, multiplexed.** HTTP/1.1 caps at six connections per origin. The SPA opens exactly
one stream; your bot should too. `topics=` accepts wildcards — `bids:*`, `raids:*`, `persons:*` — and
you need them, because you cannot know a bid-session ULID before the session exists.

A `: heartbeat` comment arrives every 15 seconds. It keeps idle proxies from closing the connection
and gives you a liveness signal that is independent of whether anything is happening in the guild.

## Auth by client class

| Client | Mechanism |
|---|---|
| Bot in Node, Python or Go | `Authorization: Bearer dkp_pat_…` on the stream request. Every non-browser SSE client can set headers |
| The SPA, same origin | The `__Host-dkp_session` cookie. Nothing special |
| A cross-origin browser tool — an overlay, an embedded board | `POST /events/ticket`, then `GET /events/stream?ticket=…` |

### The ticket flow

`EventSource` in a browser cannot set headers, so a cross-origin browser client has only the URL.
Putting a long-lived PAT there would leak it into access logs, `Referer` and browser history — which
is precisely the EQdkp `?atoken=` mistake. A 30-second single-use ticket in a URL is acceptable
exposure.

```http
POST /api/v1/events/ticket HTTP/1.1
Cookie: __Host-dkp_session=…
Content-Type: application/json

{"topics": ["bid:01JZ0BSESSCARN000000000001"]}
```

```json
{ "ticket": "eyJhbGciOiJIUzI1NiJ9…",
  "expires_in": 30,
  "topics": ["bid:01JZ0BSESSCARN000000000001"] }
```

The ticket is the **only** JWT in the system: 30-second TTL, single-use and consumed at handshake,
audience-bound to the stream endpoint, carrying an already-resolved principal and an
already-authorised topic list. It cannot be replayed and it cannot be widened — asking the stream for
a topic the ticket does not name is a `403`, not a re-check.

## Reconnecting: Last-Event-ID replay

Every frame's `id:` is its **`event_seq`** — the global outbox position, *not* a ledger `seq`. See
[pagination-and-sync](pagination-and-sync.md#the-two-sequences-are-not-the-same-number).

On reconnect, send the last id you fully processed:

```bash
curl -N "$DKP_URL/api/v1/events/stream?topics=bids:*" \
     -H "Authorization: Bearer $DKP_TOKEN" \
     -H "Last-Event-ID: 8821352"
```

Browser `EventSource` does this automatically. Clients that cannot set the header may use
`?last_event_id=8821352`.

The outbox is retained for **24 hours**. If you ask for an id older than that, the stream opens with
a `resync` frame instead of replaying, and you should do a full refetch of whatever you render.

```
event: resync
data: {"reason":"replay_window_exceeded","head_event_seq":8830112,
data: "server_time":"2026-08-03T09:14:02.118773Z"}
```

For gaps longer than 24 hours, or for a bot that would rather not hold a connection at all, use
`GET /events/replay?since_seq=8821352&topics=bids:*&limit=200`. It returns the identical frames as a
paginated JSON collection.

## Backpressure: resync and close

The server holds a bounded **256-event buffer per connection**. If your client cannot keep up and the
buffer overflows, the server sends one `resync` frame and **closes the connection**:

```
event: resync
data: {"reason":"buffer_overflow","head_event_seq":8821480,
data: "server_time":"2026-08-02T04:22:11.004332Z"}
```

Reconnect with `Last-Event-ID` set to the last id you processed and you get a deterministic catch-up.
Without this rule one slow client on a raid night stalls the hub for everyone; with it, the slow
client pays for its own slowness and nobody else notices.

Handle `resync` as a first-class case, not as an error. Treat it as: *stop trusting incremental
state, refetch what you display, resume.*

`retry: 3000` is sent on connect. During a graceful shutdown the server raises it to `retry: 30000`
so sixty clients do not stampede the binary as it comes back.

## Operational notes

- `X-Accel-Buffering: no` and `Cache-Control: no-store` are set on the stream. If your reverse proxy
  still buffers, `dkp doctor` tests the stream end to end through the operator's actual proxy and
  names nginx `proxy_buffering` explicitly when it fails.
- Limits: 5 concurrent streams per token, 100 per instance. A `429` on connect means you are opening
  a new stream instead of reusing one. The per-instance ceiling is above the nightly 60-client soak
  that is a 1.0 exit criterion, so a full raid watching a bid board is inside it.
- **Countdowns.** Every frame and every bid response carries `server_time`, and the session carries
  the authoritative `closes_at`. Render `closes_at − server_time + local_elapsed`. Never trust the
  client clock: an officer whose laptop is 40 seconds fast will otherwise tell the guild the auction
  ended when it has not.

## The SPA does the same thing

The web UI opens one stream over the same endpoint with the same frame shape and the same replay
semantics. After two failed connection attempts it falls back to a 2-second poll of
`/events/replay?since_seq=` and shows a visible *"live updates unavailable — polling"* indicator.
There is no privileged realtime channel for the browser, and gate 3 in
[the API design](../design/02-api-design.md#10-the-five-gates-that-make-the-spa-a-first-class-api-client)
proves there is not.
