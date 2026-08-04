# Dragon Kill Party

DKP and guild management for [Project 1999](https://www.project1999.com/) EverQuest raiding guilds.
One Go binary, one SQLite file, an embedded web UI, and a public API that external tools — log
parsers, Discord bots, spreadsheets — can drive as first-class citizens.

> **Status: pre-1.0, design phase. Do not run your guild on this yet.**
> There is no working software in this repository. What exists is the design, the roadmap, and the
> contract that implementation follows. For what is *implemented*, run `make status` — it is derived
> from the Makefile itself, so it cannot drift. For what is *planned*, read [ROADMAP.md](ROADMAP.md)
> and [the first ten PRs](docs/development/first-ten-prs.md).

## Why not EQdkp Plus

EQdkp Plus is the incumbent, and this project owes it the debt every successor owes. But:

| | EQdkp Plus | Dragon Kill Party |
|---|---|---|
| Latest release | 2.3.39, May 2021; repo last pushed Nov 2023 | Active |
| Runtime | PHP 7.1 + MySQL, no official container | One static binary, or one container, or `dkp.exe` |
| Points | A mutable total recomputed from cache | An append-only ledger; a balance is a `SUM` you can audit |
| Corrections | Edit history | Reversal batches — the original stays visible |
| API | HTTP 200 on errors, no pagination, no `DELETE`/`PUT`, no member CRUD | REST + OpenAPI 3.1, RFC 9457 errors, cursors, idempotency, webhooks, SSE |
| API auth | One `api_key` that impersonates the first superadmin | Scoped tokens on service accounts; **no all-powerful token exists** |
| Auctions | None — every guild's bot keeps its own totals | Server-authoritative bid sessions with holds and anti-snipe |
| Upgrades | Upload a zip over FTP | `docker pull` + restart, with snapshot-before-migrate and automatic rollback |

Dragon Kill Party is a **clean-room reimplementation**. EQdkp Plus is AGPL-3.0 and its game modules
are CC BY-NC-SA (non-commercial); no code, schema text, language strings, or assets are copied. The
importer reads your database — it does not transcribe their PHP. See
[ADR-0010](docs/adr/0010-agpl-clean-room-firewall.md).

## Quickstart

```bash
docker run -d --name dkp -p 8080:8080 -v dkp-data:/data ghcr.io/dragonkillparty/dkp:1
```

Open `http://localhost:8080`. The first run prints a one-time setup URL — there is no default
password. That is the whole installation: no database to provision, no reverse proxy required, no
cron entry, no Redis.

No Docker? Download the binary for your platform, double-click it, and open the URL it prints. It
creates its own data directory and migrates itself. A lot of P99 officers run everything on a home
Windows raid PC, and that is a supported deployment.

See [getting started](docs/getting-started/first-run.md).

## What it does

**DKP core** — multiple point pools, each with its own strategy and configuration. Ships
`fixed_price`, `tick`, `zero_sum`, `attendance_weighted`, `decay_percent`, `decay_window`, `cap`,
`start_points`, `loot_council`, `roll`, `relative_bid`, `auction_open`, and `auction_sealed` (first
and second price). `epgp` and `suicide_kings` (3 variants) are designed but built only on a named
pilot guild's request — see the roadmap. Points are integer centipoints —
never floats — and zero-sum splits use largest-remainder allocation, so credits sum to exactly the
debit.

**Raid operations** — raid sessions with attendance ticks, weighted/standby/bench attendance,
connected-raid deduplication, and 30/60/90/lifetime attendance windows. Drop a folder of
`RaidRoster-*.txt` dumps in and get your attendance back.

**Loot** — item catalogue with aliases and fuzzy name resolution, multi-buyer splits, rot handling,
and a reconciliation queue so an unrecognised item name is *quarantined and reviewed*, never
silently dropped.

**Auctions** — server-authoritative bid sessions. The platform owns the balance and the rules;
your Discord bot becomes a dumb terminal instead of a second source of truth. Holds prevent
double-spend, anti-snipe extends the timer, sealed bids stay sealed, and the tie-break chain is
recorded on the resolution so you can explain it in chat.

**The statement view** — every member can read their own DKP as a bank statement: date, reason,
delta, running balance, who did it, and a link to the raid dump behind it. This single screen
settles most loot arguments before they start.

**Guild portal** — news and articles, comments, categories, media library, calendar and raid
signups, recruitment applications, guild bank, item priority lists, shoutbox, portal blocks, and a
team page. Full parity with what EQdkp Plus offered. *(Phase 7 — see the roadmap.)*

**Migration** — import an existing EQdkp Plus install from an ACP backup zip, a `mysqldump`, or a
live read-only database connection. Dry run is the default. You get a reconciliation report that
proves what landed and names everything that did not, and an undo button.

**The API** — everything the web UI can do, a bot can do, with the same published operations. This
is enforced in CI, not promised: a test replays the web UI's exact requests using a scoped API
token and fails the build if any capability is browser-only.

## Documentation

| | |
|---|---|
| [Getting started](docs/getting-started/first-run.md) | Install and first run |
| [Officer guides](docs/guides/) | Running a raid night, choosing a DKP system, attendance, loot |
| [Migrating from EQdkp Plus](docs/migration/from-eqdkp.md) | Including [what does *not* migrate](docs/migration/what-does-not-migrate.md) |
| [API](docs/api/getting-started.md) | Auth, idempotency, pagination, realtime, webhooks |
| [Operations](docs/operations/upgrade-and-backup.md) | Upgrades, backup and restore, troubleshooting |
| [Design docs](docs/design/) | Architecture, domain model, security, testing |
| [Decisions](docs/adr/) | Why things are the way they are, including the downsides |

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md). Contributions are under the
[DCO](https://developercertificate.org/) — sign off with `git commit -s`. There is no CLA.

Much of this codebase is written by AI agents under human review, which is why the repository is
unusually explicit about invariants and unusually aggressive about mechanical enforcement. Every
rule that matters has a test, a lint rule, a CI gate, or a database trigger behind it.

## Licence

Code is [Apache-2.0](LICENSE). Documentation is CC BY 4.0. The **name and logo are not licensed** —
forks must rename. See [TRADEMARK.md](TRADEMARK.md).

Not affiliated with, endorsed by, or connected to Daybreak Game Company, Darkpaw Games, or Project
1999. EverQuest is a trademark of Daybreak Game Company LLC. No game assets are bundled.
