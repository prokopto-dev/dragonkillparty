# ADR-0029 — The idempotency record is scoped to `(principal, key)`, and both write-safety checks are middleware

**Status:** accepted · **Date:** 2026-08-23 · **Deciders:** owner

## Context and problem statement

Canonical §7 requires `Idempotency-Key` on every POST that creates domain state and `If-Match` on
every transition. Both were **declared and unenforced**: `arch_test.go` asserts that an operation
carries the parameter, `internal/guild` checks its own If-Match by hand, and the three idempotency
error codes in the closed enum had no emitter anywhere. A gate that reads a declaration cannot notice
a handler that never acts on it.

Three documents also specify the record three different ways. Canonical §7 says uniqueness is
`(principal_id, key)`; `docs/api/idempotency-and-concurrency.md` sketches a `scope_key` primary key
of `sha256(principal ‖ method ‖ path_template ‖ key)`; `01-domain-model.md` §15 calls the table
`idempotency_key` and spells the terminal state `completed`. They are not stylistic variants — the
first two disagree about whether one key reused on two routes is an error or two records.

## Considered options

| Option | For | Against |
|---|---|---|
| A — Route-scoped key (`scope_key` over principal + method + path template) | A client can safely reuse one key per route; fewer spurious collisions | A key-generation bug — one key per bot, or per session — silently opens a record per route and never surfaces. The failure is invisible for months, which is the class this whole mechanism exists to make loud |
| B — `(principal_ref, key)`, canonical §7's | AGENTS.md makes canonical normative, so no tie to break. A key reused across routes collides and is refused, loudly, on the first attempt | A client that deliberately reuses a key across routes gets a 422 it did not expect. That client has a bug |
| C — Enforce in handlers, keep the gates as declaration checks | No middleware; each handler decides | It is the state this record replaces. Forgetting is silent, and the thing forgotten is a double-charged bid |
| D — Enforce in middleware, keyed off a path list | Independent of what an operation declares | A second routing table, disagreeing with the registry the first time somebody adds a route — and disagreeing in the direction that skips the check |

## Decision outcome

**Chosen: B, with the enforcement in middleware keyed off the operation's own declaration (not D's
path list).** `idempotency_record` carries `UNIQUE (principal_ref, key)`; `principal_ref` is
`user:<ulid>` or `service_account:<ulid>` and never a token, so a rotation mid-retry replays.
`internal/api/precondition.go` and `internal/api/idempotency.go` read the declared parameters through
one function that the arch gates now call too — a gate reading a declaration differently from the
code enforcing it is the vacuity this closes.

Four consequences of that shape are decisions in their own right:

1. **The request hash covers the RESOLVED path, not only the template.** §7's wording ("method +
   path template + body") makes two ticks on two different raids indistinguishable when their bodies
   match, so one reused key would replay the first and the second raid would receive nothing. A
   missed tick is a duplicated tick with the sign flipped, and the quieter of the two.
2. **The body is hashed as bytes, not as canonical JSON**, which `docs/api` claimed. A retry sends
   the same bytes; canonicalisation would buy nothing for the case this serves while costing a parse
   on every fenced request and a definition of "canonical" for bodies that are not JSON.
3. **Order is authorization → precondition → idempotency → handler.** A 428 must not consume a key,
   or the corrected retry replays the error it corrected for 24 hours; a 403 must not reach a record,
   or a refusal outlives the reason for it.
4. **Replayed headers are an allowlist** — `Content-Type`, `ETag`, `Location`. Reproducing the whole
   header set would reproduce `Set-Cookie`.

There is no `failed` state: a 5xx or a panic DELETES the record, because absence already means
"retry me" and a third state would keep a row that only ever says "pretend I am not here".

## Consequences

**Good.** The three idempotency codes have emitters and negative tests. A new fenced route needs one
struct tag, not a remembered handler check. `docs/api/idempotency-and-concurrency.md` and
`01-domain-model.md` §15 now describe the table that shipped. `core.ETagBuilder` gives the second
mutable resource the same strong-tag computation as the first.

**Bad.** The claim, the handler and the completion are three transactions, not one — `docs/api`'s
"in the same transaction as the mutation" was never reachable from middleware, and the residue is
that a completion write can fail after its mutation committed, which is an ERROR log and a retry
that re-executes. A response larger than 1 MiB cannot be replayed and releases its claim. Under
concurrency the losers get 409 rather than a replay, so a bot that ignores `Retry-After` sees more
errors than a serialising one. And the single-writer pool is what makes the read-then-claim atomic,
so the Postgres port needs `INSERT … ON CONFLICT` with a returning discriminator instead — a real
divergence, recorded in `db/RECIPES.md`.

**Reversal cost.** Low for the middleware: both are one file each and one line each in `server.go`,
and removing either leaves the declarations and the arch gates exactly as they were. High for the
uniqueness choice once a guild is live — widening `(principal_ref, key)` to include the route is a
twelve-step SQLite rebuild of a table that is being written on every mutation, and it silently
changes the meaning of every key in flight.
