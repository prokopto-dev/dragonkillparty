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
1. Read the schema version recorded in the database.
2. Database newer than this binary?
     → REFUSE TO START, naming the image tag you need and the snapshot path.
3. Migrations pending, and DKP_AUTO_MIGRATE is on?
     a. VACUUM INTO /data/backups/pre-<version>-<timestamp>.db, compress it
     b. Apply each migration in order, running PRAGMA integrity_check after each
     c. On any failure: RESTORE the snapshot, exit non-zero, and print the failing
        migration and the recovery command
4. Record the applied version in the audit log — actor "boot", binary version, duration.
5. Serve.
```

Step 2 matters as much as step 3. Rolling back to an older image after a successful migration is
refused rather than attempted, because an old binary writing to a new schema corrupts data quietly.
The refusal names the tag to use.

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

### If `/readyz` reports `ledger_append_only` as `degraded`

```json
{"check":"ledger_append_only","state":"degraded",
 "detail":"missing append-only triggers: trg_ledger_entry_no_update. Ledger history can be rewritten…"}
```

The database-level guarantee that ledger history cannot be edited is **not** in place on this
database: one of the four triggers that make an `UPDATE` or `DELETE` on `ledger_batch` /
`ledger_entry` fail is missing. `/readyz` answers `503` for as long as that is true, on every probe,
so a load balancer takes the instance out of rotation and your monitoring keeps saying so.
`/healthz` stays green, so nothing kills the container over it.

This binary did not cause it — a migration that drops one of those triggers is refused and rolled
back — so it arrived from somewhere else: a fork's build, a patched image, or a session with a SQLite
client against the live file. The upgrade path deliberately still works, and nothing re-creates the
triggers silently, because a ledger whose history was editable for an unknown period is a
conversation and not something to paper over. **Please open an issue**, and keep the current backups:
restoring a snapshot from before the damage is the only action that restores the guarantee *and* what
it was protecting.

**If you see the verdict but no `detail`, ask the process directly rather than through your reverse
proxy** — `curl -s localhost:8080/readyz` on the box, or `docker exec <container> ...`. The names of
the missing triggers go only to a caller that is both on the local network *and* not being relayed by
a proxy, because a proxy on the same host makes every caller on the internet look local. The trigger
names are also in the boot log at error level, every start.

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
