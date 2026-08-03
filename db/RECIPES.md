# Query recipes

**Status:** target state. These land alongside the schema they query, phase by phase.

The query shapes actually used in this codebase. **Find one here before writing a new one.** Copying
a working in-repo query is far more reliable than recalling SQLite's dialect from memory, which is
the whole reason this file exists.

Raw SQL lives only in `db/queries/*.sql`, consumed by sqlc. `db.Query` and `db.Exec` outside
`internal/store` are grep-banned.

---

## Two bans, and why

**`total()` is banned. Use `sum()`.**

SQLite's `total()` returns a **REAL** — a float — where `sum()` returns an INTEGER for integer
inputs. Since every point value is `INTEGER` centipoints, `total()` would silently convert the
entire ledger to floating point and defeat the invariant the whole product rests on. It would not
error. It would just be quietly wrong, by a fraction of a point, for years.

`sum()` returns `NULL` over zero rows, so wrap it: `COALESCE(sum(amount_cp), 0)`.

A repo grep gate rejects `total(` anywhere in the tree.

**Never query into a JSON column.** `*_json` columns are validated on write and read whole. If you
need to filter or sort on a fact, it is a real column. No exceptions — this is what keeps the
Postgres port cheap and the query planner honest.

---

## Singleton fetch

```sql
-- name: GetGuild :one
SELECT * FROM guild LIMIT 1;
```

## Upsert

SQLite needs an explicit conflict target. `excluded` is the would-be-inserted row.

```sql
-- name: UpsertBalanceSnapshot :exec
INSERT INTO balance_snapshot (account_id, pool_id, balance_kind, amount_cp, as_of_seq, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, pool_id, balance_kind) DO UPDATE SET
    amount_cp  = excluded.amount_cp,
    as_of_seq  = excluded.as_of_seq,
    updated_at = excluded.updated_at
WHERE excluded.as_of_seq > balance_snapshot.as_of_seq;  -- never move a snapshot backwards
```

## Balance as of a seq

The definitional query. A balance is a `SUM` over an append-only log, positioned by `seq` — never by
timestamp, because a backdated `effective_at` must not change what a past balance *was*.

```sql
-- name: BalanceAsOfSeq :one
SELECT COALESCE(sum(e.amount_cp), 0) AS amount_cp
FROM ledger_entry e
JOIN ledger_batch b ON b.id = e.batch_id
WHERE e.account_id = ? AND e.pool_id = ? AND e.balance_kind = ? AND b.seq <= ?;
```

Served entirely from the covering index — no table access. An `EXPLAIN QUERY PLAN` golden asserts
this, because the day it starts scanning is the day standings gets slow and nobody notices.

```sql
CREATE INDEX ix_entry_balance
    ON ledger_entry (account_id, pool_id, balance_kind, batch_id, amount_cp);
```

## Account statement with a running balance

The screen that settles most loot arguments. Window function, one pass.

```sql
-- name: AccountStatement :many
SELECT
    b.seq,
    b.kind,
    b.reason,
    b.effective_at,
    b.recorded_at,
    b.reverses_batch_id,
    e.amount_cp,
    sum(e.amount_cp) OVER (ORDER BY b.seq
                           ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS running_cp
FROM ledger_entry e
JOIN ledger_batch b ON b.id = e.batch_id
WHERE e.account_id = ? AND e.pool_id = ? AND e.balance_kind = 'points'
ORDER BY b.seq DESC
LIMIT ? OFFSET 0;
```

> `LIMIT` here paginates a single account's history by cursor on `seq` in the real
> implementation — see the cursor recipe. The `OFFSET 0` above is illustrative only; offset
> pagination is banned on collections.

## Standings

The heaviest read in the product: every active person's balance in a pool, sorted. Budgeted at
**≤ 4 statements for 280 members**, enforced by the per-test statement-count fixture.

Read from the snapshot, not the ledger. The snapshot is a droppable cache, maintained synchronously
in the same transaction as the write and verified nightly by the replay job.

```sql
-- name: Standings :many
SELECT
    p.id            AS person_id,
    p.display_name,
    c.name          AS main_character,
    c.class_id,
    COALESCE(s.amount_cp, 0) AS balance_cp,
    COALESCE(r.attended, 0)  AS attended_30,
    COALESCE(r.held, 0)      AS held_30
FROM person p
JOIN account          a ON a.person_id = p.id AND a.pool_id = ?
LEFT JOIN character   c ON c.id = p.main_character_id
LEFT JOIN balance_snapshot s
       ON s.account_id = a.id AND s.pool_id = ? AND s.balance_kind = 'points'
LEFT JOIN attendance_rollup r
       ON r.person_id = p.id AND r.pool_id = ? AND r.window_days = 30
WHERE p.state = 'active' AND p.deleted_at IS NULL
ORDER BY balance_cp DESC, p.display_name ASC
LIMIT ?;
```

## Attendance over a rolling window

Officers argue about this, so the numerator and denominator are spelled out rather than inferred.

- **Numerator** — distinct raids in the pool the person attended within the window.
- **Denominator** — distinct raids *held* in the pool within the window, excluding event types
  flagged `no_attendance`, and collapsing connected raids via `attendance_group_id` so one raid
  night split across several mob entries counts once.

```sql
-- name: AttendanceInWindow :one
WITH held AS (
    SELECT DISTINCT COALESCE(r.attendance_group_id, r.id) AS raid_key
    FROM raid r
    JOIN event_type et ON et.id = r.event_type_id
    JOIN pool_event_type pet ON pet.event_type_id = et.id AND pet.pool_id = ?
    WHERE r.started_at >= ? AND r.started_at < ?
      AND r.state = 'finalized'
      AND pet.no_attendance = 0
),
attended AS (
    SELECT DISTINCT COALESCE(r.attendance_group_id, r.id) AS raid_key
    FROM raid_attendance ra
    JOIN raid r ON r.id = ra.raid_id
    JOIN pool_event_type pet ON pet.event_type_id = r.event_type_id AND pet.pool_id = ?
    WHERE ra.person_id = ?
      AND r.started_at >= ? AND r.started_at < ?
      AND r.state = 'finalized'
      AND pet.no_attendance = 0
      AND ra.credit_kind IN ('full', 'partial')   -- 'bench' and 'standby' are excluded
)
SELECT
    (SELECT count(*) FROM attended) AS attended,
    (SELECT count(*) FROM held)     AS held;
```

A slow, obviously-correct Go loop cross-checks this over 50 random member/window pairs on
`seed.Perf`. Two implementations disagreeing is how you find out which one is wrong.

## Cursor pagination

Cursor only. **Offset is banned on collections**: it drifts under concurrent inserts and is the
source of the duplicate-and-skip bugs in every bot that has ever polled EQdkp.

The cursor is base64 of `{sort_key, tiebreak_id}`, opaque, versioned and HMAC-signed. Always include
the tiebreak, or rows sharing a sort key are silently skipped.

```sql
-- name: ListRaidsAfter :many
SELECT * FROM raid
WHERE deleted_at IS NULL
  AND (started_at, id) < (?, ?)   -- (sort_key, tiebreak) from the decoded cursor
ORDER BY started_at DESC, id DESC
LIMIT ?;                          -- fetch limit+1 to compute has_more
```

SQLite supports row-value comparison (3.15+), so this is one index seek rather than the
`a < ? OR (a = ? AND b < ?)` expansion.

## Incremental sync

For a bot that was offline. Valid **only** on `/ledger/*`, `/audit` and `/events/replay` — those are
the only append-only collections with a meaningful sequence.

```sql
-- name: LedgerSince :many
SELECT b.seq, b.id, b.kind, b.effective_at, e.account_id, e.amount_cp
FROM ledger_batch b
JOIN ledger_entry e ON e.batch_id = b.id
WHERE b.pool_id = ? AND b.seq > ?
ORDER BY b.seq ASC
LIMIT ?;
```

## Full-text search

FTS5 with **external content** (`content='item'`), not contentless (`content=''`). Contentless
tables cannot return column content and require supplying old values on delete — a classic source of
silent index corruption when a sync trigger is slightly wrong.

```sql
CREATE VIRTUAL TABLE item_fts USING fts5(
    name, aliases, content='item', content_rowid='rowid', tokenize='unicode61'
);

-- name: SearchItems :many
SELECT i.* FROM item_fts f
JOIN item i ON i.rowid = f.rowid
WHERE item_fts MATCH ?
ORDER BY rank
LIMIT ?;
```

Fuzzy fallback is Levenshtein in Go over the top-N FTS hits. There is deliberately **no** separate
trigram table — two fuzzy-matching mechanisms is one too many.

Search sits behind a `Search` interface; Postgres returns `501 engine_unsupported` until 1.3.

## Case-insensitive name matching

Match on the stored `name_norm` column, never a collation. `name_norm` is computed in Go (NFKC,
casefold, strip `'` `` ` `` `-`) because SQLite's `lower()` is ASCII-only and has no NFKC, and
because `ALTER TABLE ADD COLUMN` cannot add a STORED generated column later.

```sql
-- name: FindCharacterByName :one
SELECT * FROM character WHERE name_norm = ? AND deleted_at IS NULL;
```

## Provisional item resolve — upsert, not insert

A second parse of the same unknown item name must reuse the existing provisional row, not collide
with the partial unique index on `name_norm`.

```sql
-- name: ResolveOrCreateProvisionalItem :one
INSERT INTO item (id, name, name_norm, state, created_at, updated_at)
VALUES (?, ?, ?, 'provisional', ?, ?)
ON CONFLICT (name_norm) WHERE deleted_at IS NULL AND state <> 'merged'
DO UPDATE SET updated_at = excluded.updated_at
RETURNING *;
```

---

## The three known dialect divergences

Listed on day one so the future Postgres port has them enumerated rather than discovered.

| # | Divergence | SQLite | Postgres (1.3) |
|---|---|---|---|
| 1 | **Per-pool `seq` allocation** | `SELECT COALESCE(max(seq),0)+1 FROM ledger_batch WHERE pool_id = ?` inside the write transaction, safe because `SetMaxOpenConns(1)` serialises writers | A per-pool sequence row locked `FOR UPDATE`, or an advisory lock — max+1 is **not** safe under real concurrency |
| 2 | **Bid-hold lock** | No-op; the single writer already serialises | `SELECT ... FOR UPDATE` on `account_lock`. The table ships in 1.0 unused so Postgres is a driver detail, not a schema change |
| 3 | **Full-text search** | FTS5 | `tsvector` + GIN, behind the `Search` interface (~120 lines each) |

Everything else is dialect-identical by design: integer micros instead of `timestamptz`, integer
centipoints instead of `NUMERIC`, ULID text keys instead of `uuidv7()`, and no queries into JSON.
That is what makes the port cheap, and it is why those four conventions are non-negotiable.

## Seq allocation, in full

```sql
-- name: NextPoolSeq :one
-- MUST run inside store.Tx (write pool, _txlock=immediate, SetMaxOpenConns(1)).
-- Divergence #1: this is NOT safe on Postgres. See the table above.
SELECT COALESCE(max(seq), 0) + 1 AS next_seq FROM ledger_batch WHERE pool_id = ?;
```
