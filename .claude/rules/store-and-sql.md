---
paths: ["internal/store/**", "db/queries/**/*.sql"]
description: The sqlc workflow, the hand-written Queries interface and its two compile assertions, store.Tx, the two SQLite pools, and the query bans CI greps for.
---

# Store and SQL

Query shapes live in [`db/RECIPES.md`](../../db/RECIPES.md). Find one there before writing a new one
— copying a working in-repo query beats recalling SQLite's dialect from memory, which is why that
file exists.

## The workflow

```
db/schema.hcl  ──atlas──►  db/migrations-sqlite/     (the schema sqlc parses against)
db/queries/*.sql ──sqlc(engine=sqlite)──►  internal/store/sqlitegen/   [SHIPPED]
                 ──sqlc(engine=postgresql)──►  internal/store/pggen/   [COMPILED IN CI ONLY]
```

Order matters and is not negotiable: schema, then query, then `make gen`, then the interface method,
then the service. Skip a step and `sqlc` fails to compile the query, or the build fails on the
unsatisfied interface — both are fast, both are the point.

- Raw SQL lives **only** in `db/queries/*.sql`. `db.Query`/`db.Exec`/`sql.Open` outside
  `internal/store` is repo gate `SQL001`/`SQL002` — AST analyzers in `internal/repogate`, run by
  `scripts/repo-gates.sh`. They read the call site, so a zero-argument `r.URL.Query()` is not a hit
  and a receiver renamed to `conn` still is.
- `internal/store/sqlitegen/` and `internal/store/pggen/` are generated. Never hand-edit; run
  `make gen` and commit the diff. `verify-generated` fails on drift.
- `emit_pointers_for_null_types: true` — NULL is a compile-visible concept, not a silent `""`.

## The `Queries` interface and the two assertions

`internal/store/store.go` is **hand-written**: ~180 methods, the contract both dialects satisfy.

```go
type Queries interface {
    GetGuild(ctx context.Context) (sqlitegen.Guild, error)
    BalanceAsOfSeq(ctx context.Context, arg sqlitegen.BalanceAsOfSeqParams) (int64, error)
    // ...
}

var _ Queries = (*sqlitegen.Queries)(nil)
var _ Queries = (*pggen.Queries)(nil) // CI-only build tag; compile-time Postgres proof
```

Those two lines are the entire mechanism keeping the post-1.0 Postgres port cheap. They cost nothing
and `go build` checks them on every save. **When you add a query you add the interface method in the
same change** — otherwise the sqlite implementation gains a method the contract doesn't know about
and the Postgres target silently rots.

Adding a method that only one dialect can satisfy means you have written a dialect divergence. There
are exactly three sanctioned ones (see `db/RECIPES.md`); a fourth is a design decision, not an
implementation detail. Stop and ask.

## `store.Tx` — every mutation, no exceptions

```go
err := s.store.Tx(ctx, func(q store.Queries) error {
    cur, err := q.GetPool(ctx, id)
    if err != nil { return fmt.Errorf("load pool %s: %w", id, err) }
    // ... reads and writes, all on q
    return nil
})
```

`Tx` uses the **write pool** exclusively. Reads outside a transaction go through `store.Q()` on the
read pool. Never open a transaction by hand, never hold a `*sql.DB` or `*sql.Tx` outside this
package, and never pass a `*sql.Tx` into a domain package — services see the `Queries` interface and
nothing else.

## The two pools, and their exact pragmas

Both pools carry the **same four pragmas**; only `_txlock` and the connection cap differ. The live
code is `internal/store/pragma.go`, and `TestPragmas_BothPools_MatchSpec` asserts every value below
by querying each pool.

```go
const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
    "&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"

writeDB := sql.OpenDB(connector(dsn + "?_txlock=immediate&" + pragmas))
writeDB.SetMaxOpenConns(1)

readDB := sql.OpenDB(connector(dsn + "?" + pragmas))
readDB.SetMaxOpenConns(max(4, runtime.NumCPU()))
```

| Setting | Why |
|---|---|
| `SetMaxOpenConns(1)` on the writer | One writer, **enforced in Go rather than discovered at runtime as `SQLITE_BUSY`**. It is also what makes `SELECT COALESCE(max(seq),0)+1` a safe sequence allocator on SQLite — the property that does *not* hold on Postgres |
| `_txlock=immediate`, write pool only | Takes the write lock at `BEGIN` instead of on first write, converting mid-transaction busy errors into a clean queue at the door. Never on the read pool: a reader taking the write lock serialises every read against the writer |
| `journal_mode(WAL)` | Readers never block the writer |
| `foreign_keys(ON)` | Off by default in SQLite. `parse_line` pruning depends on `ON DELETE SET NULL` actually firing |
| `synchronous(NORMAL)` | The correct pairing with WAL. `FULL` fsyncs every commit for no durability gain under WAL |
| `busy_timeout` 10 s, **both pools** | Absorbs the backup job's checkpoint pause |

> An earlier revision of this file gave the read pool `busy_timeout(5000)` and set neither
> `synchronous` nor `foreign_keys` on it, contradicting the PR 2 acceptance criteria in
> `docs/development/first-ten-prs.md`. The acceptance criteria won and the code follows them: the
> read pool is not a second-class connection, and a reader with `foreign_keys` off can observe a
> row a constraint should have pruned.

`sql.OpenDB` with a `Connector` rather than `sql.Open` with a driver name, because the statement
counter interposes on connections and `sql.Register` is process-global, panics on a repeated name
and cannot be undone.

Driver is `modernc.org/sqlite` (pure Go) — that is what makes `CGO_ENABLED=0`, cross-compilation and
`FROM scratch` possible at all.

Long jobs (import, replay) must **commit in chunks**. A 90-second transaction on the single writer
blocks every raid-night write; per-job locks serialise conflicting jobs and do nothing about writer
starvation.

## Query bans

**`total()` is banned — use `sum()`.** SQLite's `total()` returns a **REAL**. Every point value is
`INTEGER` centipoints, so `total()` would silently convert the ledger to floating point, would not
error, and would be quietly wrong by a fraction of a point for years. `sum()` returns `NULL` over
zero rows, so always `COALESCE(sum(amount_cp), 0)`. A repo grep gate rejects `total(` tree-wide.

**Never query into a JSON column.** `*_json` columns are validated on write and read whole. If you
need to filter, sort or aggregate on a fact, it is a real column. This is what keeps the Postgres
port cheap and the query planner honest.

**Offset is banned on collections.** Cursor pagination only, always with a tiebreak on the ULID, or
rows sharing a sort key are silently skipped. SQLite supports row-value comparison (3.15+), so use
`(started_at, id) < (?, ?)` rather than the `a < ? OR (a = ? AND b < ?)` expansion.

Also banned: floats and `REAL`/`NUMERIC`/`DECIMAL` columns anywhere; `BIGINT`/`BOOLEAN`/`DATETIME`
(every table is `STRICT`, which permits only `INT`, `INTEGER`, `REAL`, `TEXT`, `BLOB`, `ANY`); and
collations for case-insensitive matching — match on the stored `name_norm` column, normalised in Go.

## Statement-count budgets

A `database/sql` wrapper counts `QueryContext`/`ExecContext` per test; any test exceeding its
declared budget fails. This is the N+1 tripwire and the highest-value piece of test infrastructure
in an agent-heavy codebase.

- Declare a budget on every test that reads a collection.
- `GET /pools/{id}/standings` at 280 members is budgeted at **≤ 4 statements**, served from
  `balance_snapshot` and `attendance_rollup`, not from the ledger.
- The balance query is index-only; an `EXPLAIN QUERY PLAN` golden asserts no table access, because
  the day it starts scanning is the day standings gets slow and nobody notices.
- **Raising a budget is a review signal**, not a fix. The `test-integrity-auditor` subagent flags it.

## Before you commit a query

1. Does an equivalent shape already exist in `db/RECIPES.md`? Use it.
2. Is every aggregate `COALESCE(sum(...), 0)` and not `total()`?
3. Is the pagination cursor-with-tiebreak?
4. Does the `Queries` interface have the new method, and does `pggen` still compile?
5. If it is new and non-obvious, add it to `db/RECIPES.md` — that file is the reason the next agent
   gets it right.
