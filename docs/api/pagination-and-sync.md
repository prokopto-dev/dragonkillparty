# Pagination and incremental sync

**Audience:** bot author, spreadsheet owner.

Two separate problems. Pagination walks a collection *now*. Incremental sync catches up on what
changed while you were gone. They use different mechanisms and mixing them up is the most common
source of wrong numbers in a DKP bot.

## Cursor pagination

Every collection returns the same envelope, in the body:

```json
{ "items": [ … ],
  "next_cursor": "eyJ2IjoxLCJrIjpbODgyMTNdLCJpZCI6IjAxSlowQUNDVFRBTktHWTAwMDAwMDAwMDAxIn0.tS1x",
  "has_more": true }
```

- `limit` defaults to 50 and caps at 200.
- **`has_more` is authoritative.** `next_cursor` is `null` at the end.
- Never a `Link` header. Bots parse JSON far more reliably than they parse RFC 8288.

```bash
curl -s "$DKP_URL/api/v1/ledger/batches?pool=01JZ0PNMAN0000000000000001&limit=200" \
     -H "Authorization: Bearer $DKP_TOKEN"
# then, while has_more:
curl -s "$DKP_URL/api/v1/ledger/batches?cursor=$NEXT" -H "Authorization: Bearer $DKP_TOKEN"
```

**The cursor is opaque, signed and versioned.** It encodes the sort key values, a ULID tiebreak, a
hash of the filter and sort, and the class of the principal it was issued to, then HMACs the lot.
Consequences you can rely on:

- Reusing a cursor with different filters or a different sort returns
  `400 cursor_filter_mismatch` rather than silently wrong results.
- A tampered or truncated cursor returns `400 cursor_invalid`.
- **A cursor is bound to the principal *class* it was issued to.** Replaying one under a principal
  of a different class returns `400 cursor_invalid`, including where both could see the collection.
  It is **not** bound to an individual identity: two principals of the same class share a cursor
  space, and one can present the other's cursor. That is not a way to see more than you may — a
  cursor is a *position*, not a grant, and the server re-applies your own authorization to every row
  of every page. If your bot switches to a token of a different class mid-scan, restart the scan.
- Cursors survive a server restart. They do not survive a change to the cursor format, which is why
  they carry a version — and why you should treat "cursor rejected" as "start over", not as fatal.

**Do not parse a cursor.** It is a server implementation detail and its shape is not part of the
contract.

`?include_total=true` returns `total` where an indexed count is cheap. On `/ledger/entries` it
returns `total_estimated`. It is off by default because on the largest table it is the one query that
can blow the statement budget.

## Why there is no offset

There is no `?offset=` and no `?page=` anywhere in this API, and there never will be inside v1.

The ledger is append-heavy. While your bot walks page 3, three more batches land. With offset
pagination those rows shift under you: some rows appear twice, some never appear at all. It is not a
theoretical hazard — **it is the source of the duplicate-and-skip bugs in every bot that has ever
polled EQdkp**, and it is invisible, because the symptom is a member's balance being slightly wrong
weeks later rather than an error anyone sees.

A cursor anchors to a **position**, not a count. New rows arriving between pages cannot displace it.

Offset also degrades: `LIMIT 50 OFFSET 100000` makes SQLite walk 100 050 rows. A cursor is an index
seek at any depth.

## Incremental sync with `?since_seq=`

Cursors let you walk a collection once. `since_seq` is what you want when your bot was offline for a
day and needs the delta, not the corpus. **This is the more important primitive of the two.**

**`?since_seq=` is valid on exactly three prefixes**, and nowhere else:

| Path | The sequence is |
|---|---|
| `/ledger/entries` · `/ledger/batches` | The **per-pool ledger `seq`** |
| `/audit` | The audit log's own gapless `seq` |
| `/events/replay` | The **global outbox `event_seq`** |

Everything else uses the opaque cursor. `/raids`, `/awards` and `/adjustments` do **not** accept
`since_seq`: raids and awards have no `seq` column at all, and adjustments project over per-pool
batches, so a single global `since_seq` across pools would be meaningless. Asking for one returns
`400 invalid_filter` naming the allowed parameters.

### The two sequences are not the same number

This is the one thing to get right.

| Name | Scope | Where you see it | Means |
|---|---|---|---|
| `seq` | **Per pool**, on `ledger_batch` | `as_of_seq` in balances, `?since_seq=` on `/ledger/*` | Ledger position. A balance is defined *as of a `seq`*, never as of a timestamp |
| `event_seq` | **Global**, on the outbox | SSE `id:`, the `X-DKP-Event-Sequence` header, `Last-Event-ID`, `?since_seq=` on `/events/replay` | Delivery position for realtime and replay |

They are different magnitudes, they advance at different rates, and a pool's `seq` restarts at 1 for
every new pool. Keep them in separate variables. Never feed one to an endpoint expecting the other:
the result is not an error, it is a wrong answer.

### The catch-up loop

Store one high-water mark per pool. On start, ask for everything after it.

```bash
# state: {"01JZ0PNMAN0000000000000001": 88212}

curl -s "$DKP_URL/api/v1/ledger/entries?pool=01JZ0PNMAN0000000000000001&since_seq=88212&limit=200" \
     -H "Authorization: Bearer $DKP_TOKEN"
```

```json
{ "items": [
    { "id": "01JZ0ENTRYSETT000000000001",
      "batch_id": "01JZ0BATCHSETT000000000001",
      "pool_id": "01JZ0PNMAN0000000000000001",
      "seq": 88229,
      "account_id": "01JZ0ACCTTANKGY00000000001",
      "balance_kind": "dkp",
      "amount_centipoints": -28500,
      "kind": "award",
      "effective_at": "2026-08-02T04:21:24.771003Z",
      "recorded_at":  "2026-08-02T04:21:24.771003Z" }
  ],
  "next_cursor": null,
  "has_more": false,
  "head_seq": 88241,
  "server_time": "2026-08-02T11:40:02.118773Z" }
```

Rules for the loop:

1. **Advance the high-water mark only after the page is fully processed**, to the maximum `seq` you
   actually handled. If you crash mid-page you replay a few entries; entries are immutable, so
   replaying them is safe as long as your own writes are keyed.
2. **Page with the cursor, not by re-issuing `since_seq`.** `since_seq` selects the window;
   `next_cursor` walks it.
3. `head_seq` tells you where the pool is now, so you can report "behind by 14 batches" instead of
   guessing.
4. **Entries are append-only.** They are never updated and never deleted. A correction arrives as a
   *new* entry in a `reversal` or `correction` batch with `reverses_batch_id` set. Your mirror should
   therefore be append-only too — if you `UPDATE` rows to apply a correction you will diverge from
   the source of truth by construction.

### Backdating does not break the loop

`effective_at` is game truth and may be backdated; `recorded_at` is system truth and never is. A
reversal posted today for a raid six weeks ago gets **today's `seq`** and yesterday's `effective_at`.
So ordering by `seq` always shows you everything new, and ordering by `effective_at` never does.
Sync on `seq`. Display on `effective_at`.

## Which mechanism to use

| You want | Use |
|---|---|
| The current standings table | `/pools/{id}/standings` with a cursor, plus `If-None-Match` |
| A member's statement | `/accounts/{id}/ledger` with a cursor |
| Everything that changed since your bot restarted | `?since_seq=` on `/ledger/entries` |
| To know *immediately* when something changes | [SSE](realtime.md) — then refetch |
| To catch up on events you missed while disconnected | `Last-Event-ID`, or `/events/replay?since_seq=` |
| The whole ledger, once, for a spreadsheet | `/ledger/entries` with `Accept: text/csv` |

**Polling is a legitimate design, not a fallback.** At guild scale, a two-second poll of
`/events/replay?since_seq=` plus `If-None-Match` on the resources you render costs the server two
indexed lookups and a `304`. If a long-lived connection is impractical in your environment — a cron
job, a serverless function, a spreadsheet — poll and stop worrying about it.
