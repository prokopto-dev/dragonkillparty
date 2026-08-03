# ADR-0008 — Atlas authors migrations, goose applies them

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

The audience upgrades by replacing a binary and restarting, unsupervised, over ten years of DKP data.
Migrations are the only artefact where a mistake is unrecoverable for the user. SQLite makes this
harder than Postgres would: there is no `ALTER COLUMN` and no `DROP CONSTRAINT`, so most non-trivial
changes are a 12-step create-copy-drop-rename table rebuild that is tedious and easy to get subtly
wrong by hand.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Hand-written SQL migrations only (goose) | Total control; one tool; every statement is reviewed | Every table rebuild is hand-written; schema truth lives only in the accumulated history, so drift is invisible until it bites |
| B — Atlas both authors *and* applies, at runtime | One tool, declarative end to end | Requires the Atlas CLI on the user's machine or in the image — which breaks the single-binary promise from [ADR-0001](0001-go-single-binary-and-sqlite.md) |
| C — ORM auto-migrate | Zero authoring effort | Unreviewable, non-deterministic, and capable of dropping a column to satisfy a struct. Disqualified for a ledger product |
| D — Atlas authors in CI, goose applies at runtime | Declarative source of truth, generated table rebuilds, reviewable SQL, and nothing extra to install | Two tools and two mental models; hand-extensions live alongside generated SQL |

## Decision outcome

**Chosen: D.**

- **Source of truth:** `db/schema.hcl` (Atlas community edition).
- **CI generates:** `atlas migrate diff --dev-url "sqlite://file?mode=memory"` → versioned `.sql` files
  in `db/migrations-sqlite/`. Generated migrations are **committed and reviewed** like any other code.
- **Hand-extended only** for what Atlas cannot express: the append-only triggers
  ([ADR-0002](0002-append-only-ledger.md)), partial unique indexes, `CHECK` constraints, and data
  backfills.
- **Runtime applies:** goose v3, embedded via `go:embed`. **Atlas is a development and CI dependency
  only — the user never installs it.**
- Upgrades snapshot before migrating, auto-restore on failure, run `PRAGMA integrity_check` after each
  migration, and refuse a downgrade outright.

**Enforced by:** a CI convergence check (`atlas schema inspect` after applying the migration set,
compared against a committed fingerprint) and a **migration round-trip test** — apply all migrations
to an empty database, and separately to a copy of the previous release's schema fixture, then assert
both fingerprints match. That second case is the "works on fresh install, breaks on upgrade" class,
which is the most damaging bug this audience can experience.

### Consequences

- Good, because the declarative source catches drift: the schema is a file you read, not a history you
  reconstruct.
- Good, because SQLite's 12-step table rebuild is generated rather than hand-written, which removes
  the single most error-prone piece of SQL in the project.
- Good, because the officer installs nothing. One binary still migrates itself.
- Good, because migrations remain plain reviewable SQL — an officer debugging with `sqlite3 dkp.db`
  can read exactly what ran.
- **Bad, because a contributor must learn two tools.** HCL for the schema, SQL for the extensions, and
  a rule about which changes go where. That is a real onboarding cost on a volunteer project.
- **Bad, because the hand-added triggers, partial indexes and `CHECK`s are exactly what a future
  `atlas migrate diff` may fail to preserve.** *Whether Atlas round-trips them cleanly is an
  UNVERIFIED assumption* — `docs/development/verify-before-phase-0.md` **V6** is the experiment, and
  it must resolve before the ledger triggers ship. If it comes out badly, the extensions move to
  hand-written migrations that Atlas is told to ignore.
- **Bad, because generated migrations still need a human reviewer.** This is assistance, not
  automation; an unreviewed generated migration is as dangerous as an unreviewed hand-written one.
- **Bad, because Atlas is a vendor-backed tool with a paid tier.** The community edition covers what
  is used here, and feature-gating or licence changes upstream are a supply risk. Mitigation: the
  *output* is plain SQL committed to the repo, so losing Atlas costs authoring convenience, not the
  ability to migrate.

### Reversal cost

Low, and that is by design. Drop Atlas and keep writing the same `db/migrations-sqlite/*.sql` by hand;
goose and every shipped migration are unaffected. About a week, mostly spent on the first table
rebuild you have to write yourself.
