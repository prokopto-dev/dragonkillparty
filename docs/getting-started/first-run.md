# First run

**Status:** the bootstrap is built; the wizard is not. As of issue #264 an instance with no owner
arms a one-time token on boot, prints it, and writes it to `<data-dir>/first-run-token.txt`, and
`dkp admin create` creates the first `admin.owner` holder from the host. The server-rendered `/setup`
form that redeems the token in a browser, and the `503 setup_required` gate over every other route
while unbootstrapped, land with the web slice (issue #307) — so today the CLI is the way through.
The six steps below are reference material for what each will do and what it commits you to.

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
will return `503 setup_required`**. There is no half-open state in which an unconfigured instance
exposes an API. *(Not built yet — issue #307. Today an unbootstrapped instance answers `401` to
anything needing a credential, because there is no account to hold one.)*

Open `/setup`, and paste the token into the form. It is never accepted as a query parameter — query
strings end up in proxy logs, browser history and `Referer` headers. *(Also #307.)*

The token is single-use, allows **five attempts in total**, and expires **sixty minutes** after the
instance booted. If you lose it, or spend the attempts, restarting the instance arms a new one — or
run it from the host without a restart:

```bash
dkp admin bootstrap --new-token
```

It refuses an instance that already has an owner. A token armed on a live instance would be a
credential for creating a second owner, printed into a log file; adding one is `dkp admin create`,
which needs the same host access and prints nothing.

## Create the owner from the host

The CLI path needs no token at all, and that is not a hole: it needs a shell on the box and the
database file, which is strictly more than the token proves. It is what to use on a headless install,
on a first deploy over ssh, or when the only owner has stopped playing.

```bash
printf '%s' "$PASSWORD" | dkp admin create --username courtney --password-stdin
```

The password is read from standard input and is **never** accepted as a flag — a flag value is in the
process table for every user on the box and in the shell history. It must be 12 to 128 bytes and must
not contain your username, your email local part or the guild name.

`--role` picks the built-in role (`owner` by default; also `admin`, `officer`, `raid_leader`,
`raider`, `member`, `guest`). The first account to hold `owner` ends first-run setup and retires any
armed token.

**The last account holding `admin.owner` cannot afterwards be demoted, disabled or deleted.** The
database refuses it outright, so an instance cannot be locked out of itself by an officer tidying the
roster. Add a second owner before removing the first.

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
| Attendance-first | `attendance_weighted` + `fixed_price` | Guilds who want standing driven by turning up rather than by hoarding: each raid is worth a fixed pot, split by how much of it you were there for. It *replaces* `tick` — a pool has one earn rule. |

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
