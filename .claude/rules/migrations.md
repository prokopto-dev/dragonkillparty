---
paths: ["db/schema.hcl", "db/migrations-*/**"]
description: Atlas authors and goose applies; the four cases where hand-editing a generated migration is legitimate, and the frozen-migration rule.
---

# Migrations

## The flow

```
db/schema.hcl                    the SINGLE source of schema truth — you edit this
      │  make migration NAME=add_bid_hold
      │  → atlas migrate diff --dev-url "sqlite://file?mode=memory"
      ▼
db/migrations-sqlite/NNNNNN_add_bid_hold.sql     generated, reviewed, committed
db/migrations-postgres/…                          generated, compiled in CI only
      │  go:embed
      ▼
goose v3, at boot                 applies. Atlas is a dev/CI dependency the user never installs
```

Adding Atlas to the runtime would break the single-binary promise, which is why the authoring tool
and the applying tool are different tools.

**Never** hand-write a migration from scratch. Change `db/schema.hcl`, run
`make migration NAME=<snake_case>`, read the generated SQL, commit it. `verify-generated` fails if
the committed migration does not match a regeneration from the schema.

CI additionally runs `atlas schema inspect` on both dialects after applying both migration sets and
asserts the normalised logical schemas match a committed fingerprint — that is what keeps the
Postgres target honest without a Postgres runtime.

## The hand-edit allowlist

Atlas cannot express four things this schema requires. Hand-editing a generated migration is
legitimate **only** for these, and the edit goes at the end of the generated file under a comment
naming which case it is:

| # | Case | Example |
|---|---|---|
| 1 | **Append-only triggers** | `CREATE TRIGGER trg_ledger_entry_no_update BEFORE UPDATE ON ledger_entry BEGIN SELECT RAISE(ABORT, 'ledger_entry is append-only'); END;` |
| 2 | **Partial unique indexes** | `CREATE UNIQUE INDEX ux_bid_live ON bid_session(item_instance_id) WHERE state IN ('draft','open','extended','closing','resolved');` |
| 3 | **CHECK constraints Atlas drops or cannot infer** | `CHECK (amount_cp <> 0)`, `CHECK (x IN (0,1))`, the guild-singleton check |
| 4 | **Data backfills** | populating `name_norm` for existing rows after adding the column |

Anything else — renaming a column by hand, "just fixing" a generated `ALTER`, adding a table
directly — is wrong. Change `db/schema.hcl` and regenerate.

> **Empirical assumption, verify before relying on it:** that Atlas *preserves* hand-added triggers,
> partial indexes and CHECKs across a subsequent `migrate diff`. Phase 0 has a one-hour spike (add a
> trigger, change an unrelated column, diff). If Atlas drops them, the append-only guarantee is at
> risk on every schema change and this flow needs a preservation step.

### Backfills: two hard rules

- A backfill may **never** `UPDATE` or `DELETE` a `ledger_batch` or `ledger_entry` row. The triggers
  will abort the migration, which is the correct outcome; do not work around them by dropping the
  trigger and recreating it. To correct ledger data, write a reversal batch at runtime.
- A backfill must be safe on a **populated** database, not just an empty one, and must be
  idempotent — migration-on-boot can be interrupted and the snapshot restored, and the officer will
  run it again.

## Shipped migrations are frozen

A migration that has appeared in a tagged release is immutable. Editing it means an existing install
and a fresh install end up with different schemas, and "works on fresh install, breaks on upgrade"
is the most damaging bug class for this audience — a volunteer officer with ten years of guild DKP
and no backup discipline.

- `db/migrations-sqlite/SHIPPED.lock` lists them. The `schema-migration-reviewer` subagent checks
  the diff against it.
- A migration round-trip test applies all migrations to an empty DB and, separately, to a copy of
  the previous release's schema fixture, then asserts both fingerprints match.
- To change something a shipped migration created, write a **new** migration.

## SQLite's 12-step table rebuild

SQLite's `ALTER TABLE` cannot drop or retype a column, so any such change becomes the documented
12-step rebuild: create a new table, copy, drop the old, rename.

**Let Atlas generate that rebuild.** A hand-written rebuild silently loses every trigger, index and
partial index attached to the old table — including the append-only triggers, which is exactly the
failure mode where the product's trust argument evaporates without a single test going red.

If a rebuild touches a table that carries hand-allowlisted objects, the migration must **re-create
them after the rename**, in the same file, and a test must assert the trigger still fires. The
integration test `TestLedger_Update_RaisesTrigger` exists precisely so a rebuild that eats the
trigger fails CI.

## Column conventions

Every table is `STRICT`. `STRICT` permits only `INT`, `INTEGER`, `REAL`, `TEXT`, `BLOB`, `ANY` —
`BIGINT`, `BOOLEAN`, `DATETIME`, `NUMERIC` and `DECIMAL` are **illegal** and will fail at
`CREATE TABLE`.

```
id           TEXT    NOT NULL PRIMARY KEY   -- ULID, 26 chars
*_at         INTEGER                        -- Micros (int64 Unix microseconds, UTC)
*_day        TEXT                           -- 'YYYY-MM-DD', guild-local, where day is a domain concept
*_cp         INTEGER                        -- centipoints. Never REAL, never NUMERIC
*_bp         INTEGER                        -- basis points, 10000 = 100%
*_json       TEXT NOT NULL DEFAULT '{}'     -- validated on write, NEVER queried into
enum         TEXT + CHECK (x IN ('a','b'))  -- lowercase snake_case, identical to the wire value
boolean      INTEGER NOT NULL CHECK (x IN (0,1))
name_norm    TEXT                           -- normalised in Go (NFKC + casefold + strip ' ` -)
```

Tables are **singular** (`ledger_entry`). `name_norm` is a plain column, not a generated one:
generated-column expressions may use only deterministic scalar functions, core SQLite has no NFKC
and `lower()` is ASCII-only — and `ALTER TABLE ADD COLUMN` cannot add a `STORED` column, so every
future normalisation change would force a 12-step rebuild.

**There is no `guild_id` column.** Not on a new table, not "for later".

## Boot behaviour you are changing when you add a migration

1. `schema_version` newer than the binary → **refuse to start** with the restore command printed.
2. Pending migrations and `DKP_AUTO_MIGRATE != false` → `VACUUM INTO` a zstd snapshot **first**,
   then apply per-migration with `PRAGMA integrity_check` after each.
3. Any failure → **automatically restore the snapshot**, exit 1, name the failing migration.

A test seeds a DB, injects a migration that fails halfway, and asserts the file is byte-identical to
the pre-migration snapshot. Write your migration so that path works: one logical change per file,
integrity-check-clean, and reversible by restore rather than by a `Down` section.

## Stop and ask if

- The change is destructive (drops a column, drops a table, narrows a type). It needs the
  `!destructive-migration` label and a human.
- You are tempted to hand-edit for a fifth reason.
- A new column would hold a queryable fact inside a `*_json` blob.
