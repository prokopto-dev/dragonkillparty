# Invariants

**Status:** the invariant engine lands in Phase 1; several enforcement mechanisms land with the
subsystems they guard. `reference/invariants.md` is generated from the `Invariants()` registrations
and supersedes the runtime table below once it exists.

A rule without a mechanism is a wish. Every rule on this page names the thing that enforces it — a
database trigger, a lint rule, a test, or a CI gate — because a rule enforced only by review is a rule
that survives until the first tired Friday.

## Data invariants

| Invariant | Enforced by |
|---|---|
| `ledger_batch` and `ledger_entry` are never updated or deleted | `BEFORE UPDATE OR DELETE … RAISE(ABORT)` triggers, **plus** an integration test asserting the trigger fires after the full migration set has been applied — table rebuilds drop triggers, and this is how you find out |
| A batch may be reversed at most once | A unique index on `reverses_batch_id` |
| Amounts are integers; no floats anywhere in the arithmetic | A `golangci-lint` rule banning float types in `internal/ledger` and `internal/strategy`; the `NoFloat` runtime invariant; a contract test asserting the JSON Schema type is `integer` |
| Every table is `STRICT` | Atlas generates the DDL from `db/schema.hcl`; `PRAGMA integrity_check` then verifies column content types, which is the cheapest guard against `"350.00"` reaching a centipoint column |
| There is no `guild_id` column anywhere | A repository grep gate. A missing `WHERE guild_id = ?` is a silent cross-guild leak that no test catches by accident; removing the column removes the bug class |
| Enum values are identical in the SQL `CHECK`, the JSON and the OpenAPI | One Go catalogue; `make gen` writes all three; a test asserts the copies agree |
| A migration that has shipped in a tagged release is never edited | CI compares migration checksums against the previous release |
| Migrations are forward-only | Every `Down` block contains `RAISE(ABORT, …)`; `lint / repo` fails any migration whose `Down` contains DDL |
| No migration silently drops data | CI records row counts and column digests for the protected tables before and after every migration, and **fails on any decrease** |
| A destructive migration is deliberate | `DROP TABLE`/`DROP COLUMN`/`DELETE FROM` fails CI without a `-- dkp:destructive-approved: #<issue>` marker, and the referenced issue must confirm the previous minor release stopped writing to that object |

## Ledger invariants

Checked on every proposed batch before it commits. The first four cannot be waived by any strategy.

| Invariant | Means |
|---|---|
| `NoFloat` | Every amount is an integer. Always on. |
| `BatchNonEmpty` | A batch with no entries is a bug, not an event |
| `SeqMonotonic` | Per-pool sequence numbers only increase |
| `EntriesReferenceLiveAccounts` | No entries against deleted accounts |
| `SumZero(kind, batch)` | Zero-sum award entries sum to exactly zero. Checked as a column comparison against the precomputed batch total, not as an aggregate. |
| `NonNegative(kind, floor)` | A batch may not **take** an account below the pool's floor. It constrains a deduction, not a balance: a batch that leaves an account below the floor but better off than it started — debt forgiveness, or a split crediting a member a reversal left in debt — is legal, and one that pushes an account already below the floor further down is not. |
| `MonotoneNonDecreasing(kind)` | EPGP gear points never decrease except by reversal |
| `Permutation(sk_position)` | Suicide Kings positions remain a bijection |
| `RatioPreserved(ep, gp, tol)` | An EPGP decay batch scales both kinds identically |
| `Conserved(kind, total)` | The total across all accounts is unchanged |

**Enforced by:** the ledger service validates every proposal against the strategy's declared
invariants *and* the non-waivable set, inside the committing transaction. Property tests
(`testing/quick` with seeded generators — `make test-property`, 200 checks per PR and 20 000
nightly) drive random award, reversal, suicide and decay sequences and assert the invariants hold
throughout; a nightly job replays a 100,000-entry synthetic ledger from zero and asserts every
balance matches the incrementally maintained cache.

## Architectural laws

Four laws, each with a mechanism, restated from [AGENTS.md](../../AGENTS.md).

| Law | Enforced by |
|---|---|
| HTTP routes are declared only in `internal/api` | An architectural test walking the route registry |
| `*sql.DB` is held only by `internal/store` | An import-graph test |
| `internal/strategy` is pure — no store, no `time.Now`, no `math/rand` | Import-graph test plus lint rules; the clock and a seeded generator are injected and the seed is persisted onto the batch |
| `web/src` contains no `fetch` or `XMLHttpRequest` outside `web/src/api` | An ESLint rule plus a CI grep |

Plus the law that makes the API real rather than aspirational: **the single-page app is a pure API
client.** A test replays the web UI's exact requests using a scoped token and fails the build if any
capability turns out to be browser-only.

## API invariants

| Invariant | Enforced by |
|---|---|
| Every operation declares `Security` and `x-dkp-permission` | A spec lint (`vacuum`) plus an architectural test asserting the declared scope set matches what the middleware actually checks |
| Every operation has an explicit `operationId` in `lowerCamelCase`, never renamed | Spec lint for presence; `oasdiff` fails a rename as a breaking change. SDK method names derive from it, so a rename breaks published clients even when the HTTP surface is unchanged. |
| Every POST that creates domain state requires `Idempotency-Key` | An architectural test over the route registry |
| Errors are RFC 9457 with a `code` from a closed enum | Response-validation middleware runs across the whole integration suite, not just the end-to-end pass |
| Never HTTP 200 with an error body | Same middleware |
| `openapi.json` cannot drift from the handlers | CI regenerates it and fails on any diff |
| Every error code has a documentation page | A Go test enumerates the enum against the docs tree; an Astro build check fails on orphan pages |
| No breaking change inside `/api/v1` | `oasdiff` against `main`, requiring an explicit label and a changelog entry |

## Licence invariants

| Invariant | Enforced by |
|---|---|
| No EQdkp Plus code, DDL text, language strings or assets enter this repository | A CI grep for `pdh_`, `gen_class`, `plus_exchange` and `__multidkp2event` outside `internal/importer/legacy_names.go` and `internal/api/compat/` |
| No copyleft or source-available runtime dependency | `internal/licence` (`make licence-gate`), run by the required `security / licences` job and by `make check`. It classifies every module in `go list -deps ./...` — the code that actually ships, not the test-only graph — unioned across the release platforms, and every package in the `web/` graph via `pnpm licenses list --json`, and fails closed (LIC001 denied, LIC002 unidentified or not on the allowlist, LIC003 embedded third-party copyleft) |
| No game data is bundled | Reviewed at release; item names, stats and icons come from the separate, optional `dkp-p99-seed` importer |

Reading a guild's own EQdkp database at runtime creates no derivative work. Transcribing their PHP
does. This distinction matters most when the task is "match EQdkp's behaviour" — that is exactly when
the temptation appears.

`THIRD_PARTY_NOTICES.txt` is **not** generated yet, despite what `NOTICE` implies. It is a release
artifact — it belongs with the SBOM and the cosign attestations in Phase 0 PR 7, which is where the
release train that would attach it lands. The licence gate above is what keeps the dependency graph
clean in the meantime; the notices file records it, and recording a graph nothing publishes yet
would be ceremony.

## Test-integrity invariants

The ones that exist because the fastest route to a green build is to change the test.

| Invariant | Enforced by |
|---|---|
| Golden files are not rewritten to go green | `test/golden/` and `test/fixtures/` are CODEOWNERS-protected; `-update` is refused when `CI=true`; a test asserts the fixture count is non-decreasing |
| Tests are not skipped or weakened to land a change | Review, plus a coverage floor per package |
| A query budget is not silently exceeded | A `database/sql` wrapper counts statements per test against a declared budget. `GET /standings` with 200 members is budgeted at four. This is the N+1 tripwire. |
| No goroutine leaks in the event and webhook packages | `goleak` in `TestMain` |

## Operational invariants

| Invariant | Enforced by |
|---|---|
| An upgrade never destroys guild data | Snapshot before migrate; auto-restore on failure; an integration test deliberately corrupts a migration and asserts the process exits non-zero **and** the database is byte-identical to the snapshot |
| A downgrade is refused, not attempted | The binary compares the schema version at boot and refuses to start, naming the correct image tag and the snapshot path |
| The container health check never touches the database | The Dockerfile calls `/healthz`, which does not. A database-touching health check lets Docker kill the container mid-migration. |
| `/metrics` is not exposed by default | Default `DKP_METRICS_ENABLED=false`; when enabled it binds a separate listener and requires a token. It is never gated by a token scope. |
| No secret is ever logged | `type Secret` renders as `***` in `String`, `MarshalJSON` and `LogValue`; a test marshals the whole config and asserts no known secret value appears; a second test posts a password through the login endpoint and asserts it appears nowhere in captured log records |
| No token appears in a URL | Query-string tokens are rejected with `401`, except on the compat shim, whose access logger redacts `atoken` — asserted by a test |

## What to do when two rules conflict

The invariant wins, and the conflict is a bug worth reporting. Say so rather than picking one
silently. [00 Canonical conventions](../design/00-canonical-conventions.md) is the tie-breaker between
any two documents; if a document disagrees with it, the document is stale.

## Next

- [The ledger](ledger.md) — the trust argument the data invariants exist to support
- [Point strategies](strategies.md) — which invariants a strategy may declare
- [Architecture overview](../development/architecture-overview.md) — where the mechanisms live
