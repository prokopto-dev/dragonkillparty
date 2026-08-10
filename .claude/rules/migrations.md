---
paths: ["db/schema.hcl", "db/migrations-*/**"]
description: Atlas authors and goose applies; the four cases where hand-editing a generated migration is legitimate, and the frozen-migration rule.
---

# Migrations

## The flow

```
internal/ledger/kinds/          the enum catalogues — canonical §5's one Go const block per
internal/audit/kinds/            vocabulary, LEAF packages so this step compiles before sqlc has run
      │  make gen  (scripts/gen-enums.sh)
      ▼
db/schema.hcl                    the SINGLE source of schema truth — you edit this, EXCEPT the
      │                          regions between the GENERATED markers, which make gen owns
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

The generated enum CHECKs are the parts of `db/schema.hcl` you do not edit: `ledger_batch.kind` and
`ledger_batch.source` from `internal/ledger/kinds`, and `audit_log.actor_kind` from
`internal/audit/kinds`. `make gen` writes each CHECK expression between its own `BEGIN/END
GENERATED` markers (canonical §5) — the marker text names the catalogue, because a whole-line match
is how each render finds its region and only its region. Add the value in Go, run `make gen`, then
`make migration NAME=<snake_case>`. `TestLedgerKinds_CheckMatchesCatalogue` and
`TestAuditKinds_CheckMatchesCatalogue` fail on a hand-edit, and so does `verify-generated` —
`db/schema.hcl` is in `GENERATED_PATHS` for exactly those regions.

A new vocabulary joins them by adding a catalogue package (a stdlib-only leaf over
`internal/schemaenum`, which owns the CHECK rendering and the region rewrite) and one row in
`internal/ledger/enumgen`'s `catalogues()`. Three enums here are still bare literals and have not
had that done: `account.kind`, `account.system_key` and `audit_log.outcome`.

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

> **Verified in Phase 0 PR 3 (2026-08-06), and the answer changes how you use this table.** See item
> V6 of `docs/development/verify-before-phase-0.md` for the experiment and the evidence.
>
> - **Partial indexes and CHECKs are NOT special cases.** Atlas expresses both in `schema.hcl`
>   (`index "x" { where = … }`, `check { expr = … }`) and re-creates them across a 12-step rebuild.
>   Put them in `schema.hcl`; cases 2 and 3 above are for the residue Atlas genuinely cannot express,
>   which so far is nothing.
> - **Triggers are the real case, and they are fragile.** The community edition cannot express a
>   trigger at all. A trigger it cannot see does not provoke a `DROP TRIGGER` on an ordinary diff —
>   but a table rebuild emits `DROP TABLE` and re-creates **nothing**, silently. Any migration that
>   rebuilds a table carrying a trigger MUST re-create it after the rename, in the same file.
> - The backstop is `TestMigrate_FreshInstall_MatchesFingerprint`, which fingerprints every
>   `sqlite_schema` row including `type='trigger'`.

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

- `db/migrations-sqlite/SHIPPED.lock` lists them, one `filename sha256` row per migration. The
  `schema-migration-reviewer` subagent checks the diff against it.
- **`MIG003` in `scripts/repo-gates.sh` is the machine half**, on every PR, in `ci-required`. Two
  assertions, and the second is what makes the first mean anything:
  1. Every listed file exists and still hashes to its recorded value.
  2. The manifest at the **merge base** is an exact byte **prefix** of the manifest now.
  Without (2), (1) is defeated by editing the migration and its row in the same commit — or by
  deleting the row, which un-freezes the file entirely. The manifest ships in the same diff as the
  migration it protects, so it is only trustworthy against its own history. (2) reads git, so it
  skips loudly without one; `lint / repo` carries `fetch-depth: 0` and a test asserts that it does.
- It is **not** a completeness check, deliberately: a migration added on a feature branch has not
  shipped and must not be listed. Completeness is asserted once, at tag time, by
  `make release-shipped-lock` in `release.yml`'s `prepare` job — at a tag everything present ships,
  so an unlisted migration there is a hole in the record.
- Rows are appended by `make shipped-lock-seal` when a release is prepared, **in the Release PR**.
  Nothing in CI pushes to `main`, and a record written by the job that consumes it is not a record.
- **This is not what `atlas.sum` already does.** `atlas.sum` protects the current set as it *is*:
  edit a migration, re-run `atlas migrate hash`, and `make verify-generated` is satisfied, because
  it only asks whether regenerating changes anything. `SHIPPED.lock` records what a user's database
  has already executed, which nothing in this repository may rewrite.
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
them after the rename**, in the same file, and a test must assert the trigger still fires.

Two tests cover that, and they cover different halves.
`TestTriggers_MutatingLedger_Raises` (`internal/ledger/trigger_test.go`) asserts all four
append-only triggers fire — on a database that applied every migration in one go, so it notices a
trigger that was never created. `TestMigrate_FullStack_LedgerDataSurvivesUpgrade`
(`test/migrations/populated_upgrade_test.go`) is the one that notices a trigger that was created and
then dropped: it seeds a real pool, account, batches and entries with foreign keys enforced, applies
a fixture migration that rebuilds `ledger_entry`, and requires both the rows and all four triggers
to survive. Its negative control applies the same rebuild with the trigger re-creation removed and
proves that the migration still succeeds, `integrity_check` and `foreign_key_check` still pass, and
the ledger is silently editable afterwards. **That is what "silently" means here** — write the
rebuild so the first test is the one that describes your migration.

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
