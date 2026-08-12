---
paths: ["db/schema.hcl", "db/migrations-*/**"]
description: Atlas authors and goose applies; the four cases where hand-editing a generated migration is legitimate, and the frozen-migration rule.
---

# Migrations

## The flow

```
internal/ledger/kinds/          the enum catalogues — canonical §5's one Go const block per
internal/audit/kinds/            vocabulary, LEAF packages so this step compiles before sqlc has run
internal/account/kinds/
      │  make gen  (scripts/gen-enums.sh)
      ▼
db/schema.hcl                    the SINGLE source of schema truth — you edit this, EXCEPT the
      │                          regions between the GENERATED markers, which make gen owns
      │  make migration NAME=add_bid_hold
      │  → atlas migrate diff --env sqlite    (atlas.hcl declares the dev database)
      ▼
db/migrations-sqlite/NNNNNN_add_bid_hold.sql     generated, reviewed, committed
db/migrations-postgres/…                          generated, compiled in CI only
      │  go:embed
      ▼
goose v3, at boot                 applies. Atlas is a dev/CI dependency the user never installs
```

Adding Atlas to the runtime would break the single-binary promise, which is why the authoring tool
and the applying tool are different tools.

**The dev database is named per invocation, and that is about a lock rather than about data.** Atlas
derives a machine-wide advisory lock name from the dev-url, and invocations that share one do not
queue — the losers exit 1 with `acquiring database lock: sql/sqlite: lock on "atlas_migrate_diff_…"
already taken`, which reads as a broken toolchain rather than as a collision, on the change most
likely to have caused it (#36). `atlas.hcl` computes a fresh name per invocation, so nothing needs to
pass `--dev-url` and nothing may pin one: `TestAtlasHCL_DevURL_IsPerInvocation`,
`TestAtlas_FixedDevURL_AppearsNowhere` and `TestAtlas_ConcurrentInvocations_DoNotShareALock` in
`test/repo/` hold that.

**Never** hand-write a migration from scratch. Change `db/schema.hcl`, run
`make migration NAME=<snake_case>`, read the generated SQL, commit it. `verify-generated` fails if
the committed migration does not match a regeneration from the schema.

The generated enum CHECKs are the parts of `db/schema.hcl` you do not edit: `ledger_batch.kind` and
`ledger_batch.source` from `internal/ledger/kinds`, `audit_log.actor_kind` and `audit_log.outcome`
from `internal/audit/kinds`, and `account.kind` and `account.system_key` from
`internal/account/kinds`. `make gen` writes each CHECK expression between its own `BEGIN/END
GENERATED` markers (canonical §5) — the marker text names the catalogue, because a whole-line match
is how each render finds its region and only its region. Add the value in Go, run `make gen`, then
`make migration NAME=<snake_case>`. `TestLedgerKinds_CheckMatchesCatalogue`,
`TestAuditKinds_CheckMatchesCatalogue` and `TestAccountKinds_CheckMatchesCatalogue` fail on a
hand-edit, and so does `verify-generated` — `db/schema.hcl` is in `GENERATED_PATHS` for exactly those
regions.

A new vocabulary joins them by adding a catalogue package (a stdlib-only leaf over
`internal/schemaenum`, which owns the CHECK rendering and the region rewrite) and one row in
`internal/ledger/enumgen`'s `catalogues()`. Every string-enum CHECK in the schema is now generated;
the next one added has no excuse to be a literal — and **`ENUM001` in `scripts/repo-gates.sh` is the
machine half of that sentence**, because the three `CheckMatchesCatalogue` tests each compare their
own region with their own catalogue and none of them can see a seventh vocabulary that has no
catalogue at all. A `check` block in `db/schema.hcl` whose `expr` lists quoted values — in either SQL
quote form, since SQLite makes a string literal out of `'x'` and out of a double-quoted token that
matches no column — and does not lie between `BEGIN`/`END GENERATED` markers fails the gate. Boolean
CHECKs (`x IN (0, 1)`) and index predicates are not string enums and are not caught.

**A region is generated when a catalogue owns it, not when the schema says so.** The markers are
comments, so wrapping a new literal in a balanced pair would otherwise be a self-service exemption —
and `make gen` would not notice either, because it rewrites only the regions its catalogues declare.
So the marker line must match, whole, a `schemaEnumBegin`/`schemaEnumEnd` const in an
`internal/*/kinds` package, and one that nothing declares is itself a failure.
`TestEnumMarkers_InSchema_AreExactlyTheRegisteredCatalogues` is the Go twin: the marker pairs in the
schema are exactly the pairs `internal/ledger/enumgen`'s `catalogues()` renders, so a marker const
declared in Go but wired into no generator fails too. The waiver is a `// dkp:enum-literal <reason>`
comment on the line above the check — with a reason; a bare marker fails — and it belongs in the
schema rather than in an allowlist inside the script, so the exception appears in the diff a reviewer
reads. `TestRepoGates_HandWrittenEnumCheck_FailsGate` in `test/repo/` is its negative fixture.

A **nullable** column's CHECK is rendered by
`schemaenum.NullableCheckExpr` (`x IS NULL OR x IN (…)`), not by wrapping the plain form at the call
site — `account.system_key` is the worked example, and the prefix is load-bearing rather than
decorative: a bare `IN` list is NULL, not true, for a NULL column.

A catalogued value can still reach a database that has no row for it. `account.system_key` is the
case: the four system accounts are **seed rows** in `000003_ledger.sql`, and a fifth key added to the
catalogue and to the CHECK resolves to `store.ErrNotFound` on a fresh install until it is seeded and
paired with a deterministic id in `internal/ledger.SystemAccountIDs`.
`TestSystemAccountIDs_CoverTheCatalogue` and `TestAccountKinds_SeededSystemAccounts_MatchTheCatalogue`
are what fail in the meantime.

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
  The gate itself is `internal/migrate/shippedlock` (`verify` / `verify --complete` / `seal` /
  `init`); `repo-gates.sh` and both Makefile targets run that one command, and a missing `go` is a
  MIG003 **failure**, never a skip. A malformed row is likewise a failure: a manifest that parsed to
  nothing would otherwise report "0 shipped migrations unchanged" and pass.
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
proves that the migration still succeeds at the database level, `integrity_check` and
`foreign_key_check` still pass, and the ledger is silently editable afterwards. **That is what
"silently" means here** — write the rebuild so the first test is the one that describes your
migration. (The boot path now refuses that migration — see "Boot behaviour" below — but the database
is exactly as happy with it as it ever was, which is why the fixture stays.)

The fixtures come in **matched pairs**: a correct migration and a deliberately broken twin that
differs from it in exactly one declared way, registered in `test/repo/fixture_pairs_test.go` and held
to that difference statement by statement. Edit one and you edit both, in the same commit. A twin
carries `-- dkp:fixture deliberately-broken` in its header, which means what it says: never repair
it. Repairing the control leaves the positive test asserting something nobody has ever watched fail.

### Rebuilding a table that HAS CHILDREN — Atlas's generated form cannot work

**`ledger_batch` is the case this exists for, and Atlas's output fails on every populated database.**

Atlas wraps every rebuild in a pragma pair:

```sql
PRAGMA foreign_keys = off;
-- create new_x, copy, DROP TABLE x, rename, re-create indexes
PRAGMA foreign_keys = on;
```

**Both pragmas are silent no-ops as generated.** goose runs each migration inside a transaction and
SQLite ignores `PRAGMA foreign_keys` inside one — no error, no warning. So `DROP TABLE ledger_batch`
runs with foreign keys *enforced*, `ledger_entry` still references it, and the migration raises
`FOREIGN KEY constraint failed (787)`. It passes every fresh-install check on the way there, because
an empty `ledger_batch` has no children to violate anything. That is the "works on a fresh install,
breaks on upgrade" class, aimed at the highest-value table in the product.

`PRAGMA defer_foreign_keys = ON` does **not** rescue it. Deferred enforcement counts violating
operations; it does not re-scan at COMMIT. Dropping the parent and re-creating it with identical rows
never decrements the counter.

**The pattern: take goose's transaction away, so the pragma means something.**

```sql
-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = off;
-- … Atlas's 12 steps, unchanged …
PRAGMA foreign_keys = on;
-- then the trigger re-creation, case 1
```

Copy `test/fixtures/migrations/rebuild/000002_ledger_batch_rebuild.sql`. It is the worked example,
and `TestMigrate_FullStack_LedgerBatchRebuildSurvivesPopulatedUpgrade` applies it to a populated
ledger on every CI run, so the pattern is verified rather than remembered. Its twin,
`…_in_transaction.sql`, is Atlas's form unmodified and is asserted to *fail* — if that test ever goes
green, this section is obsolete and both fixtures need rewriting.

**What NO TRANSACTION costs, and why it is affordable here.** It trades per-migration atomicity: a
migration that dies halfway leaves the statements before that point applied. The recovery story does
not depend on that transaction — the boot path takes a `VACUUM INTO` snapshot *before* applying
anything and restores it on any failure, so the officer's rollback is the snapshot, not the rollback
log. That is the whole argument, and it is why the boot path's snapshot must never become optional.

Consequences to write into your migration anyway:

- **Only where it is needed.** A rebuild of a table nothing references — `ledger_entry`, most tables
  — works inside the transaction and keeps its atomicity. Do not paste the annotation everywhere.
- **Leave the pragma as you found it.** Turn it back `on` at the end. It is connection state on the
  write pool's single connection, not a per-statement flag, so a migration that forgets would hand
  the next migration in the same boot a connection with no referential integrity. The boot path
  re-asserts `foreign_keys = ON` after every migration as a backstop
  (`store.RestoreForeignKeyEnforcement`), and `TestRestoreForeignKeyEnforcement_AfterANoTransactionMigration`
  proves the leak is real — but write the `on` anyway. The backstop is not the contract.
- **Do not write the annotation out in prose.** goose treats *any* comment line carrying its
  annotation marker as a directive, so a header comment quoting the directive makes the migration
  fail to parse. Say "the NO TRANSACTION annotation" and let the real one be the only one.
- The migration still needs its trigger re-creation (case 1) and its index re-creation.

## What the populated-upgrade gate compares, and why it is asymmetric

`TestMigrate_FullStack_LedgerDataSurvivesUpgrade` walks a populated ledger through every migration.
It does **not** hold every table to the same standard, and the asymmetry is a decision:

| Table | Rows | Columns | Values |
|---|---|---|---|
| `ledger_batch`, `ledger_entry` | exact set | no column may be dropped | **every column of every row, byte for byte** |
| `pool`, `account` | exact set | no column may be dropped | **not compared** |

**Values are strict for the ledger** because nothing may ever rewrite it. A migration that changed a
committed `amount_cp` is a defect with no benign reading, and the correction for bad ledger data is a
reversal batch at runtime — never a migration.

**Values are not compared for `pool` and `account`** because a backfill on a mutable table is
sanctioned work: case 4 above names populating `name_norm` for existing rows as *the* worked example,
and that is literally a `pool` row being rewritten by a migration. Strict comparison there would fail
the migration this document tells you to write, and the fix would be to loosen the test — the wrong
direction of travel, and the direction that ends with somebody in a hurry loosening the ledger's
comparison too. A test that must be edited to land correct work stops being believed by the third
time.

**Columns are strict everywhere**, including the mutable tables, because dropping a column is not a
backfill. It destroys data an officer's database already holds, it is destructive under "Stop and ask
if" below, and no legitimate backfill removes one — so that half cannot fire on correct work.

If you are writing a backfill on `pool` or `account`, you do not need to touch that test. If you find
yourself wanting to relax what it says about `ledger_batch` or `ledger_entry`, stop: that is the
invariant, not the test being awkward.

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
   then apply per-migration, and after each one: restore `foreign_keys = ON` on the write
   connection, then `PRAGMA integrity_check`, `PRAGMA foreign_key_check`, and **the ledger's tables
   and append-only triggers are all still there**.
3. Any failure → **automatically restore the snapshot**, exit 1, name the failing migration.

The third check is `internal/store.AppendOnlyState`, read against a hard-coded catalogue in that
package. It is there because the other two have no opinion about a trigger: a migration that rebuilds
a ledger table and forgets the re-creation passes both, loses no row, and hands back a database whose
history is editable. It compares **two** things, and the second is not decoration — a trigger on a
table that does not exist is vacuously present, which is what lets a fresh install run migration
000001, and would otherwise be a way straight through: a migration that `DROP`s `ledger_entry` takes
every row and both triggers with it, dangles no foreign key, corrupts no page, and leaves nothing
"missing" to report. Three properties are worth knowing before you write a migration:

- **A ledger table that disappears across a migration is refused.** A 12-step rebuild is unaffected:
  it drops and re-creates the table under the same name inside one file, and only the state before
  and after the whole file is compared.

- **It compares against the state before each migration, not against the catalogue.** What is refused
  is a migration that *lost* a trigger that was present when it started. A database that arrived
  already missing one — from a fork's build, a past upgrade, a support session with a SQLite client —
  is logged loudly at boot and still upgrades. Failing there would close that officer's upgrade path
  permanently over damage no version of the binary can undo.
- **The boot path never re-creates a trigger it did not drop.** A database whose history was
  rewritable for an unknown period is a support conversation, not something to paper over.

Adding a ledger trigger means adding it to that catalogue in the same change;
`TestAppendOnlyTriggers_Catalogue_MatchesAFreshInstall` fails if you do not.

A test seeds a DB, injects a migration that fails halfway, and asserts the file is byte-identical to
the pre-migration snapshot. Write your migration so that path works: one logical change per file,
integrity-check-clean, and reversible by restore rather than by a `Down` section.

## Stop and ask if

- The change is destructive (drops a column, drops a table, narrows a type). It needs the
  `!destructive-migration` label and a human.
- You are tempted to hand-edit for a fifth reason.
- A new column would hold a queryable fact inside a `*_json` blob.
