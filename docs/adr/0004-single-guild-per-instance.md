# ADR-0004 — One guild per instance

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

The owner's initial choice was **multi-guild-ready**: a `guild_id` column on every row, a
`WHERE guild_id = ?` on every query, and a single instance able to host several guilds later without
a migration. The domain-model review argued the opposite, and the argument was decisive: a *missing*
`WHERE guild_id = ?` is a silent cross-guild data leak. It throws no error, fails no constraint,
returns plausible rows, and **no test catches it by accident** — you only find it when one guild sees
another guild's standings. The owner changed the decision.

## Considered options

| Option | For | Against |
|---|---|---|
| A — `guild_id` on every row (the original choice) | Hosted multi-guild becomes a config flag; one process serves many guilds; cheap to add now, expensive later | The leak is silent and undetectable by accident; every query, index, permission check and generated SDK carries a tenancy dimension forever; it is the bug class this product can least afford |
| B — Schema (or database file) per guild, one process | Isolation is structural; one binary serves many guilds | SQLite connection routing, per-guild migration runs, per-guild job queues; you pay most of A's complexity and get a worse operational story |
| C — Strictly one guild per instance, no `guild_id` anywhere | The bug class does not exist; every query, backup and export is simpler; scope comes from the principal | Hosted multi-guild is a real project later, not a flag; a hoster with 40 guilds runs 40 processes |

## Decision outcome

**Chosen: C.** There is exactly one guild per instance and **no `guild_id` column anywhere**. Do not
add one "for later". Guild-level configuration lives in a singleton `guild` row. Scope comes from the
request principal, not from a column. A hoster who wants several guilds runs several containers with
separate volumes — which is also several backups, several upgrades and several failure domains,
independently.

This supersedes the `AGENTS.md` draft line that required `guild_id` on every row and in every
`WHERE`; the canonical conventions (§9) are the tie-breaker and they say C.

**Enforced by:** the `lint / repo` CI job greps `db/schema.hcl` and `db/queries/` for `guild_id` and
fails the build, and a schema test asserts no column of that name exists after migrations run. The
mechanism matters more here than usual, because the natural instinct of every contributor who has
built a SaaS product is to add the column back.

### Consequences

- Good, because the entire class of cross-tenant leaks is removed rather than mitigated. There is
  nothing to forget.
- Good, because every query is shorter, every index is narrower, and no permission check has a
  tenancy dimension — which measurably reduces the surface an agent can get subtly wrong.
- Good, because backup, restore, export and "give me my data" are one file. `VACUUM INTO` is the
  whole backup story.
- **Bad, because hosted multi-guild is now a real project, not a config flag.** It needs request
  routing, per-guild auth and session scoping, cross-guild identity for a person in two guilds,
  per-guild resource limits and billing, and a shared media story. That is a quarter of work, and
  this ADR is the reason it is not two weeks.
- **Bad, because a hoster running 40 guilds runs 40 processes, 40 database files and 40 upgrade
  runs.** Fine at 5, tedious at 40, and it makes "I'll host it for a few friends' guilds" more work
  than the incumbent's shared install.
- **Bad, because cross-guild features are impossible in-product** — an alliance leaderboard, a shared
  item database, a server-wide raid calendar. Any of those becomes an external service reading
  several instances' APIs.
- **Bad, because retrofitting is not a column add.** Adding tenancy in two years means touching every
  table, every query, every index, every permission check and every generated client, plus a data
  migration that must be right the first time.

### Reversal cost

Weeks to months, and it reintroduces exactly the bug class this decision deleted. Treat a future
multi-guild product as a new service that federates instances, not as a schema change to this one.
