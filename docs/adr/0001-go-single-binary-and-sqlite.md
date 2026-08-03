# ADR-0001 — Go single binary and SQLite

**Status:** accepted · **Date:** 2026-08-03 · **Deciders:** owner

## Context and problem statement

The product is run by volunteer guild officers, not sysadmins, and most of the code will be written
by agents under human review. Three finalists went through a full architecture bake-off — a
TypeScript monorepo (Hono + Drizzle + Postgres), Python (FastAPI + SQLAlchemy + Postgres), and Go
1.26 + SQLite with the React SPA embedded. The bake-off was **genuinely close**, and the deciding
argument rests on an assumption about P99 officers that nobody has checked.

## Considered options

Scored on seven equally weighted axes (self-hostability, API-first, agent-friendliness, testability,
migration tooling, contributor pool, longevity), 1–10 each.

| Option | Raw sum | Self-host | Contributor pool | For | Against |
|---|---|---|---|---|---|
| A — TypeScript | **58** | 7 | 8 | `tsc --noEmit` is a sub-second whole-system oracle across schema, handlers, SDK and UI | No single binary, no `.exe` (conceded as "a genuine product loss"); ~130 MB image; Postgres in the default path; Drizzle 1.0 still beta after 12+ months |
| B — Python | 57 | 6 | 8 | Best OpenAPI and best migration tooling of the three; by far the best importer substrate (`phpserialize`, `ftfy`, SQLAlchemy reflection) | ~200 MB image, no binary ever; concedes outright "if double-click on the raid PC is a requirement, choose Go"; Alembic autogenerate needs a human reviewer |
| C — Go + SQLite | 57 | **10** | **6** | One `docker run`, no database to administer, ~25 MB `FROM scratch` image, native cross-compile, `dkp.exe` from the same pipeline; full-fidelity integration suite in ~25 s with no container | Thinnest contributor pool of the three; two-language repo; no maintained PHP-serialize or mojibake libraries, so ~400 lines are hand-rolled |

## Decision outcome

**Chosen: C.** TypeScript wins a contest scored on unweighted breadth, and would have been the right
answer to a different brief. This brief weights two axes heaviest — **self-hostability for a
non-sysadmin** and **agent loop speed** — and on those two Go is not marginally ahead, it is
categorically ahead. C is the only candidate whose default install has no database to administer, and
A and B both concede their all-in-one images must embed Postgres under s6, which is the largest
data-loss vector in self-hosted software. On the agent loop the deciding property is not which
language models write best (A scored higher there on raw breadth) but **how fast the highest-fidelity
oracle runs**: a real-migrations, real-server, real-database suite in ~25 s with no container, versus
~45 s for A and ~90 s for B.

**SQLite is the only supported runtime engine in 1.0.** The dual-dialect tax was self-priced at 8–12%
of backend effort, permanent and concentrated in migrations and CI, and it would be paid for a
capability with zero current users. The compile-time Postgres target is kept — a hand-written
`Queries` interface plus `var _ Queries = (*pggen.Queries)(nil)` — so the door stays open at
near-zero cost.

> **UNVERIFIED ASSUMPTION — this is what broke the tie.** *A meaningful share of P99 officers want a
> double-clickable `dkp.exe` on a Windows raid PC.* It is an assumption, not a finding. It is
> load-bearing on the stack choice, and through it on SQLite-only, `FROM scratch`, `go:embed` for the
> SPA, the no-container test story and the six-platform release matrix. Verification is one poll in
> the P99 Discord: `docs/development/verify-before-phase-0.md` **V1**, which must resolve before
> Phase 0 exits. If it comes out the other way, re-run the comparison in Phase 0 while it is still
> cheap — Phase 1 makes the ledger expensive to move.

### Consequences

- Good, because the install is one binary, one file, one backup, and the upgrade path is "replace the
  binary" — the failure mode is removed rather than mitigated.
- Good, because integration tests need no Docker, so an agent can write a failing test, fix it and
  re-run inside one tool-call budget; and unused variables and imports are compile errors, which kills
  a large share of copy-paste artefacts before any test runs.
- Good, because three of the four hallucination-prone boundaries are generated and diff-gated (Atlas →
  migrations, sqlc → query types, Huma → OpenAPI → TS client).
- **Bad, because this is the thinnest contributor pool of the three, in a community whose living
  tooling — eqalert, nparse, Castle Steward, bidbot2, froakbot — is Python and JavaScript.** The
  mitigation is structural, not aspirational: the API is the extension point, SDKs are published to
  PyPI and npm, and community extensions live outside this repo in whatever language the author likes.
- **Bad, because the importer's substrate is Go's weakest area here.** No maintained `phpserialize`,
  no `ftfy`, no `SQLAlchemy.reflect()`; roughly 400 lines of byte-level PHP-serialize and mojibake
  handling are hand-rolled with golden tests. Bounded and one-shot, but real.
- **Bad, because it is a two-language repo** (Go + TypeScript) with two lint and test loops — an
  underrated tax on both human review and agent context.
- **Bad, because `riverdriver/riversqlite` is an early preview** with "minimal real world vetting".
  Pinned exactly, wrapped behind a six-method `jobs.Queue` interface, and soak-tested nightly (see
  ROADMAP R6).

### Reversal cost

Total rewrite. This decision is the substrate for every other one in the seed set.
