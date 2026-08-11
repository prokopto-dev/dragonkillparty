# Upgrade and backup

**Status:** migrate-on-boot with snapshot and auto-restore lands in Phase 0; scheduled backups and
`dkp restore` in Phase 9. This page is the runbook the implementation must make true.

Read this once before your first upgrade. It is the page that stands between a volunteer officer and
ten years of guild history.

## The short version

```bash
docker pull ghcr.io/dragonkillparty/dkp:1
docker restart dkp
```

That is the whole upgrade. The instance snapshots the database before it applies any migration and
restores that snapshot automatically if a migration fails.

Practise a restore once, on a copy, before you need one. A backup you have never restored is a
hypothesis.

## What happens at boot

```
1. Finish an interrupted restore, if a previous boot was killed part-way through one.
2. Read the schema version recorded in the database.
3. Database newer than this binary?
     → REFUSE TO START, naming the image tag you need and the snapshot path.
4. Are the ledger's append-only triggers all present?
     → If not: LOG IT LOUDLY and carry on. This boot did not cause it, so there is
       nothing to restore — see "Your ledger's triggers are missing" below.
5. Migrations pending, and DKP_AUTO_MIGRATE is on?
     a. VACUUM INTO /data/backups/pre-<version>-<timestamp>.db, compress it
     b. Record which ledger tables and triggers exist right now. This is the baseline
        check (iv) compares against.
     c. Apply ONE migration, then run all four checks on the result, in this order:
          i.   put PRAGMA foreign_keys back ON — a NO TRANSACTION migration can
               leave it off, and that would silently disarm every later migration
               in this same boot
          ii.  PRAGMA integrity_check
          iii. PRAGMA foreign_key_check — integrity_check does not validate foreign
               keys, so a rebuild that copied rows in the wrong order passes it
          iv.  append-only survival — no ledger table and no ledger trigger that was
               present before this migration may be absent after it
     d. Repeat (c) until nothing is pending.
     e. On any failure at any point in (c): RESTORE the snapshot, exit non-zero, and
        print the failing migration and the recovery command.
6. Record the applied version in the audit log — actor "boot", binary version, duration.
7. Serve.
```

Step 3 matters as much as step 5. Rolling back to an older image after a successful migration is
refused rather than attempted, because an old binary writing to a new schema corrupts data quietly.
The refusal names the tag to use.

One migration at a time, checked after each, is the reason a failure can name a *file*. A bulk apply
would discover the damage after the migrations that followed it had also run, and the file name is
the entire actionable content of the message you get.

### The four checks, and what each one is for

| Check | Catches |
|---|---|
| `PRAGMA foreign_keys = ON` restored | A migration that turned foreign keys off to rebuild a table and forgot to turn them back on. Nothing reports a pragma, so without this every *later* migration in the same boot would apply with no referential integrity, silently. |
| `PRAGMA integrity_check` | A corrupt database — torn pages, a broken index, a malformed record. |
| `PRAGMA foreign_key_check` | Dangling references that `integrity_check` calls healthy. This is the normal outcome of a table rebuild that copied rows in the wrong order. |
| Append-only survival | A migration that rebuilt a ledger table and did not re-create its `BEFORE UPDATE OR DELETE` triggers, or that dropped a ledger table outright. Both pass the three checks above, lose no page and dangle no key, and hand back a ledger whose history can be rewritten. |

The append-only check compares against the state **before that migration**, not against a full
catalogue of what should exist. That is deliberate: a database that arrived already missing a
trigger would otherwise fail on the first migration it was ever offered, and your upgrade path would
be closed for good by damage that predates the binary. What is refused is a migration that *lost*
something that was there when it started.

### What it looks like when it goes wrong

Every row below is a message the process prints before it exits non-zero. In every case marked
"restored", your data is intact — the snapshot from step 5(a) is already back in place and the
process checked that it reads correctly before putting it there.

| It says | What happened | What you do |
|---|---|---|
| `database schema is newer than this binary` | You rolled back to an older image after upgrading. Nothing was changed. | Run the image tag it names. |
| `migration <file> failed: …` followed by "Your database was restored automatically" | A migration failed, or one of the four checks failed after it. | Do not retry this version. Report the migration it names, with a support bundle. |
| `a migration dropped an append-only ledger trigger` | Check (iv). The migration applied cleanly and lost no data — it removed a protection, so the upgrade was refused. **Your ledger is fine.** | Report it. This is a bug in the migration, not in your database. |
| `a migration dropped a ledger table` | Check (iv), the louder half: the rows went with the table. Restored. | Report it. Same as above, and quote the whole message. |
| `THE AUTOMATIC RESTORE ALSO FAILED` | A migration failed *and* the snapshot could not be put back. This is the one case where you have to act. | **Do not start this version again.** Restore by hand with the `zstd -d` command it prints, then report it. |
| `read the ledger's append-only state before migrating` | The database could not be inspected before anything was applied. Nothing ran; there is nothing to restore. | Usually a permissions or disk problem on the data directory. Check both, then report it. |
| `a previous upgrade was interrupted while restoring its snapshot` | The process was killed mid-restore and could not finish the job on this boot either. | Restore by hand from `<data-dir>/backups/` and report it. |

### Your ledger's triggers are missing

Step 4 is the one message that does **not** stop the boot:

> the ledger's append-only triggers are not all present on this database

It is logged at error level and the site starts anyway. That combination is deliberate, and it means
something specific: **this boot did not cause it.** The damage arrived with the database — from an
older upgrade, a fork's build, or a support session with a SQLite client — so there is no snapshot to
go back to and no migration to name. Refusing to start would lock your guild out of their site over
damage that is already done, at whatever hour you happened to restart.

What it costs you until it is fixed is the guarantee, not the data: ledger history can be rewritten
by anything with direct access to the file, because the triggers that refuse an `UPDATE` or `DELETE`
are not there to refuse it. The balances are still correct and `dkp verify-ledger` still checks them.

**`/readyz` says so too, on every probe, for as long as it is true** — so this is not a message you can
miss by not watching a restart:

```json
{"check":"ledger_append_only","state":"degraded",
 "detail":"missing append-only triggers: trg_ledger_entry_no_update. Ledger history can be rewritten…"}
```

It answers `503`, so a load balancer takes the instance out of rotation and your monitoring keeps
firing. `/healthz` stays green throughout, so nothing kills the container over it — that is the same
split as everywhere else on this page, and here it matters twice: losing the raid night on top of
losing the guarantee helps nobody.

If you see the verdict but no `detail`, that is the default: the trigger names are withheld from
everyone until you set `DKP_READYZ_DETAIL`, because a proxy on the same host makes every caller on the
internet look local and the process cannot tell them apart. Set `always` if `/readyz` is reachable only
from your own network, or `local` if the binary is published directly with no proxy — and with `local`,
**ask the process directly rather than through your reverse proxy**: `curl -s localhost:8080/readyz` on
the box, or `docker exec <container> …`, since any forwarded header on the request withholds it again.
See [Configuration](configuration.md#health-and-readiness). The same names are in the boot log at error
level whatever you set.

Report it with a support bundle and the log line, which names exactly which triggers are missing.
Keep your current backups while you do: restoring a snapshot from before the damage is the only action
that puts back the guarantee *and* what it was protecting. Nothing re-creates the triggers silently,
deliberately — a ledger whose history was editable for an unknown period is a conversation, not
something to paper over.

**No `down` migrations ship, ever.** A down migration is code that runs exactly once, in an emergency,
on data you cannot reproduce, written months earlier by someone who never tested it against your
actual data. Restoring the snapshot taken 200 ms before the migration is always correct and needs no
code. The `Down` blocks in the migration files contain a deliberate abort telling you which snapshot
to restore.

## What CI guarantees before a release ships

| Guarantee | Mechanism |
|---|---|
| A failed migration leaves your database untouched | A test corrupts a migration deliberately, boots, and asserts the process exits non-zero **and** the database is byte-identical to the snapshot |
| No migration silently drops data | CI records row counts and column digests for the ledger, raid, attendance, award, person, character and item tables before and after every migration, and fails on any decrease |
| The append-only triggers survive migrations | A test asserts the triggers fire after the *full* migration set has been applied, not merely after the one that created them. Table rebuilds drop triggers; this is how you find out. |
| A destructive change is deliberate and staged | `DROP TABLE`/`DROP COLUMN`/`DELETE FROM` fails CI without an approval marker and a linked issue confirming the previous minor release stopped writing to that object |
| The upgrade path works from every supported version | Every release publishes a seeded reference database; the next release's CI upgrades from each of them |

## Backups

On by default. Nightly, into `<data-dir>/backups/`, keeping 14 daily and 8 weekly snapshots.

| Kind | When | Where |
|---|---|---|
| Scheduled | Nightly, per `DKP_BACKUP_SCHEDULE` | `<data-dir>/backups/` |
| Pre-migration | Automatically, before any migration | `<data-dir>/backups/pre-<version>-<timestamp>.db.zst` |
| Manual | `dkp backup`, or the admin UI's **Back up now** | `<data-dir>/backups/` |
| Off-box | If `DKP_S3_BACKUP_URL` is set | Your S3-compatible bucket |

### Take one now

```bash
dkp backup
docker exec dkp /dkp backup          # in a container
```

`dkp backup` is safe against a running instance. **Copying `dkp.db` with `cp` is not** — you get a
torn file, and you will not find out until you try to restore it.

### Get it off the box

Backups on the same disk protect you from mistakes, not from disk failure, not from ransomware, and
not from the raid PC being thrown out. Set `DKP_S3_BACKUP_URL`, or copy `<data-dir>/backups/`
somewhere else on a schedule. Downloading a backup through the API is a session plus step-up
operation with no token scope — a backup is the entire guild in one file, so no bot can fetch one.

## Restoring

```bash
dkp restore /data/backups/2026-08-03-0300.db.zst --dry-run
dkp restore /data/backups/2026-08-03-0300.db.zst
dkp restore --from-s3 s3://guild-backups/dkp/2026-08-03-0300.db.zst
```

`--dry-run` reports exactly what would be replaced and does nothing. Run it first, always.

Stop the instance before restoring. Restoring under a running server is refused.

## Verifying the ledger

```bash
dkp verify-ledger
```

Rebuilds every balance from the log, from zero, and compares it against the cached snapshot. This runs
nightly on its own and its last result appears in `dkp doctor` and at `/ops`.

If it ever disagrees, that is a **bug in this software**, not a data-loss event: the log is intact and
the cache is derived. `dkp verify-ledger --rebuild` discards and recomputes the cache. Please report
the drift.

`dkp verify-ledger` checks the balances, which is a different question from whether the database can
still refuse an edit. If `/readyz` reports `{"check":"ledger_append_only","state":"degraded"}`, it is
the second question that has failed: see
[Your ledger's triggers are missing](#your-ledgers-triggers-are-missing) above.

## A restore drill worth doing once

Twenty minutes, on a laptop, before you need it:

```bash
# 1. Take a backup from the live instance
dkp backup

# 2. Restore it into a scratch directory
mkdir /tmp/dkp-drill
dkp restore /data/backups/<file>.db.zst --data-dir /tmp/dkp-drill

# 3. Boot it on a different port and look at it
DKP_DATA_DIR=/tmp/dkp-drill DKP_LISTEN=:8081 dkp serve

# 4. Check three things: a member's balance, a raid's attendance, the last award
```

If step 4 shows what you expect, your backup strategy works. If any step failed, you found out on a
Tuesday afternoon instead of during a raid.

## Version and support policy

| | |
|---|---|
| Versioning | Semver on the binary. `/api/v1` is additive-only; breaking changes mint `/api/v2`. |
| Tags | `:1` tracks every non-breaking release. `:1.4` tracks patches. `:1.4.2` is exact. |
| API support | v1 lives at least 18 months after v2 appears, with `Deprecation` and `Sunset` headers throughout. |
| "Breaking", to a self-hoster | An upgrade that needs a human. Anything else is not breaking, whatever the diff says. |
| Release notes | Generated: the API diff, the new migrations, and any commit marked breaking. |
| Update notification | A daily check against a static file, shown as an admin banner. **The instance never updates itself.** |

## Before an upgrade you are nervous about

| Do | Why |
|---|---|
| Read the release notes for anything marked breaking | They are generated, so they are complete |
| Take a manual backup and copy it off the box | The automatic pre-migration snapshot is on the same disk |
| Upgrade one minor at a time if you are several behind | Every release's CI proves the upgrade from the previous ones, not from every historical version |
| Not upgrade an hour before a raid | The upgrade is safe; your evening is not the moment to discover you were wrong |

## Next

- [Configuration](configuration.md) — `DKP_AUTO_MIGRATE`, backup schedule and retention
- [Troubleshooting](troubleshooting.md) — when the boot fails
- [The ledger](../concepts/ledger.md) — why a cache disagreeing is not a data-loss event
