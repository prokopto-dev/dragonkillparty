---
paths: ["internal/jobs/**", "internal/events/**", "internal/webhook/**"]
description: The jobs.Queue wrapper around River and why it exists, the transactional outbox, SSE frame discipline, backpressure, and why bid timers are not jobs.
---

# Jobs, events and webhooks

## `jobs.Queue` — River lives behind an interface

```go
type Queue interface {
    Enqueue(ctx context.Context, j Job) (JobID, error)
    EnqueueUnique(ctx context.Context, j Job, key string) (JobID, error)
    Schedule(ctx context.Context, j Job, at core.Micros) (JobID, error)
    Cancel(ctx context.Context, id JobID) error
    Stats(ctx context.Context) (Stats, error)
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
```

**Nothing outside `internal/jobs` imports River.** Not the worker bodies' callers, not
`internal/events`, not a service. The reason is specific and verified: River's SQLite driver
(`riverdriver/riversqlite`) is an **early-preview driver with "minimal real world vetting"**, running
at roughly a quarter of Postgres throughput. The throughput is irrelevant at ~200 jobs/day; the
vetting is not. Behind this six-method interface, replacing River with a hand-rolled
`BEGIN IMMEDIATE`-claim table is a day of work rather than a rewrite.

Consequently: **pin the River version exactly**, never float it, and keep the nightly 10k-job soak
test in `nightly-verify.yml` green — it is the only thing exercising the driver at volume.

Periodic jobs replace cron entirely. EQdkp's "decay silently didn't run because cron wasn't wired"
is designed out; a periodic job that did not run is visible at `/api/v1/admin/jobs`, in the same
SQLite file the officer already backs up.

### Writing a job

- Jobs are **idempotent**. They are retried; `EnqueueUnique` deduplicates on the key but does not
  make the body safe.
- Take a **per-job lock** for semantically conflicting work ("recompute pool 3's balances"). Two
  concurrent snapshot rebuilds for one pool are a correctness bug, not a performance one.
- Per-job locks do **not** prevent writer starvation. `SetMaxOpenConns(1)` means a long transaction
  blocks every raid-night write, so long jobs (import, replay, backfill) **commit in chunks** with a
  bounded chunk size.
- Progress is reported over the SSE topic for the job (`import:{id}`), never by polling a table from
  the client.
- Test with `jobs.DrainForTest(ctx)`, which runs workers until the queue is empty. `time.Sleep` is
  grep-banned in tests; periodic jobs get one real-scheduler test each under `testing/synctest`.
- `goleak.VerifyTestMain(m)` in `TestMain` for all three packages.

Decay runs, cap application and start-points grants **post explicit ledger batches** with
idempotency key `(pool_id, cadence_period)`. Missed periods after downtime apply
`per_missed_period` by default, because applying once silently differs from the guild's stated rules.

## The outbox — written inside the state-change transaction

```go
err := s.store.Tx(ctx, func(q store.Queries) error {
    batch, err := q.InsertLedgerBatch(ctx, params)
    if err != nil { return fmt.Errorf("insert batch: %w", err) }
    // SAME transaction. Not after it. Not in a defer. Not in a goroutine.
    return q.InsertOutboxEvent(ctx, store.OutboxParams{
        Topic: "pool:" + poolID, EventType: "ledger.batch.committed",
        ResourceRef: "/api/v1/ledger/batches/" + batch.ID, CreatedAt: now,
    })
})
```

```
event_outbox(id INTEGER PK AUTOINCREMENT, topic, event_type, resource_ref, created_at)
```

An event emitted outside the transaction is a lost event when the transaction rolls back, and a
phantom event when it commits and the emit fails. There is no third option and no acceptable
"mostly works" version.

- The outbox id **is** `event_seq` — the global realtime sequence. It is **never** called `seq`; the
  per-pool ledger `seq` is a different number and bots will mix them (canonical §4).
- `events.Hub` tails the outbox above its high-water mark and fans out. Retention 24 h, pruned by a
  periodic job.
- **One bus feeds SSE, webhooks and the Discord notifier.** There is exactly one place events are
  emitted, so no channel can know something another does not.

## SSE frames carry ids, never documents

```
id: 8821352
event: bid.placed
data: {"topic":"bid:01J7…","event_seq":8821352,
       "resource":"/api/v1/bid-sessions/01J7…","server_time":"2026-08-03T19:12:49.000000Z"}
```

The consumer refetches through the same REST endpoint the SPA uses. Consequences, all of which you
lose the moment you embed a document: there is exactly one representation of a bid session; the
"realtime payload drifted from the REST payload" defect cannot occur; the event layer needs no
schema, no versioning and no OpenAPI entry beyond the event-name enum.

**Never put a bid amount in a frame.** Sealed amounts are excluded from every read path, frame and
officer UI until `state >= closing`.

Mechanics:

- **One connection, multiplexed topics**: `?topics=bids:01J7…,raid:01J8…,guild:01J1…`. HTTP/1.1 caps
  six connections per origin, so per-topic streams do not scale to a bid board.
- `Last-Event-ID` on reconnect replays from the outbox. At-least-once with a monotonic id; the
  client dedupes on `event_seq`.
- 15-second heartbeat; `X-Accel-Buffering: no` on the response.
- Auth is the session cookie, or a 30-second single-use ticket from `POST /events/ticket` for
  cross-origin tools.

### Backpressure: 256 events, then resync-and-close

Each connection has a **bounded 256-event send buffer**. On overflow the server sends a single
`event: resync` frame and **closes the connection**, forcing a clean reconnect with `Last-Event-ID`.

Do not grow the buffer, do not drop frames silently, do not block the hub. Without this, one slow
client on a raid night stalls fan-out for everyone. The nightly soak runs 60 clients for 30 minutes
with one deliberately stalled, asserting zero dropped `event_seq`, correct resync-and-close, and no
goroutine growth.

## Bid timers are deliberately NOT jobs

`bids.Supervisor` holds one in-memory `time.Timer` per open session. The **database is
authoritative**: on boot it scans `bid_session WHERE state IN ('open','extended')` and re-arms; a
15-second sweep is the safety net.

This is not an oversight and must not be "improved" by moving it onto the queue. A job queue's
latency floor is its poll interval; an auction close needs milliseconds, and the anti-snipe window
is measured in seconds. The recovery path here is twenty lines and survives a restart mid-auction.
A queue-based version is slower, more code, and fails the same way.

## Webhooks

```
X-DKP-Event: bid_session.settled
X-DKP-Delivery: 01J7…                    stable across retries — consumers dedupe on it
X-DKP-Signature: t=1786000000,v1=<hex hmac_sha256(secret, "<t>.<raw body>")>
```

| Concern | Rule |
|---|---|
| Signature | HMAC-SHA256 over `"<t>.<raw body>"`, hex. Verifiers compare in constant time and reject `\|now − t\| > 300 s` |
| Secret rotation | dual-secret overlap window; both accepted for `overlap_seconds` |
| Retry ladder | 5 attempts at ~10 s, 60 s, 5 m, 30 m, 2 h with full jitter, then dead-letter |
| Success | any 2xx |
| `410 Gone` | auto-disable the webhook and notify the admin |
| `429` | honour its `Retry-After` and **do not count the attempt** |
| Timeouts | 10 s connect, 20 s total |
| Ordering | attempted in `event_seq` order within a topic; a failed delivery **does not block later ones** — head-of-line blocking means one broken raid webhook stalls the bid board |
| Dead letter | visible admin queue with one-click redeliver; the delivery log records status, attempts, response code and the first 2 KB of the body |

Webhook payloads carry the same `{topic, event_type, event_seq, resource, occurred_at,
server_time}` frame as SSE. The wire field is `resource`; `resource_ref` is the `event_outbox`
column it is read from, and the column name never reaches a bot. **The one deliberate exception**: `bid_session.opened` and
`bid_session.settled` additionally embed a `snapshot` object so a Discord bot can render something
when the API is briefly unreachable. It is labelled `"snapshot_is_advisory": true` and documented as
display-only. Do not add a third exception.

Every outbound request is constructed through `internal/net/safehttp` — the only place an
`*http.Client` may be built. It rejects loopback, link-local and private targets, resolves DNS once
and pins the IP, and refuses cross-origin redirects. Never log the signing secret; the delivery log
carries the webhook id, not the key.

## Adding an event type

An event type is public API: it appears in the OpenAPI 3.1 `webhooks` block, in the SSE event-name
enum, and in `docs/webhooks/events.md`, all generated from one catalogue. Names are
`resource.past_tense_verb` (`bid_session.settled`, `raid.tick.created`). Adding one is additive;
changing a payload shape or removing one is breaking.

## Stop and ask if

- An event would need to carry authoritative state.
- A job needs to hold a transaction open for more than a few seconds.
- You need River's API surface directly rather than through `jobs.Queue`.
