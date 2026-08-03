# ADR-0007 — Server-Sent Events over WebSockets

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

Two surfaces need live updates: a **bid board** (many readers, few writers, sub-second matters) and
**raid attendance ticking** (many readers, slow updates, seconds are fine). The instance is behind
whatever reverse proxy a volunteer officer already has — commonly Cloudflare Tunnel, Apache, an old
nginx config, or nothing at all. The transport choice is therefore an operations decision at least as
much as a technical one.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Polling every 2–5 s | Zero ops cost; works through every proxy and firewall on earth; genuinely free at 30–70 concurrent clients | Seconds of latency on a bid board, where a raider needs to know they were outbid before the timer ends |
| B — WebSockets | Bidirectional; one connection; the natural choice if chat or an in-game overlay control channel is ever added | Requires `Upgrade` headers proxied correctly, and **WebSocket `Upgrade` misconfiguration is the number one self-hoster support ticket** — especially behind Cloudflare Tunnel, Apache and older nginx |
| C — SSE over a transactional outbox | Plain HTTP/1.1 chunked, so it works through the same proxies that break WebSockets; reconnect and `Last-Event-ID` replay are built into `EventSource`; bids are `POST`s, so the board is one-way anyway | One-way only; HTTP/1.1's 6-connections-per-origin cap; buffering proxies still need one setting |

## Decision outcome

**Chosen: C, with A as a real fallback.** The operational argument is decisive: bidirectionality buys
nothing here — a bid is a `POST` and the board is a broadcast — and it costs the single most common
class of self-host support ticket. Plain chunked HTTP/1.1 needs no proxy feature at all.

Shape of the decision:

- **One stream per client** with multiplexed topics (`?topics=bid:01J7…,raid:01J8…`), because HTTP/1.1
  caps six connections per origin.
- **Frames carry ids, never documents.** `{topic, seq, resource, server_time}`; the client refetches
  through the same REST endpoint everything else uses. There is exactly one representation of every
  resource in the system, so the classic "the realtime payload drifted from the REST payload" defect
  cannot occur, and the stream needs no schema and no versioning.
- **Transactional outbox.** Every domain mutation writes an `event_outbox` row *inside the same
  transaction as the state change*; the hub tails the outbox and fans out. `Last-Event-ID` replays
  from the outbox (retained 24 h). Same bus feeds webhooks and Discord, so no channel can know
  something another does not.
- **Fallback:** two failed `EventSource` connects → 2-second polling with a visible "live updates
  unavailable, polling" indicator.

**Enforced by:** `X-Accel-Buffering: no` on the stream, and a `dkp doctor` check that tests the stream
end to end through the operator's actual reverse proxy and names nginx `proxy_buffering` explicitly
when it fails. The check exists because this is the one proxy setting SSE still cares about.

### Consequences

- Good, because reconnect, backoff and replay are browser behaviour rather than code we maintain.
- Good, because it works through Cloudflare Tunnel, Apache and old nginx with no operator action,
  which is the entire point.
- Good, because the outbox makes delivery at-least-once with a monotonic `event_seq`, so a bot that
  went offline resumes instead of resyncing.
- **Bad, because it is one-way.** A future chat feature, presence, or an in-game overlay control
  channel needs a second transport — and adding WebSockets later reintroduces the support ticket this
  decision avoided.
- **Bad, because topic multiplexing is our problem, not the protocol's.** The 6-connection cap forces
  one stream with a topic list, and a subscription bug there is a *silent* missed update rather than a
  visible error.
- **Bad, because id-only frames cost a follow-up `GET` per event.** A busy bid board is chattier than
  a document-carrying WebSocket would be; ETags and `304`s make it cheap, not free.
- **Bad, because at-least-once pushes deduplication onto every client**, including third-party bots,
  who must dedupe on `seq`. The SDKs do it; a hand-rolled `curl` loop will not.
- **Bad, because a buffering proxy produces a stream that connects and then appears frozen** — the
  worst diagnostic shape there is. Hence the `dkp doctor` check; without it this would trade one
  support ticket for a subtler one.

### Reversal cost

Adding WebSockets alongside SSE is a phase of work plus a permanent second operations story. Removing
SSE in favour of polling is a day, and is the correct emergency lever if the hub misbehaves.
