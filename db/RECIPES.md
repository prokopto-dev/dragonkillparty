# Query recipes

The query shapes actually used in this codebase. **Find one here before writing a new one.** Copying a
working in-repo query is far more reliable than recalling SQLite's dialect from memory, which is the
whole reason this file exists.

Raw SQL lives only in `db/queries/*.sql`, consumed by sqlc. `db.Query` and `db.Exec` outside
`internal/store` are grep-banned (gate SQL002).

The two **runnable** recipes below (the `guild` singleton fetch and the `dkp_meta` upsert) are
extracted and type-checked by `TestDocs_ExampleEndpointSnippets_Compile`
(`internal/api/docs_snippets_test.go`): they are run through `sqlc` against the committed migration
set, so a recipe that names a column the schema does not have fails CI. The **forward-looking** recipes
further down query tables that do not exist yet (`person`, `raid`, `item_fts` and friends); they are
marked as such and fenced out of the compile gate, because a query cannot be type-checked against a
table nobody has migrated. They land, unfenced, with the schema they query — phase by phase.

`ledger_batch`, `ledger_entry` and `balance_snapshot` now **exist** (Phase 0 PR 9): the real queries
live in `db/queries/ledger.sql` and are compiled by `sqlc` on every `make gen`. The ledger recipes
below stay fenced as `text` rather than `sql` on purpose — the snippet gate's `sqlc`/`atlas` step
`t.Skip`s in the integration job (no `sqlc` there), so un-fencing them adds zero CI coverage while
risking the per-job-tool trap; the real queries carry the coverage regardless. The shapes below are
kept in step with `db/queries/ledger.sql`, which is the authority.

---

## Two bans, and why

**`total()` is banned. Use `sum()`.**

SQLite's `total()` returns a **REAL** — a float — where `sum()` returns an INTEGER for integer inputs.
Since every point value is `INTEGER` centipoints, `total()` would silently convert the entire ledger to
floating point and defeat the invariant the whole product rests on (canonical §1). It would not error.
It would just be quietly wrong, by a fraction of a point, for years.

`sum()` returns `NULL` over zero rows, so wrap it: `COALESCE(sum(amount_cp), 0)`.

A repo grep gate (`MONEY002` in `scripts/repo-gates.sh`) rejects `total(` anywhere in the tree, and
`TestRecipes_TotalIsBanned` proves the gate fires on a fixture query that contains it.

**Never query into a JSON column.** `*_json` columns are validated on write and read whole. If you need
to filter or sort on a fact, it is a real column. No exceptions — this is what keeps the Postgres port
cheap and the query planner honest.

---

## Singleton fetch — `guild` (runnable)

There is exactly one guild row, keyed on `id = 1` (canonical §9). `GetGuild` reads it without a
predicate: the schema `CHECK (id = 1)` guarantees a single row, so no filter is needed and the query
carries no numeric literal. Selecting the columns explicitly — rather than `SELECT *` — is what makes
sqlc emit a stable, named row struct that the `store.Queries` interface can pin.

```sql
-- name: GetGuild :one
SELECT
    id, name, tag, timezone, week_start, points_label, points_precision,
    inactive_after_days, auto_set_inactive, hide_inactive, created_at, updated_at
FROM guild;
```

## Upsert — `dkp_meta` (runnable)

SQLite needs an explicit conflict target, and `excluded` is the would-be-inserted row. `dkp_meta` is
the instance's key/value state (`schema_version` and friends); every value is `TEXT` and parsed by the
caller — a `REAL` or `NUMERIC` column here would be the first float in a database whose central
invariant is that there are none.

```sql
-- name: UpsertMetaValue :exec
INSERT INTO dkp_meta (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT (key) DO UPDATE SET
    value      = excluded.value,
    updated_at = excluded.updated_at;
```

The `dkp_meta` read is the mirror of it — one row, by key:

```sql
-- name: GetMetaValue :one
SELECT value FROM dkp_meta WHERE key = ?;
```

## Whole-row update with RETURNING — `guild` (runnable)

A PATCH reads the current row, merges the patch in Go (that is domain logic — see
`internal/api/EXAMPLE_ENDPOINT.md` step 4), and hands this query a full set of values. A
`COALESCE`-per-column update would put the merge in SQL, where absent and set-to-the-current-value
become indistinguishable and it cannot be unit-tested without a database. `RETURNING` the new row lets
the handler emit the fresh representation and its new ETag in one round trip. `id` is never updated: it
is the singleton key.

```sql
-- name: UpdateGuild :one
UPDATE guild SET
    name                = ?,
    tag                 = ?,
    timezone            = ?,
    week_start          = ?,
    points_label        = ?,
    points_precision    = ?,
    inactive_after_days = ?,
    auto_set_inactive   = ?,
    hide_inactive       = ?,
    updated_at          = ?
WHERE id = 1
RETURNING
    id, name, tag, timezone, week_start, points_label, points_precision,
    inactive_after_days, auto_set_inactive, hide_inactive, created_at, updated_at;
```

---

# Forward-looking recipes — the tables do not exist yet

Everything below queries a table no migration has created. The shapes are recorded here so the schema
they belong to has a query pattern waiting when it lands, but they are **not** run through the compile
gate — a query cannot be type-checked against a table nobody has migrated. Each moves up into the
runnable section, in a real `db/queries/*.sql` file, in the PR that ships its table (the ledger tables
arrive in Phase 0 PR 9; roster, raids and items later). The fences below are tagged `text`, not `sql`,
so the snippet gate skips them; unfence them to `sql` when the table exists.

## Additive upsert — `balance_snapshot` (Phase 0 PR 9, live in `db/queries/ledger.sql`)

The snapshot is a cache, maintained synchronously in the same transaction as the write, and it is
**load-bearing rather than droppable**
([ADR-0023](../docs/adr/0023-balance-snapshot-is-load-bearing.md)): the log stays the only source of
truth, but nothing serves the standings page from it in under 22 seconds, so losing this table is a
rebuild. It is upserted **additively**: the caller passes this batch's per-account delta (the SUM and
COUNT of just its entries) and the running total accumulates. The primary key is
`(pool_id, account_id, balance_kind)` — the same order the `WITHOUT ROWID` table is built on — and the
conflict target matches it exactly. `entry_count` is carried alongside `amount_cp` (per the domain
model) so the nightly drift check has both a sum and a count to compare against the fold.

There is deliberately no `WHERE excluded.as_of_seq > ...` guard: under the single writer the snapshot
only ever moves forward, one batch at a time, and an additive upsert with a backward guard would
silently drop a legitimate delta.

```text
-- name: UpsertBalanceSnapshot :exec
INSERT INTO balance_snapshot (pool_id, account_id, balance_kind, amount_cp, as_of_seq, entry_count, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (pool_id, account_id, balance_kind) DO UPDATE SET
    amount_cp   = amount_cp   + excluded.amount_cp,    -- ADDITIVE: accumulate the batch's delta
    entry_count = entry_count + excluded.entry_count,
    as_of_seq   = excluded.as_of_seq,
    updated_at  = excluded.updated_at;
```

## Balance as of a seq — `ledger_entry` (Phase 0 PR 9, live in `db/queries/ledger.sql`)

The definitional query. A balance is a `SUM` over an append-only log, positioned by `seq` — never by
timestamp, because a backdated `effective_at` must not change what a past balance *was*. It filters the
**denormalised `seq`** on `ledger_entry` directly, with **no join to `ledger_batch`**: the seq is
carried on every entry precisely so this query is served entirely from the covering index
`ix_entry_balance(pool_id, account_id, balance_kind, seq, amount_cp)` with no table access. An
`EXPLAIN QUERY PLAN` golden (`test/golden/explain/ledger_balance.txt`) asserts that, because the day it
starts scanning is the day standings gets slow and nobody notices. Note `sum(...)`, never `total(...)`
(which returns a REAL); the `CAST(... AS INTEGER)` pins sqlc's result type to `int64` rather than
`interface{}`, since an aggregate loses column affinity.

```text
-- name: BalanceAsOfSeq :one
SELECT CAST(COALESCE(sum(amount_cp), 0) AS INTEGER) AS amount_cp
FROM ledger_entry
WHERE pool_id = ? AND account_id = ? AND balance_kind = ? AND seq <= ?;
```

## Account statement with a running balance — `ledger_entry` (Phase 0 PR 9)

The screen that settles most loot arguments. Window function, one pass. Paginated by cursor on `seq` in
the real implementation — offset pagination is banned on collections (see below).

```text
-- name: AccountStatement :many
SELECT
    b.seq, b.kind, b.reason, b.effective_at, b.recorded_at, b.reverses_batch_id,
    e.amount_cp,
    sum(e.amount_cp) OVER (ORDER BY b.seq
                           ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS running_cp
FROM ledger_entry e
JOIN ledger_batch b ON b.id = e.batch_id
WHERE e.account_id = ? AND e.pool_id = ? AND e.balance_kind = 'points'
ORDER BY b.seq DESC
LIMIT ?;
```

## Standings — `person` + `balance_snapshot` (Phase 2)

The heaviest read in the product: every active person's balance in a pool, sorted, budgeted at **≤ 4
statements for 280 members**. Read from the snapshot, not the ledger.

```text
-- name: Standings :many
SELECT
    p.id AS person_id, p.display_name, c.name AS main_character, c.class_id,
    COALESCE(s.amount_cp, 0) AS balance_cp,
    COALESCE(r.attended, 0)  AS attended_30,
    COALESCE(r.held, 0)      AS held_30
FROM person p
JOIN account a ON a.person_id = p.id
LEFT JOIN character c ON c.id = p.main_character_id
LEFT JOIN balance_snapshot s ON s.account_id = a.id AND s.pool_id = ? AND s.balance_kind = 'points'
LEFT JOIN attendance_rollup r ON r.person_id = p.id AND r.pool_id = ? AND r.window_days = 30
WHERE p.state = 'active' AND p.deleted_at IS NULL
ORDER BY balance_cp DESC, p.display_name ASC
LIMIT ?;
```

## Cursor pagination — `raid` (Phase 2)

Cursor only. **Offset is banned on collections**: it drifts under concurrent inserts and is the source
of the duplicate-and-skip bugs in every bot that has ever polled EQdkp. The cursor is base64 of
`{sort_key, tiebreak_id, filter_hash, principal_class}`, opaque, versioned and HMAC-signed — only the
first two feed the query below, the other two are what `Decode` verifies before you get them. Always
include the tiebreak, or rows sharing a sort key are silently skipped. SQLite supports row-value comparison (3.15+), so this is one
index seek rather than the `a < ? OR (a = ? AND b < ?)` expansion.

```text
-- name: ListRaidsAfter :many
SELECT id, started_at, state
FROM raid
WHERE deleted_at IS NULL
  AND (started_at, id) < (?, ?)   -- (sort_key, tiebreak) from the decoded cursor
ORDER BY started_at DESC, id DESC
LIMIT ?;                          -- fetch limit+1 to compute has_more
```

## Keyset over a composite primary key — `balance_snapshot` (Phase 1, live in `db/queries/ledger.sql`)

The same row-value seek as the cursor recipe above, applied to a **job** rather than to an endpoint:
`dkp verify-ledger` (issue #198) walks every cached balance in a pool and compares it against a fold
over the log. It pages for one reason and it is not politeness — a `:many` materialises its whole
result set as a Go slice, so an unpaged walk of the ledger is the ledger in memory, on a Raspberry Pi.
Paged, the verifier's footprint is one page plus one accumulator per account, whatever the log's size.

Two things here that the `raid` recipe does not show:

**The cursor is the primary key itself.** `balance_snapshot` is `WITHOUT ROWID` on
`(pool_id, account_id, balance_kind)`, so `ORDER BY account_id, balance_kind` with the pool fixed *is*
the table's own order — the seek walks the table b-tree rather than an index and a lookup. Start at
`('', '')`: the empty string sorts before every ULID, so no "first page" special case is needed.

**Name the cursor parameters.** sqlc names a positional parameter after the column it is compared
to, so `(account_id, balance_kind) > (?, ?)` generates `AccountID` and `AccountID_2` — and a caller
passing a balance kind to a field called `AccountID_2` is one edit away from passing them in the wrong
order, which is a page boundary that silently skips rows. `sqlc.arg(...)` costs one line per parameter
and makes the params struct say what it holds.

```text
-- name: ListSnapshotsAfter :many
SELECT account_id, balance_kind, amount_cp, as_of_seq, entry_count
FROM balance_snapshot
WHERE pool_id = sqlc.arg(pool_id)
  AND (account_id, balance_kind) > (sqlc.arg(cursor_account_id), sqlc.arg(cursor_balance_kind))
ORDER BY account_id, balance_kind
LIMIT sqlc.arg(page_limit);
```

## Full-text search — `item_fts` (Phase 3)

FTS5 with **external content** (`content='item'`), not contentless — contentless tables cannot return
column content and are a classic source of silent index corruption. Fuzzy fallback is Levenshtein in Go
over the top-N FTS hits; there is deliberately no separate trigram table. Search sits behind a `Search`
interface, and Postgres returns `501 engine_unsupported` until the `tsvector` implementation lands.

```text
-- name: SearchItems :many
SELECT i.id, i.name FROM item_fts f
JOIN item i ON i.rowid = f.rowid
WHERE item_fts MATCH ?
ORDER BY rank
LIMIT ?;
```

---

## The three known dialect divergences

Listed on day one so the future Postgres port (post-1.0) has them enumerated rather than discovered.
**Divergence #1 is now LIVE** — `ledger_batch` exists and the seq allocator ships in
`db/queries/ledger.sql` as `NextPoolSeq` (Phase 0 PR 9). The other two stay forward-looking: neither
`account_lock` nor `item_fts` exists yet.

| # | Divergence | SQLite | Postgres (post-1.0) | Status |
|---|---|---|---|---|
| 1 | **Per-pool `seq` allocation** | `SELECT COALESCE(max(seq),0)+1 FROM ledger_batch WHERE pool_id = ?` inside the write transaction, safe because `SetMaxOpenConns(1)` serialises writers | A per-pool sequence row locked `FOR UPDATE`, or an advisory lock — max+1 is **not** safe under real concurrency | **live** (PR 9) |
| 2 | **Bid-hold lock** | No-op; the single writer already serialises | `SELECT ... FOR UPDATE` on `account_lock` | forward-looking |
| 3 | **Full-text search** | FTS5 | `tsvector` + GIN, behind the `Search` interface | forward-looking |

Everything else is dialect-identical by design: integer micros instead of `timestamptz`, integer
centipoints instead of `NUMERIC`, ULID text keys instead of `uuidv7()`, and no queries into JSON. That
is what makes the port cheap, and it is why those conventions are non-negotiable.

The seq allocator, in full — `COALESCE(max(...), 0) + 1`, never `total(...)`. This is the LIVE query in
`db/queries/ledger.sql`; the `CAST(... AS INTEGER)` pins sqlc's result type to `int64` (an aggregate
otherwise loses affinity). `MaxPoolSeq` (the current head, without the `+ 1`) sits beside it for the
"as of the latest seq" case.

```text
-- name: NextPoolSeq :one
-- MUST run inside store.Tx (write pool, _txlock=immediate, SetMaxOpenConns(1)).
-- Divergence #1: this is NOT safe on Postgres. See the table above.
SELECT CAST(COALESCE(max(seq), 0) + 1 AS INTEGER) AS next_seq FROM ledger_batch WHERE pool_id = ?;
```
