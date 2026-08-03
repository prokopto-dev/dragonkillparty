# First run

**Status:** the wizard is specified, not built. It lands in Phase 2 (accounts, bootstrap) and Phase 3
(the remaining steps). This page is reference material for what each step will do and what it commits
you to — there are no screenshots because there is nothing to screenshot yet.

You have an instance running from [Docker](install-docker.md) or a [binary](install-binary.md). This
page takes you from a blank instance to a guild that can hold a raid.

## Get the bootstrap token

There is no default password. On first boot with zero users, the instance generates a 32-byte
bootstrap token, prints it to standard output, and writes it to `<data-dir>/first-run-token.txt` with
mode `0600`.

```bash
docker logs dkp          # container
journalctl -u dkp        # systemd
```

Until setup completes, **every route except `/setup`, `/healthz`, `/readyz` and the static assets
returns `503 setup_required`**. There is no half-open state in which an unconfigured instance exposes
an API.

Open `/setup`, and paste the token into the form. It is never accepted as a query parameter — query
strings end up in proxy logs, browser history and `Referer` headers.

The token is single-use. If you lose it before finishing setup, restart the instance and read the new
one.

## The six steps

`/setup` is server-rendered, not part of the single-page app, because it has to work before any
bundle, session or token exists.

### 1. Create your admin account

A local account with an argon2id password. You can add Discord sign-in afterwards.

Give every officer their own account. A shared "officer" login destroys the audit trail, which is the
only thing that settles a DKP argument three months later.

### 2. Name your guild

Guild name, server (Blue, Green, Red or other), era, and the guild's timezone.

The timezone is not cosmetic. Day buckets — attendance windows, decay periods, the "raid day" a tick
belongs to — are computed in guild-local time, while every timestamp is stored in UTC. A guild that
raids until 01:00 and sets the wrong timezone will find raids split across two attendance days.

All of these can be changed later.

### 3. Start fresh, or import from EQdkp Plus

Importing **reads** your old database and never writes to it. The default is a dry run: you see a full
report — what matched, what did not, per-member balance deltas against the old site — before anything
is saved. Writing requires an explicit commit, and commit is a session + step-up operation with no
token scope that can perform it.

If you are migrating, stop here and read [Migrating from EQdkp Plus](../migration/from-eqdkp.md) and
[what does not migrate](../migration/what-does-not-migrate.md) first. Passwords are never migrated;
members claim their accounts with one-time codes.

### 4. Choose how points work

You are creating your first **pool**: a named currency with one strategy and its configuration. Three
presets, each of which is just a starting configuration you can edit:

| Preset | Strategy stack | Suits |
|---|---|---|
| Time and kills | `tick` + `decay_window` | Most P99 guilds. Points per attendance tick, bonus per named, earnings older than the window stop counting. |
| Zero-sum | `tick` + `zero_sum` | Guilds that want a closed economy: what a winner spends is redistributed to everyone else on the raid. |
| Attendance-first | `tick` + `attendance_weighted` | Guilds who want standing driven by turning up rather than by hoarding. |

You can add more pools at any time, and a raid can feed several. Changing an existing pool's rules
later is allowed and is recorded as a migration event — **it does not rewrite past raids.**

If you are not sure, read [Choosing a DKP system](../guides/choosing-a-dkp-system.md). It is the page
worth twenty minutes before your first raid and worth a guild split after it.

### 5. Backups

On by default, nightly, kept in the data volume: 14 daily and 8 weekly snapshots. One checkbox adds an
off-box copy to an S3-compatible target.

Backups on the same disk protect you from mistakes, not from disk failure. Set up the off-box copy on
day one — see [Upgrade and backup](../operations/upgrade-and-backup.md).

### 6. Done

The last screen prints your site URL, the admin URL, a **Mint an API token** button, and — if you
imported characters — the list of one-time claim codes, with a copy-as-Discord-message button.

## What to do next, in order

| Order | Do | Where |
|---|---|---|
| 1 | Distribute claim codes so members link their characters | [Roster, mains and alts](../guides/roster-and-alts.md) |
| 2 | Set your mains and alts policy before the first raid, not after | [Roster, mains and alts](../guides/roster-and-alts.md) |
| 3 | Create the event types you raid — NToV, Kael, Sky, Fear | [Running a raid night](../guides/running-a-raid-night.md) |
| 4 | Assign officer roles; nobody needs to be owner | [Permissions for officers](../guides/permissions-for-officers.md) |
| 5 | Do a dry run with last week's dumps before you trust it live | [Running a raid night](../guides/running-a-raid-night.md) |
| 6 | Mint a token for your Discord bot | [API getting started](../api/getting-started.md) |

## Checks worth running once

```bash
dkp doctor
```

`dkp doctor` checks database reachability, disk space, WAL size, clock skew, forwarded-header sanity,
SSE delivery through your actual reverse proxy, TLS expiry, the Discord OAuth redirect URI against
`DKP_BASE_URL`, SMTP reachability, backup freshness, pending migrations, and the last ledger
verification result. Each failure prints the fix, not the error. The same output renders at `/ops`.

## Next

- [Configuration](../operations/configuration.md) — every `DKP_*` variable
- [The ledger](../concepts/ledger.md) — what your members will ask about first
- [Troubleshooting](../operations/troubleshooting.md)
