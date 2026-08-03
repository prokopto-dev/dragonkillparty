---
name: add-migration
description: Change the database schema. Use whenever work needs a new table, column, index, constraint, trigger or data backfill — including when another task (add-endpoint, add-strategy) stops because a column does not exist. Migrations are the only artefact where a mistake is unrecoverable for the user.
argument-hint: "[snake_case_name]"
allowed-tools: Read, Grep, Glob, Edit, Write, Bash(make migration *), Bash(make gen), Bash(make test), Bash(make check)
---

# Add a migration

`db/schema.hcl` is the single source of schema truth; Atlas generates the migration. Hand-writing one
is legitimate for exactly four things, listed below, and for nothing else.

The failure class this skill exists to prevent is **"works on a fresh install, breaks on upgrade"** —
invisible to the fast test loop unless someone specifically looks.

**Read first:** [`docs/design/00-canonical-conventions.md`](../../../docs/design/00-canonical-conventions.md)
§8 (database conventions) and §10 (the ledger).

---

## Steps

### 1. Confirm this is a schema change

A query change is not a schema change. Check `db/RECIPES.md` first — the shape you want may already
exist over the columns you already have.

Two agents adding *migrations* concurrently is fine. Two agents editing `schema.hcl` concurrently is
not. **Batch schema work into a deliberate schema PR with one owner at a time.**

### 2. Edit `db/schema.hcl`

| Rule | Value |
|---|---|
| `strict = true` | On every table. STRICT permits only `INT`, `INTEGER`, `REAL`, `TEXT`, `BLOB`, `ANY`. |
| **`BIGINT`, `BOOLEAN`, `DATETIME`, `NUMERIC`, `DECIMAL` are illegal** | Use `INTEGER` — it is already 64-bit. |
| Keys | `id TEXT NOT NULL PRIMARY KEY` — ULID. |
| Money | `*_cp INTEGER`. Never `REAL`, never a decimal string. |
| Ratios | `*_bp INTEGER` — basis points, 10000 = 100%. |
| Time | `*_at INTEGER` — Unix microseconds, UTC. Plus `*_day TEXT` (`YYYY-MM-DD`, guild-local) where day-bucketing is a domain concept. |
| Enums | `TEXT` + `CHECK (x IN ('a','b'))`, lowercase `snake_case`, generated from the Go catalogue. |
| Booleans | `INTEGER NOT NULL CHECK (x IN (0,1))`. |
| Names | `name TEXT` + `name_norm TEXT`, normalised **in Go** (NFKC + casefold + strip `'` `` ` `` `-`), then indexed. Not a generated column. |
| JSON | `*_json TEXT NOT NULL DEFAULT '{}'`, validated on write, **never queried into**. |
| Table names | Singular: `ledger_entry`, not `ledger_entries`. |
| Tenancy | **No `guild_id`. Not now, not "for later."** Canonical §9. |

If you need to filter or sort on a fact, it is a real column — not a key inside a `*_json` blob.
This is what keeps the Postgres port cheap and the query planner honest.

### 3. Generate

```bash
make migration NAME=add_bid_hold
```

Files are `NNNNNN_snake_case.sql`, numbered, **append-only**.

### 4. Read the generated SQL before doing anything else

Ask these four questions of the diff:

1. Does it rebuild a table? SQLite's 12-step rebuild **drops triggers and indexes** unless the
   rebuild re-creates them. Check that yours are re-created, by name.
2. Is it safe on a *populated* database, not just an empty one? A `NOT NULL` column with no default
   fails on every existing row.
3. Does it touch `ledger_batch` or `ledger_entry` with `UPDATE` or `DELETE`? If so, stop — the
   append-only trigger raises and it is correct to.
4. Is every new column a real column, not JSON you intend to query into?

### 5. Hand-edit only inside the allowlist

`Edit` on `db/migrations-*/**` is denied by policy and the prompt is deliberately slightly annoying.
Migrations should be. Atlas cannot express four things, and these are the only legitimate reasons to
extend a generated migration by hand:

| # | Category | Example |
|---|---|---|
| 1 | **Append-only triggers** | `CREATE TRIGGER ledger_entry_no_update BEFORE UPDATE ON ledger_entry BEGIN SELECT RAISE(ABORT, 'ledger is append-only'); END;` |
| 2 | **Partial and expression indexes** | `CREATE UNIQUE INDEX ux_bid_live ON bid_session(item_instance_id) WHERE state IN ('draft','open','extended','closing');` |
| 3 | **`CHECK` constraints Atlas cannot round-trip** | `CHECK (amount_cp <> 0)` |
| 4 | **Data backfills** | Populating `name_norm` for existing rows after adding the column. |

Anything else you are tempted to hand-write is a sign that `schema.hcl` is wrong. Fix `schema.hcl`
and regenerate.

### 6. Never edit a shipped migration

A migration listed in `db/migrations-sqlite/SHIPPED.lock` has run on somebody's real database.
Editing it means goose's applied-version table disagrees with the file, and the next upgrade is
undefined. **Write a new migration.** There is no exception and no "it only shipped in edge."

### 7. `make gen`

Regenerates `internal/store/sqlitegen/` and compiles the `internal/store/pggen/` target. If
`var _ Queries = (*pggen.Queries)(nil)` stops compiling, the Postgres port just got more expensive —
fix it now, at zero cost, rather than in 1.3.

### 8. Verify the upgrade path against the previous release

**This is the step people skip, and it is the whole point of the skill.** A fresh-install test proves
nothing about the officer whose database has six years of raids in it.

```bash
# The reference database published by the last release.
docker pull ghcr.io/dragonkillparty/dkp-refdb:$(gh release view --json tagName -q .tagName)
./scripts/upgrade-test.sh "$(gh release view --json tagName -q .tagName)"
```

The upgrade must satisfy all four:

- [ ] Migrations apply cleanly against the populated refdb.
- [ ] `dkp verify-ledger` is clean afterwards.
- [ ] `/readyz` returns 200.
- [ ] Protected-table row counts are **non-decreasing** (`ledger_batch`, `ledger_entry`, `raid`,
      `person`, `character`, `item_award`).

If `scripts/upgrade-test.sh` does not exist yet, say so explicitly and run the equivalent by hand —
do not report the migration as verified on a fresh install alone.

### 9. Tests

| Test | When |
|---|---|
| Fresh-install fingerprint | Always — the schema after replay matches `atlas schema inspect`. |
| N-1 upgrade against the refdb | Always. |
| Trigger-fires test | Whenever you add a trigger. Assert `UPDATE` and `DELETE` both raise. A guardrail with no test asserting it fires can be silently regressed. |
| Migration-failure auto-restore | Whenever the migration rebuilds a table: break it deliberately on a scratch branch, assert exit 1 and a byte-identical database. |
| `seed_profile_test` floors | Whenever you add a table the seeder populates — row-count floors are non-decreasing. |

### 10. `make check`

---

## Stop and ask if

- **You want to drop or rename a column that has shipped.** That is a data-loss migration and needs a
  human decision plus a deprecation window.
- **The change would need `UPDATE` or `DELETE` on a ledger table.** Corrections are reversal batches
  with `reverses_batch_id` set — always, in Go, in SQL, and in migrations (canonical §10).
- **The migration cannot be preceded by an automatic snapshot**, or needs a documented manual step.
  That is a **MAJOR** version bump under the SemVer policy, not a patch.
- **You are adding `guild_id`.** Single-guild is a locked decision. Multi-guild is a deliberate future
  project on Postgres with RLS, not a retrofit hiding in this schema.
- **Atlas produced a rebuild you do not fully understand.** A wrong 12-step rebuild silently drops an
  index and standings gets slow six months later.
