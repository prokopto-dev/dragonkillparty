# Testing strategy

**Status:** design. **Audience:** contributor, agent.
**Normative tie-breaker:** [`00-canonical-conventions.md`](00-canonical-conventions.md). Where this
file and that one disagree, that one wins and this file has a bug. Command names and budgets come
from the canonical table in [`AGENTS.md`](../../AGENTS.md).

## Why a trophy, not a pyramid

The pyramid is wrong here for one concrete reason: **there is nothing expensive about the
highest-fidelity test.** The database is a file in `t.TempDir()`, the server is
`httptest.NewServer`, migrations are `go:embed`ed, and River workers are goroutines. An integration
test that exercises a real migration, a real HTTP round-trip, a real trigger and a real background
job costs **~25 ms**. A mocked handler test of the same code costs ~0.2 ms and proves almost nothing.

**When the fidelity ratio is 100× and the cost ratio is 100×, you buy fidelity.**

So: a trophy — a wide integration body, a solid but selective unit base, a thin E2E cap, and a fourth
axis the classic trophy omits (static, architectural and contract gates) that is disproportionately
load-bearing in an agent-heavy codebase, because those gates fail at build time and cannot be
weakened by editing a `_test.go` file.

**(assumption)** The 25 ms figure is still the design's own estimate. The clone is no longer: PR 2
measured `newDB(t)` at **~1.0 ms p50** warm (0.44 ms of it the file copy), against a size-matched
stand-in template — roughly 3× the 0.3 ms guessed here, and still under a second across all ~900
planned integration tests, so the thesis holds. The measurement, its caveats and PR 3's obligation to
re-run it against the real schema are recorded as item V4 of
`docs/development/verify-before-phase-0.md`.

| Layer | Share of test *count* at maturity | Share of defects caught (estimate) | Runner | Budget |
|---|---|---|---|---|
| Static, architectural, contract gates | ~7% (≈150) | ~20% | `make vet`, `make lint` | folded into the build |
| Unit (table-driven, pure) | ~24% (≈480) | ~15% | `make test-unit` | **< 5 s** |
| Property and state-machine | ~6% (≈40 properties) | ~10% | `make test` at PR check counts | ~8 s of it |
| Golden-file (parsers, reports, query plans, SDK output) | ~12% (≈250) | ~8% | `make test-unit` | < 1 s |
| **Integration (real SQLite, real HTTP, real jobs)** | **~45% (≈900)** | **~40%** | **`make test`** | **~30 s** |
| Importer (real MariaDB containers) | ~3% (≈60) | ~5% | `make test-importer` | ~120 s, tag-gated |
| E2E (Playwright against the released binary) | ~1% (12 journeys) | ~2% | release workflow | ~3 min |

Percentages are targets to steer by, not quotas to enforce.

**The one hard rule: if a behaviour touches the database, the test that owns it is an integration
test.** No mocked repositories. No fake `Queries` implementation. Ever. The `Queries` interface exists
for the Postgres compile-time assertion (`var _ Queries = (*pggen.Queries)(nil)`), **not as a mocking
seam**, and a lint rule fails the build on any implementation of it outside `sqlitegen` and `pggen`.
An agent that assumes integration tests are expensive reaches for a mock, and mocks are how the
ledger's invariants stop being tested.

---

## Tooling

| Concern | Choice | Why this one |
|---|---|---|
| Runner | stdlib `go test`, always `-shuffle=on -count=1`; `-race` in CI | One runner, one idiom, no config file for an agent to get wrong |
| Assertions | stdlib `t.Fatalf` + `google/go-cmp` for structs; `testify/require` permitted, **`testify/assert` banned** | `assert` continues after failure and produces cascading noise (`AGENTS.md`). One style, enforced by lint |
| Property / state machine | `pgregory.net/rapid` | Native Go generators, automatic shrinking, `rapid.Run` state-machine mode for the bid FSM |
| Fuzzing | stdlib `go test -fuzz` | Corpus in `testdata/fuzz/`; crashers auto-persist as golden files |
| Concurrency and time | **`testing/synctest`** + `go.uber.org/goleak` | Fake clock and deterministic scheduling inside a bubble |
| HTTP | `net/http/httptest` + the generated Go client where one exists | Tests that call the SDK prove the SDK |
| Database | `modernc.org/sqlite` at `t.TempDir()/test.db`, template-clone | Real WAL, real `busy_timeout`, real triggers, real `_txlock=immediate` |
| Contract validation | **Open — see below** | |
| Containers | `testcontainers-go`, **only** for the importer's MariaDB and the nightly Postgres convergence job | Never for the main suite |
| E2E | Playwright + `@axe-core/playwright` | Runs against the shipped binary, not a dev server |
| Coverage | `go test -cover`, plus `go build -cover` + `GOCOVERDIR` + `go tool covdata` for the E2E binary | A genuine merged unit + integration + E2E profile |
| CI reporting | `gotestsum --format testname --junitfile`, **`--rerun-fails=0`** | JUnit for the UI, `go test -json` retained for flake mining |
| Tool pinning | `go install <tool>@$(<TOOL>_VERSION)` in `make setup`, with the pins and a `# renovate:` comment per tool in the Makefile | One number per tool, in one file. `.github/actions/setup-toolchain` reads those same Makefile lines, so CI and the laptop cannot install different binaries and disagree about a green build |

### Open item: which OpenAPI response validator

**Unresolved. Do not assert either answer in code or docs until Phase 0 settles it.**

The source design documents disagree. One prescribes `pb33f/libopenapi-validator` on the grounds that
Huma v2 emits **OpenAPI 3.1 with JSON Schema 2020-12** while `getkin/kin-openapi` lists 3.1 as not yet
supported. The adversarial review contradicts that: current sources indicate kin-openapi supports
3.0, 3.1 and 3.2, and notes that the "verified" label on the original claim appears stale.

Why it matters enough to be a named decision: the validating middleware runs across the **entire**
integration suite, so a validator that quietly mis-handles 3.1 constructs (`type: ["string","null"]`,
`const`, `$defs`, numeric `exclusiveMinimum`, `prefixItems`, top-level `webhooks`) is **worse than no
validator** — it produces green builds that mean nothing.

**Phase 0 action:** generate the real spec from a handful of Huma operations including one webhook and
one nullable field, run both validators against it, and pick one. Record the result in an ADR and
delete this section. Treat it as a worked example of the broader lesson: audit every other claim in
the source material carrying a "verified" label the same way.

---

## Unit testing: what genuinely deserves it

Unit tests are for **pure functions with interesting arithmetic or grammar**. Everything else is an
integration test. The list is short, and every item on it is a place where a bug is silent,
permanent, and about someone's points.

- **`internal/strategy` planners** — every strategy × every planner. Pure by architectural law
  (canonical §10). Table-driven, with the whole `BatchProposal` compared as canonical JSON against a
  golden file, so the *entire* proposal is asserted rather than three cherry-picked fields.
- **Ledger arithmetic** — largest-remainder allocation, `Centipoints` round-half-even at the float
  boundary, running-balance folding, hash-chain canonicalisation, reversal negation.
- **Decay math** — percent compounding, catch-up after a three-week outage, negative-balance policy,
  floor clamping, window taper, and derivation of the `(pool_id, cadence_period)` idempotency key
  across DST and month boundaries.
- **Attendance-window calculation** — the denominator is where the bugs are: `no_attendance` event
  exclusion, connected-raid dedup, the tenure-capped variant, day-based versus raid-based counting,
  alt roll-up, join-date grace, arbitrary ranges. Bucketing runs from integer micros into the guild
  timezone, so include a `Pacific/Chatham` (+12:45, with DST) case.
- **Log-line parsers** — `/who` in all its variants, RaidRoster TSV, loot lines, paired `/random`
  lines, chat award grammars, FTE `engages`, `has been slain by`, EQdkp raid XML. `[]byte → struct`,
  zero I/O.
- **Item name matching** — normalisation, **longest-candidate article stripping** (`a Shiny Brass
  Idol` must not become `Shiny Brass Idol` by a greedy prefix strip), trigram and Levenshtein ranking
  determinism, stability under equal scores.
- **Boundary types** — `Micros`, `Centipoints`, ULID ordering, cursor encode/decode/signature,
  idempotency-key body-hash canonicalisation, `name_norm` generation.
- **Importer transforms** — the PHP `serialize()` byte-level reader, entity-unescape ordering,
  per-value mojibake detect-and-repair, the table-map resolver's unknown and missing-field behaviour.

**Not unit-tested on purpose:** HTTP handlers (thin; integration owns them), anything with a
`*sql.DB` in scope, React components (no snapshots — brittle, low value), and CLI flag wiring.

### Property-based tests and their actual invariants

The ledger defines an executable invariant vocabulary (`SumZero`, `NonNegative`, `Permutation`,
`RatioPreserved`, `Conserved`, `MonotoneNonDecreasing`, `NoFloat`, `LargestRemainderSumsToDebit`).
Property tests are those invariants driven by generated input rather than hand-picked cases.

| # | Property | Generator | What it kills |
|---|---|---|---|
| P1 | **Zero-sum conservation.** For any random sequence of award / reversal / adjustment batches over a random roster, the sum of balances across all accounts equals a constant at *every* `seq` | roster 1–300, 0–500 batches, 1–10⁶ centipoints | The rounding leak. The flagship property. |
| P2 | **Largest remainder sums to the debit.** Credits sum to exactly `P`, for all `(P, N)` including `N=1`, `N=2`, `P` prime, `P < N` | `P ∈ [1,10⁹]`, `N ∈ [1,400]` | Per-credit independent rounding — mint/burn |
| P3 | **Balance equals the sum of the ledger.** The incrementally maintained `balance_snapshot` equals a from-scratch fold, at an arbitrary as-of-`seq` | random ledger + random snapshot cadence | Cache drift — the exact EQdkp failure mode this product exists to fix |
| P4 | **No bid can exceed balance.** For any interleaving of bids, holds, releases, settlements and decay runs across ≥2 concurrent sessions, no account commits a spend exceeding `balance(seq_at_settle)`, and active holds never exceed the balance | `rapid.Run` state machine over the bid FSM | Double-spend across sessions |
| P5 | **Reversal is an exact inverse.** Applying a batch and then its reversal restores every affected balance and every derived position | any batch from any strategy | The highest-yield property: it exercises every strategy's inverse logic |
| P6 | **`cap` clamps and is idempotent.** Applying the cap twice produces one batch and never moves a balance past the cap | random balances and caps | Double application after a restart |
| P7 | **`start_points` applies exactly once per account**, and never to an account that already has ledger history | random rosters with partial history | The "everyone got 1000 points again" ticket |
| P8 | **Determinism.** The same `(event, config, clock, seed)` produces a byte-identical proposal hash | random events | Map-iteration order; `time.Now` leaking into a planner |
| P9 | **Decay idempotency.** Two runs for the same `(pool_id, cadence_period)` produce one batch | random cadences and downtime gaps | "Decay ran twice after the box rebooted" |
| P10 | **Attendance monotonicity.** Adding a raid the member attended never decreases their window percentage; adding one they missed never increases it | random raid histories | Denominator and dedup logic |
| P11 | **Cursor round-trip and order preservation.** `decode(encode(x)) == x`, and `encode` is monotone with respect to the sort key | random `(sort_key, tiebreak_id)` | Duplicate-and-skip in every polling bot |
| P12 | **Parser totality.** No input panics; every accepted line re-serialises to something the parser re-accepts | `go test -fuzz` + rapid strings | Panics on hostile log lines |
| P13 | **Position permutation** under random insertions, removals and absences — *only if a Suicide Kings variant ships* | `rapid.Run` state machine | Ordering corruption no scalar assertion catches |
| P14 | **Ratio preservation under decay** — *only if `epgp` ships* | random pairs and schedules | The whole point of that model |

P13 and P14 are conditional: `epgp` and `suicide_kings` ship on pilot-guild request only, and a
property for a strategy that does not exist is maintenance with no payer.

Two policies make properties pay off rather than annoy:

1. **Failures become permanent unit tests.** When rapid shrinks a counterexample, the minimised input
   is checked in as a named table case *in the same PR as the fix*. The property stays; it just no
   longer carries the regression alone. Rerunning with a stored seed is **not** a regression test —
   seeds do not survive generator changes.
2. **Budgeted in CI, deep overnight.** `rapid.Check` at 200 checks on PRs (~8 s total), 20,000 checks
   nightly with a longer shrink budget. PR latency stays flat; depth arrives overnight.

Eight fuzz targets, one per parser family, run seed-corpus-only on PRs and 10 minutes each nightly.
Crashers land in `testdata/fuzz/` as CODEOWNERS-protected golden files.

---

## Integration testing: a real database, no containers

### Isolation: template database, decisively

The reasoning is written down because an agent will otherwise reinvent the wrong option.

| Strategy | Verdict | Why |
|---|---|---|
| Per-test transaction rollback | **Rejected** | The code under test owns its transactions (`store.Tx`). Wrapping it forces savepoint emulation, which changes trigger and `BEGIN IMMEDIATE` semantics — i.e. it disables the exact guardrails the tests exist to verify. With a single write connection an outer transaction also deadlocks the pool. And it cannot test the migration path at all. |
| Truncate between tests | **Rejected** | Needs a hand-maintained table list that will drift, and does not reset per-pool `seq`, so any `seq`-dependent assertion becomes order-dependent — poisonous under `-shuffle=on`. |
| **Template database** | **Adopted** | `TestMain` migrates once into `<tmpdir>/template.db` and `VACUUM INTO` compacts it; each test copies the file. A ~250 KB warm-cache copy is **~0.3 ms**. This is Postgres's `CREATE DATABASE … TEMPLATE` trick with none of the container tax. |

```go
func TestMain(m *testing.M) { store.BuildTemplate(); goleak.VerifyTestMain(m, ...) }

func newDB(t *testing.T) *store.Store {
    t.Helper()
    path := filepath.Join(t.TempDir(), "test.db")
    store.CloneTemplate(t, path)          // io.Copy — see the note below, NOT os.Link
    return store.Open(t.Context(), path)  // same pragmas, same two pools as production
}
```

**Not `os.Link`.** An earlier draft of this section suggested hard-linking the template where the
filesystem allows it. That is wrong for a file the test is about to write to: a hard link shares the
inode, so the first write through the "clone" mutates the template itself and silently contaminates
every test that clones it afterwards — including ones that have already passed. The saving is
microseconds; the failure is cross-test corruption that reproduces only under `-shuffle=on`. A
copy-on-write clone (`clonefile` on APFS, `FICLONE` on Btrfs/XFS) would be safe, but is not portable
and is not worth the build tags at 0.44 ms per copy.

Every test is `t.Parallel()`; every test owns its own file; the only serialisation is inside a single
database, which is exactly the production topology. The shared in-memory VFS is permitted **only** for
read-only query tests: WAL, `busy_timeout`, `_txlock=immediate` and `VACUUM INTO` behave differently
there, and a concurrency test on memdb is testing a different database than the one that ships.

### Fixtures and factories

Two tiers, and a hard rule between them.

- **Tier 1 — the template:** schema plus immutable reference data (the 14 P99 classes, race/class
  legality, the event catalogue, default ranks, default pools). Built once per package.
- **Tier 2 — factories:** Go functions, not YAML or SQL files.

```go
f    := fixture.New(t, db)              // RNG seeded from t.Name(), printed on failure
cle  := f.Character(fixture.Class("Cleric"), fixture.Level(60), fixture.Rank("Raider"))
raid := f.Raid(fixture.Event("Vulak`Aerr"), fixture.At(f.Clock.Now()))
tick := f.Tick(raid, fixture.Attendees(cle, war), fixture.Value(centi(100)))
```

Factories take `*testing.T` and call `t.Fatal` on error rather than returning one. That removes ~80%
of fixture boilerplate and the accompanying `if err != nil` noise an agent will otherwise generate.

**The rule:** factories write through the **real domain services**, never raw `INSERT`s, for anything
carrying an invariant. A factory that inserts a ledger entry directly can construct states the domain
forbids, and then you are testing fiction. Two documented exceptions, both build-tagged and living in
`test/legacy/`: the importer suite's staging writes, and the deliberately corrupt states used by the
recovery tests.

### The determinism kit

Every source of non-determinism is injected, and each injection has a lint rule behind it:

| Source | Injection | Gate |
|---|---|---|
| Wall clock | `Clock` interface; `DKP_TEST_CLOCK` for out-of-process runs | `time.Now` banned outside `internal/clock` |
| Randomness | Seeded RNG, and the **seed is persisted onto the batch** so a replay is byte-identical (canonical §10) | `math/rand` banned in `internal/strategy` |
| ULID entropy | Injected reader | — |
| Map iteration | Canonical JSON with sorted keys on every golden | golden diff would show it |
| Timezone | `TZ=UTC` in CI, plus two tests pinned to `TZ=Pacific/Chatham` (+12:45 with DST — the nastiest real offset) | — |
| Per-test RNG | Seeded from `t.Name()`, printed on failure | — |

### Keeping it fast, with numbers

**Target: `make test` ≤ 30 s wall, and no single integration test above 250 ms.** Enforced, not
aspirational: a `TestMain` wrapper records per-test duration and fails the package if p95 exceeds
250 ms or any untagged test exceeds 2 s.

| Cost | Budget |
|---|---|
| Template build (migrate + vacuum), once per package | ~120 ms |
| Per-test `newDB(t)` — clone, open both pools, ping both | ~1.0 ms p50 warm (0.44 ms of it the copy); measured in PR 2, see V4 |
| `httptest` server boot (routes + middleware) | ~4 ms |
| `seed.Small` | ~15 ms |
| Typical integration test | 10–40 ms |
| Full suite with `-race` | ~90 s, CI only |

Infrastructure that earns its place:

- **Statement-count budget.** The `database/sql` wrapper counts `QueryContext`/`ExecContext` per test
  *and retains the SQL text*, so a budget failure prints the offending queries in order. `GET
  /standings` at 280 members is budgeted at **≤ 4 statements**; `GET /persons/{id}/attendance` at
  ≤ 3; the default portal layout at ≤ 8. This is the cheapest N+1 detector in existence: fully
  deterministic, zero runner noise, and it fires the instant the regression appears rather than when
  someone notices the page is slow.
- **Job draining, not sleeping.** `jobs.DrainForTest(ctx)` runs the River workers until the queue is
  empty. Periodic jobs get one real-scheduler test each under `synctest`.
- **Trigger assertions.** `UPDATE ledger_entry` must raise; `DELETE FROM ledger_batch` must raise;
  `DELETE FROM audit_log` must raise. The guardrail is itself under test so a future migration cannot
  silently drop it (canonical §10).
- **Ledger replay.** `dkp verify-ledger` recomputes every balance from the append-only log and
  compares it against `balance_snapshot` and the published chain head. It runs **in CI against
  `seed.Perf`** on every PR, and **nightly in production** as a scheduled job that alarms on any
  drift. The snapshot is explicitly a droppable cache; replay is what makes that claim true rather
  than hopeful.
- **Response validation on every request** in the whole suite, failing hard (pending the validator
  decision above).
- **`goleak.VerifyTestMain`** in `events`, `webhook`, `bids`, `jobs`, `server`.
- **Outbound HTTP is stubbed with `httptest.NewServer`, never a mocking framework** — Discord
  OAuth/OIDC, webhook receivers, S3, the seed importer. Stubs live in `test/stub/` and are shared.

### `testing/synctest` — where it is the only reasonable tool

Time-dependent behaviour is tested inside a `synctest` bubble with a fake clock and deterministic
scheduling, never with `time.Sleep`:

| Behaviour | What the bubble buys |
|---|---|
| Bid session countdown and close | Assert the exact `closes_at` transition without waiting for it |
| **Anti-snipe extension** | A bid at T−2 s extends the window; assert the new deadline and that a second bid extends again up to the cap |
| SSE reconnect with `Last-Event-ID` | Drop the connection, advance the clock, reconnect, assert no gap and no duplicate in `event_seq` |
| Webhook retry backoff | Assert the full 5-attempt schedule and the dead-letter transition in microseconds of real time |
| Decay catch-up after downtime | Advance three weeks instantly; assert one batch per missed period and no double-post |
| Session and token expiry | Assert the exact boundary rather than a token minted with a 1-second TTL |

This single choice removes the largest flake source in Go services. It is what makes these behaviours
deterministic instead of hopeful.

---

## API contract testing

Five mechanisms, each closing a different hole:

1. **Golden OpenAPI snapshot.** `openapi/openapi.json` is a committed artifact generated from the Go
   types. CI runs `make gen` and `git diff --exit-code`. A handler change without a spec update is
   impossible, and so is the reverse. The diff is also the best review artifact in the repo: an API
   change shows up as readable JSON.
2. **Request *and* response validation in tests.** The validating middleware is active whenever
   `DKP_ENV != production`, so every request the suite makes and every response it receives is
   checked. Validating requests is not redundant — it catches tests that lie about the API, where an
   agent constructs a body the spec forbids and a lenient handler accepts it.
3. **Executable snippets.** Every `curl`, Python and JS example in `openapi/snippets/` and in `docs/`
   is executed against a freshly seeded test server. Docs cannot rot, because a rotted doc is a red
   build.
4. **Breaking-change detection.** `oasdiff breaking` against `origin/main`'s spec; a breaking delta
   fails unless the PR carries the label *and* a `docs/api-changelog.md` entry — and that file is
   CODEOWNERS-protected, which is the only thing that makes an author-settable label real. Plus
   additive-only tests inside v1: inject a synthetic future enum value and an unknown field into a
   response and assert the generated SDKs do not panic.
5. **Generated-client smoke tests.** CI regenerates the TS and Python SDKs, diff-gates them, then runs
   a ~40-line program per SDK against the test server: mint a PAT → create a raid → post a tick →
   award an item → read standings → paginate by cursor → replay the idempotency key and assert
   `Idempotency-Replayed: true` → trigger `409 insufficient_balance` and assert the typed error
   discriminant. This catches the class the spec diff misses entirely: specs that are *valid* but
   *ungeneratable* — duplicate or missing `operationId`, unnameable inline schemas, recursive `$ref`s
   the generators choke on, `oneOf` shapes that produce an untyped `any`.

**Architectural tests** over the Huma registry — cheap, and each makes a whole defect class
unmergeable:

- every operation declares `Security`, `x-dkp-permission` and `x-dkp-scopes`;
- every operation has a unique, non-empty, explicitly set `operationId` (auto-derivation forbidden —
  renaming a handler must not rename a public SDK method, canonical §7);
- every mutating `POST` that creates domain state declares `Idempotency-Key`;
- every collection response uses the shared pagination envelope, and `?since_seq=` appears only on
  `/ledger/*`, `/audit` and `/events/replay` (canonical §4);
- every mutable resource emits an `ETag`, and every state transition requires `If-Match`;
- every error `code` is in the closed enum and every `type` URL resolves to a page under
  `docs/errors/`;
- `Hidden: true` appears nowhere outside the canonical allowlist;
- every registered route is declared in `internal/api` (package scan versus registry);
- money fields are `integer` in the JSON Schema, never `string` (canonical §1).

**PAT-parity suite.** The Playwright run proxies through a recorder; the observed
`(method, path-template, body-shape)` sequences are stored as `test/parity/<journey>.jsonl`. The
integration suite replays those exact sequences with a **scoped PAT** instead of a session cookie and
asserts byte-identical responses modulo a documented volatile-field allowlist (ids, timestamps,
`server_time`, ETags, idempotency echoes). If the browser can do something a bot cannot, CI goes red.

State the risk honestly: **the normaliser is where an agent will hide a diff.** It lives in one file,
under CODEOWNERS, and a ratchet test asserts the allowlist length is non-increasing. Phase 2 ships
hand-written parity cases; Phase 3 adds the recorder plus a test asserting the recorded set is a
superset of the hand-written one. The hand-written cases are deleted only once that superset holds.

---

## The authorization test matrix

### The design

One table-driven test asserting **every operation × every principal → expected verdict**. Expectations
live in one committed golden table, `test/golden/authz_matrix.tsv`, mapping
`operationId × principal → {allow, 401, 403, 404}`. Generated once, reviewed by a human line by line,
then frozen.

The nineteen principal fixtures:

```
anonymous              member                 member_unclaimed       self_member
other_member           officer                officer_no_mfa         admin
owner                  session_stale_epoch    pat_zero_scope         pat_roster_read
pat_raids_write        pat_dkp_adjust         pat_bids_manage        pat_logs_ingest
pat_expired            pat_revoked            legacy_compat_token
```

There is no cross-tenant principal, because there is no `guild_id` and no second guild
(canonical §9). What that fixture used to prove — that a principal cannot reach a resource it must not
know exists — is proved instead by `member_unclaimed` and `other_member` against ownership-sensitive
resources, and by sealed-bid and hidden-rank cases.

The test:

1. builds a **canonical valid request** per operation from the OpenAPI `examples`, so it reaches the
   real authorization check rather than short-circuiting on validation;
2. executes it as each principal against a shared, read-mostly fixture guild;
3. asserts the verdict;
4. **fails if any registered operation is absent from the matrix** — adding a route without stating
   its permission expectations is unmergeable;
5. **fails if the matrix references an operation that no longer exists** — dead expectations get
   pruned;
6. asserts that a deny is a deny **at the data layer**: for a sample of denied writes, row counts are
   unchanged and no `event_outbox` row was written; for denied reads, the body leaks no resource
   attributes and no existence-revealing audit row is created.

The `403`-versus-`404` policy is encoded in the matrix rather than left to per-handler taste. Across
~140 operations that is the only way it stays consistent.

Runtime: ~140 operations × 19 principals ≈ 2,700 requests against an in-process server and a shared
clone. **Budget ≤ 8 s**, one parallel test with per-principal subtests.

`docs/reference/permissions.md` is rendered *from* the matrix and the catalogue, so the published
permission reference cannot disagree with the code.

### Why it is the single highest-value suite in the product

- **The blast radius is the entire value proposition.** DKP's only reason to exist over a spreadsheet
  is trust: an append-only, publicly readable, officer-writable record. One missing permission check
  on `POST /adjustments` and any member can mint themselves points — and because the ledger is
  append-only, that damage is permanent and public. You do not quietly fix it; you reverse it in front
  of the whole guild.
- **Authorization is the defect class an agent is most likely to introduce, and the only
  cross-cutting concern that fails silently-permissive.** Omit idempotency and a test fails. Omit the
  pagination envelope and a test fails. Omit `Security` and everything works beautifully, for
  everyone. It also looks fine in review, because the missing thing is a line that is not there.
- **Cost is O(1) per endpoint; coverage is O(endpoints × principals).** Adding a route costs one TSV
  row. Nothing else in the suite has that leverage ratio.
- **It survives refactoring.** The matrix is expressed over `operationId`s and role names, not
  internal structure, so a middleware rewrite is validated by it rather than invalidating it.

---

## Cross-checks: two implementations, one answer

Where a query is too clever to review by reading it, correctness comes from a second, independent
implementation rather than from a stronger assertion.

**Attendance.** A deliberately slow, obviously correct Go reference — a plain loop over all raids,
honouring `no_attendance` events and connected-raid dedup — runs against `seed.Perf` and is compared
with the SQL for 50 random `(member, window)` pairs. Two independent implementations agreeing is how
you get to trust a window-function query nobody can read. The reference implementation is not
optimised, is never used in production, and is deliberately kept naive.

The same shape appears elsewhere and is worth recognising as a pattern:

| Fast path | Independent oracle |
|---|---|
| `attendance_rollup` + SQL window functions | the slow Go loop |
| `balance_snapshot` + bounded delta scan | full replay in `dkp verify-ledger` |
| The importer's transform | the reconciliation classifier (below) |
| Hand-written assertions on ledger arithmetic | the twelve-plus rapid properties |
| Hand-read query performance | the `EXPLAIN QUERY PLAN` goldens |

An agent that weakens a hand-written assertion still has to get past an oracle it cannot edit without
the edit being conspicuous.

---

## Importer testing

### Fixtures

| Fixture | Provenance | Runs on |
|---|---|---|
| EQdkp Plus 2.0.5 / 2.1.5 / 2.2.27 / 2.3.39 | Built by running the real PHP installers in Docker, seeded with synthetic guild data, published as OCI artifacts to GHCR so contributors pull ~5 MB rather than rebuild; sha256 pinned in `test/fixtures/CHECKSUMS` | PR |
| **Anonymised real-guild dump** | A donated production database. **The most valuable fixture in the repo.** | PR |
| Hostile | Hand-crafted | PR |

The anonymised dump, concretely:

- **Consent is recorded** in `test/fixtures/eqdkp-real/PROVENANCE.md` — donating guild, date, scope of
  permission, contact.
- **Anonymisation is a checked-in, deterministic tool**, not a one-time manual scrub, so the
  transform is reviewable and re-runnable. Character names are replaced from a fixed pseudo-EQ name
  list keyed by a salted hash, so main/alt structure, duplicate names and `Bob`/`bob` case collisions
  all survive; emails become `user<N>@example.invalid`; password, API-key and session material is
  nulled or dropped; log IPs are zeroed. **Item names, raid dates, point values and every kind of
  dirt are left untouched — the dirt is the point.**
- Target ≤ 8 MB gzipped. If the donor guild is larger, sample by **raid-date window** — whole raids
  plus their attendees and items — never by row, so referential shape survives.
- Licence posture: EQdkp Plus is AGPL-3.0, but **a database dump is data, not the program**. No DDL
  text is transcribed into our source and nothing links against it. It lives under `test/fixtures/`,
  never under `internal/`, and the reasoning is written in `PROVENANCE.md` (canonical §15).
- **(assumption)** That a guild will donate one at all. If nobody says yes, the reconciliation
  classifier is only ever exercised against synthetic data, which is a materially weaker product.
  Ask three guilds before Phase 4.

The **hostile** fixture: latin1 double-encoding; entity damage on item names (`Trakanon&#39;s
Tooth`); duplicate attendance rows; orphaned items and attendees; a main-character cycle A→B→A plus a
self-reference; an MD5 password and a legacy salted bcrypt; an unknown plugin table *with rows*; a
half-applied 2.3.x migration; **two table prefixes in one database**; a MyISAM/InnoDB mix; a float
value where `round(v*100) != v*100`; a non-numeric foreign key; an attendance blob pointing at a
deleted raid; and `Bob` and `bob` coexisting, which is legal under the source collation and fatal
under any case-insensitive unique index you add.

### The end-to-end test and its invariants

```
for each fixture:
  start mariadb (testcontainers-go), load the dump
  dkp import eqdkp --source <dsn> --dry-run   → report matches golden JSON
  dkp import eqdkp --source <dsn> --commit
```

| # | Invariant |
|---|---|
| I1 | Character count equals source member count minus the rows the report declared unimportable |
| I2 | Raid count equals source raid count; attendance rows equal distinct source attendance rows |
| I3 | **The reconciliation classifier oracle.** Recompute every balance; the set of members whose totals differ from the source's stored points **equals exactly** the set predicted by the detection step. This converts "import as much as possible" from a hope into a CI-verifiable property, and it is the single most important assertion in the importer suite |
| I4 | Global conservation: earned − spent + adjustments equals the sum of balances |
| I5 | **No orphan rows.** Every ledger-entry foreign key resolves |
| I6 | Every id-map row round-trips `legacy_id → new_id → legacy_id` |
| I7 | No empty character names; no duplicate `name_norm` except those reported as collisions |
| I8 | Every text field is valid UTF-8 with no entity residue and no mojibake signature |
| I9 | Every money value is an int64 centipoint; the rounding-delta log row count equals the report's |
| I10 | Exactly one import batch chain exists and its hash chain verifies |
| I11 | **Phase-1 fidelity:** staging row counts equal source counts exactly. Any loss during staging is a bug, not a policy decision — policy belongs in the transform phase, where it is diffable in SQL |

**Round-trip.** Export the imported guild and assert the exported balances and pool structure equal
the recomputed ones. Import → domain → export must be lossless for everything the report claimed to
carry.

**Idempotency, three ways:**

1. Run `--commit` twice. Assert zero new rows of every kind, a second report reading `0 new / N
   unchanged`, and — the sharp assertion — **an unchanged head `ledger_batch.hash`**, which proves
   nothing was appended at all.
2. **Crash-resume.** Inject a fault after the raids table in the transform phase, kill the process,
   re-run. The result must be byte-identical to an uninterrupted run. This is what the staging
   boundary exists for; untested, it is just extra tables.
3. **Interleaved.** Import, let the guild create a raid natively, re-import. Assert the native raid
   survives and the import touches nothing it did not create.

**Budgets.** The real-guild fixture (~500k rows) imports in **≤ 90 s at ≤ 512 MB RSS**, asserted. The
"ORM-per-row is 40× slower" failure mode is real, and an agent will reintroduce it the first time it
refactors the bulk-insert helper. A separate write-latency test asserts an import in progress does not
starve raid-night writes: commits chunk at ≤ 2,000 rows with an explicit yield, and an import refuses
to start while any raid is open.

**Dry-run report goldens are the contract with the officer** — CODEOWNERS-protected, `-update`
refused in CI.

---

## Schema-migration testing

| Test | Mechanism |
|---|---|
| **Fresh install** | Apply all migrations to an empty database → normalise every `sqlite_schema` row → the SHA-256 must equal the committed `test/golden/migrations/fresh_install_fingerprint.txt`. Landed in Phase 0 PR 3 as `TestMigrate_FreshInstall_MatchesFingerprint`. The path is under `test/golden/` because that tree is CODEOWNERS-protected, and the fingerprint's whole job is to make a silent schema change loud. The fingerprint covers **every** row type including `type='trigger'` — Atlas cannot express triggers and a 12-step rebuild drops them silently (item V6), so this is the mechanism that notices |
| **Upgrade from each prior release** | The release workflow emits a schema + 20-row snapshot per version into `test/fixtures/schema/vX.Y.Z.db`. For each: apply migrations → the fingerprint must equal the *fresh-install* fingerprint, **and** the seeded rows must remain readable and semantically unchanged. On PRs, the last three releases; before release, every prior release. Bounded to the last 12 plus every `x.0.0`; older versions are documented as "upgrade via 1.x first" |
| **Destructive-migration detection** | Not a regex over SQL — **diff the Atlas inspections before and after** and compute removed tables, removed columns, narrowed columns and new `NOT NULL` without default, plus a scan for `DELETE`/`UPDATE` without `WHERE`. A hit fails unless the PR carries the label *and* the migration file contains a `-- dkp:destructive: <reason>` header. Inspection diffing is far harder to fool than a regex, which matters precisely because the fooling would be done by an agent trying to go green |
| **Migration-failure auto-restore** | Seed → inject a migration that fails halfway → assert exit code 1, the DB file **byte-identical** to the pre-migration snapshot, and a printed message naming the failing migration and the exact rollback command. This is the highest-value 40 lines in the product; untested it is decoration |
| **Downgrade refusal** | Write a schema version above `maxKnown`, boot, assert refusal and the exact operator-facing message |
| **Integrity** | `PRAGMA integrity_check` **and** `PRAGMA foreign_key_check` after every migration in the harness; `quick_check` on every restored snapshot |
| **Trigger survival** | After every migration, assert the append-only triggers still fire. Atlas authoring a migration that silently drops a trigger is the specific catastrophe this catches **(assumption:** that Atlas preserves hand-added triggers across `migrate diff` is unverified — it is a Phase 0 experiment**)** |
| **Postgres convergence** | Nightly: apply the Postgres migration set to a throwaway container, inspect, assert the normalised logical fingerprint matches SQLite's |

**Rule:** migration tests may not import domain packages. They run against SQL only, so a domain
refactor can never silently rewrite what a historical migration meant.

---

## E2E and browser testing

### The twelve journeys, and why each is irreplaceable

| # | Journey | What only E2E can prove |
|---|---|---|
| 1 | First-run wizard → admin created → dashboard | The bootstrap path, which by definition has no session or token. Nothing else exercises it |
| 2 | Local login, "sign out everywhere", OIDC login against a stub IdP | Cookie flags, rotation on privilege change, redirect handling |
| 3 | Create raid → paste a RaidRoster dump → tick created → attendance grid correct | The artifact → parse → preview → commit pipeline through a browser |
| 4 | Award an item with a fuzzy name → it lands in the reconciliation queue → resolve it | The unknown-name quarantine loop, the product's highest-value single feature |
| 5 | **Bid session from two browser contexts** → anti-snipe extension visible in both → close → resolve → settle → the statement shows the debit | Real SSE over a real connection, two clients, a server-authoritative countdown, a simultaneous board update. Untestable anywhere else |
| 6 | Standings for `seed.Demo` (200+ rows, 12 columns) → sort, filter, paginate, save a view | A virtualised grid plus server-side sort under real data density |
| 7 | Importer wizard against the anonymised fixture → dry-run report rendered → commit → reconciliation summary | The wizard is a pure API client; this proves it |
| 8 | Statement drill-down: balance → batch → source artifact → original log line | The "explain this number" chain that resolves guild disputes |
| 9 | Backup → download → `dkp restore` into a fresh data dir → standings identical | The operations promise, end to end |
| 10 | `/ops` diagnostics and the public standings page on a no-session browser | The server-rendered surfaces that must work when the SPA is broken |
| 11 | Mint a PAT → dev inspector "copy as curl" → replay that curl → identical response | Ties E2E to the PAT-parity suite |
| 12 | Block `EventSource` → assert the "live updates unavailable, polling" indicator and that the board still updates | The fallback everyone ships and nobody tests |

**Not worth the maintenance:** every CRUD form (integration owns the API; the form is a generated
client call), admin settings toggles, standings column permutations, error-state pages.

Mechanics: Playwright's `webServer` boots the **released binary** built with `go build -cover` and
`GOCOVERDIR` set, so E2E contributes to a genuine merged coverage profile via `go tool covdata`. One
server and one data directory per worker; the bid journeys pinned to a single worker. The server
clock is frozen via `DKP_TEST_CLOCK` — client-side clock control alone is not enough, because the
countdown is server-authoritative by design. Every run starts from `seed.Demo`, so any failure
reproduces with one command.

Journeys 1, 2, 6, 8 and 10 land in Phase 3; 3 and 4 in Phase 4; 5 and 12 in Phase 6. A PR runs a
three-journey smoke subset; the full matrix runs before release.

### Accessibility

`@axe-core/playwright` on the key screen of each journey, not every DOM state. **Gate: zero
`serious`/`critical` violations on the primary routes**, with a per-route, shrink-only allowlist under
the same anti-tampering rules as golden files.

State the limits honestly: axe covers roughly 30–50% of WCAG. Its value here is regression-catching on
this product's specific risks — form labels on the bid input, **contrast on guild-configurable class
colours** (an officer *will* pick an unreadable one), header semantics on the standings grid, and
focus order in the bid dialog. A unit test on the colour picker's contrast validator stops an
unreadable roster being configurable in the first place. Two manual checks per release: keyboard-only
completion of the bid flow, and a screen-reader pass on the statement view.

### Visual regression: not doing it

**Cut, including the three "narrow yes" baselines an earlier draft proposed.**

1. Maintainers are volunteers reviewing PRs on GitHub, where image diffs are painful. A red build with
   no readable cause is the fastest known path to a disabled test suite.
2. Baselines must be generated inside the CI container to survive font, anti-aliasing and GPU drift.
   That turns every legitimate CSS change into a "regenerate the baselines" commit that no human can
   meaningfully review and an agent will happily rubber-stamp — **which is exactly the golden-file
   tampering pathology described below, with far worse signal-to-noise.** A control whose failure mode
   is "re-run the tool and commit whatever it produces" is not a control.
3. This product's visual risk is concentrated in data density and theming, and both are better
   protected by the contrast checks and by deterministic **DOM assertions** than by pixels.

What replaces it, in the same journeys:

| Risk | DOM assertion |
|---|---|
| Standings layout collapse at 1280×800 with `seed.Demo` | Assert the expected column count is rendered, that no cell's text is truncated by CSS (`scrollWidth <= clientWidth`), and that the virtualiser has the expected row count in the viewport |
| Bid board in the closing state | Assert the state badge text, the countdown element's `aria-live` region, and the disabled state of the bid input |
| Theme tokens | Assert computed foreground/background pairs meet the contrast ratio — the same check the colour-picker unit test enforces, verified once end to end |

---

## Test data: the P99 seed dataset

One generator (`internal/seed`), one CLI (`dkp seed --profile demo|small|perf`), three profiles, fully
deterministic under a fixed seed — and **the same artifact is the demo instance, the E2E baseline, the
source of every OpenAPI `example`, and every code sample in the docs.** That triple duty is the point:
examples generated from a real seeded database cannot be wrong, and a contract test executes them. The
generator writes through the domain services, so seeding is itself a continuous exercise of every
write path.

**`seed.Demo`** — realistic enough that a P99 officer recognises their guild:

- Guild on server Blue, Velious era, `America/New_York`.
- **62 characters / 47 persons across all 14 P99 classes**, levels 51–60, with level-correct class
  titles and legal race/class pairings. 12 persons have alts, 3 characters are unclaimed, 2 inactive,
  1 bank mule on a hidden rank, 1 rename in the name history.
- Ranks: Leader / Officer / Raider / Trial / Alt / Bank, with Bank hidden.
- **Two pools, deliberately different:** a tick + decay-window + sealed-auction main pool, and a
  second pool on a different strategy — so every doc example shows multi-pool working.
- **22 real events** across eras, one flagged `no_attendance`.
- **9 raid sessions over 11 weeks**, 8–16 ticks each, 30–52 attendees per tick with realistic churn.
  **Two sessions are connected raids**, so connected-attendance dedup is exercised in every attendance
  example. One session has zero kills (attendance is still owed). One has four.
- **38 item awards** including every punctuation trap: `Cloak of Flames`, `Journeyman's Boots`,
  `` Vulak`Aerr's Mantle `` (backtick and apostrophe), `Shiny Brass Idol` (defeats greedy article
  stripping), one rot, one `/random`, one loot-council award with a reason, one multi-buyer split,
  and **one reversed award** so the corrections view has content.
- **1 live bid session** in the extended state with 6 bids and an active hold, so a fresh demo
  instance has a moving bid board, plus 2 settled sessions in history.
- ~4,900 ledger entries, 1 applied decay run, 3 adjustments, 1 open dispute, 4 artifacts with real
  content, 3 reconciliation-queue rows.
- **4 users and 2 service accounts with scoped PATs — the exact principals the authz matrix uses**, so
  the matrix and the demo share one truth.

**`seed.Small`** — 8 characters, 1 raid, 2 ticks, 1 award. The default for integration tests that need
*a* guild. ~15 ms.

**`seed.Perf`** — the realistic upper bound for a long-lived P99 guild: 280 characters / 190 persons,
3,400 raids over 6 years, 41,000 ticks, 61,000 attendance rows, 9,800 awards, ~520,000 ledger entries,
3 pools. Built once into `perf.db`, cached in CI by content hash, never rebuilt per run.
**(assumption:** that this is the real upper bound. Two `SELECT COUNT(*)` queries from two large
guilds settle it, and if real guilds are 5× larger the snapshot design needs re-examining.**)**

`seed.Perf` is built in layers as the domain arrives — ledger in Phase 1, roster in Phase 2, raids and
awards in Phase 4 — because the standings statement budget and the `EXPLAIN` goldens depend on it from
Phase 3 onward.

---

## Performance testing

### The scale that is actually real

| Dimension | Realistic | Design headroom |
|---|---|---|
| Characters | 100–300 | 500 |
| Raids over the guild's life | 3,000–4,000 | 10,000 |
| Ticks | ~40,000 | 120,000 |
| Ledger entries | **300k–800k** | 3M |
| Concurrent humans on a raid night | 30–70, all on one bid board | 200 |
| Writes on a heavy raid night | ~5,000 over 4 h (≈0.35/s, bursting to ~20/s at a settle) | 100/s |

**This is small.** The risk is not throughput; it is a single bad query shape over a 500k-row ledger
on a Raspberry Pi's SD card. Everything below follows from that.

### The two queries that will actually get slow

**Standings** — a per-account fold over the whole ledger, joined to attendance percentages across up
to four windows, sorted and paginated over 12 configurable columns. Three naive shapes appear in the
wild: one query per member (N+1 at 280 queries), a full ledger recompute per page load, or attendance
computed in Go. *Defence:* `balance_snapshot` plus a bounded delta scan over `seq >
snapshot.seq`, and a materialised `attendance_rollup` refreshed by a periodic job and invalidated on
raid mutation.

**Attendance windows** — the denominator is "raids held in this pool since T, honouring
`no_attendance` events **and connected-raid dedup**". The dedup goes quadratic if written at read
time, and the tenure-capped variant adds a correlated subquery per member, which is the classic hiding
place for an N+1. *Defence:* precompute the daily raid denominator (raids are immutable after
finalize), and **resolve connected raids into a group id at write time, not read time** — the single
most important schema decision for this query.

### Three layers, in increasing order of stability

| Layer | Assertion | Why it is the right shape |
|---|---|---|
| **Statement budget** | `/standings` ≤ 4 statements at 280 members; attendance ≤ 3; default portal layout ≤ 8 | Deterministic, zero noise, catches N+1 the instant it appears |
| **`EXPLAIN QUERY PLAN` golden** | The plan for each hot query is a checked-in golden (`test/golden/plans/standings.txt`). Must contain **no `SCAN ledger_entry`** and **no `USE TEMP B-TREE FOR ORDER BY`** | The cheapest and most stable perf test in existence: machine-comparable, immune to runner noise, and a plan change becomes a reviewable diff |
| **Wall-clock budget** | Benchmarks against `seed.Perf`: standings p50 ≤ 40 ms / p99 ≤ 150 ms; attendance p99 ≤ 60 ms; cold start < 400 ms. Failure threshold at **2× budget** | Generous on purpose: it catches order-of-magnitude regressions, not jitter. A tight budget here becomes a flaky test, and a flaky perf test gets deleted |

One budget test each for: the six-year member's statement (cursor pagination must not offset-scan),
the reconciliation queue at 3k unknown names, FTS5 item search, and the importer's bulk load.

**No k6, Gatling or Locust in 1.0.** A load test proving 5,000 rps is theatre when the real peak is 70
concurrent readers and one serialised writer. Instead, nightly soaks: 10k background jobs; **60
simultaneous SSE clients on one bid session for 30 minutes**, asserting zero dropped `event_seq`,
correct resync-and-close behaviour for one deliberately stalled client, no goroutine growth, and RSS
≤ 120 MB; and a 24-hour raid-night simulator replaying `seed.Perf` writes at 10× with the nightly
verify job asserting zero balance drift.

---

## Flake policy

**Zero tolerance, mechanically enforced.**

- `gotestsum --rerun-fails=0`. **Retries are forbidden for Go.** Playwright uses `retries: 1` in CI
  *solely so flakes are detected*: a test that passes on retry is reported flaky, and **a flaky result
  fails the build**. This is a single-process app with no eventual consistency; there is no legitimate
  reason a retry should help.
- **`-shuffle=on -count=1` always.** A shuffle failure is a real shared-state bug, not flake.
- **Any test that sleeps is a bug.** `time.Sleep` is grep-banned in `**/*_test.go` and `test/`.
  Sanctioned replacements: `testing/synctest`, `jobs.DrainForTest`, and bounded polling **only**
  against an out-of-process binary in E2E, with a named 10 s ceiling.
- **Quarantine is time-boxed and expensive.** A test may carry a `flaky` build tag for **7 days**, with
  a linked issue and a named owner; on day 8 CI fails on the presence of the tag. There is no
  permanent skip list, because permanent quarantine is how a suite dies — and the P99 audience will
  not forgive a DKP tool that "usually" computes the right balance.
- Every `t.Skip`, quarantine tag and race-disabled package requires
  `// FLAKE(#123, @owner, expires YYYY-MM-DD): reason`, linted for shape and expiry.
- **Flake dashboard.** CI uploads `go test -json` and the Playwright report; a nightly script
  aggregates the last 200 runs and opens or updates one issue listing any test that failed twice or
  more. This is the only way you learn about flakes that self-heal on local rerun.

---

## Coverage policy

**No repository-wide percentage gate.** A global number is gameable, and what gets gamed is assertion
strength — precisely the thing you cannot afford to lose.

| Control | Value |
|---|---|
| Overall statement coverage across `./internal/...` | **75%, measured and reported, not enforced** |
| **Enforced per-package floors** (ratchets — may only rise) | `internal/ledger` **95%** · `internal/strategy` **95%** · `internal/auth` **90%** · `internal/importer` **85%** |
| **Diff coverage on changed lines** | **≥ 80%**, from the merged unit + integration + E2E profile. This is the gate that correlates with defects and cannot be satisfied by testing unrelated easy code |
| Reporting | A per-PR table, not a badge. A drop in an enforced package fails; a drop elsewhere is a comment |

**Explicitly excluded**, via one CODEOWNERS-protected `coverage.exclude` file: generated code, CLI flag
wiring, `/ops` templates, `dkp doctor` check bodies that touch the host environment, `String()` and
`MarshalJSON` boilerplate, and unreachable I/O-fault branches. Each excluded item carries
`// coverage:ignore <reason>`, and a ratchet test asserts the count of those does not increase.

**The number nobody should chase:** `internal/api` handler coverage. Handlers are thin, their coverage
arrives free from the integration suite, and optimising it produces exactly the isolated handler unit
tests this design bans on purpose.

**Mutation testing is not a gate.** It is an optional per-release ritual, scoped to `internal/ledger`
and `internal/strategy` — a few thousand lines of pure, fast, arithmetic-dense code where boundary
mutations map one-to-one onto bugs that cost a guild real points and the equivalent-mutant noise rate
is low enough to triage by hand. It is not a CI gate because the practical Go tooling is 0.x with no
quality gate, no baseline file, no coverage-aware filtering and no diff mode, and because a mutation
score gate produces tests written to kill mutants rather than to describe behaviour — the same
pathology as a coverage gate, with a longer runtime. Surviving mutants are either killed by a new
assertion or annotated in `test/MUTANTS.md` with why they are equivalent. The properties above do most
of this work already.

---

## Protecting the golden files

**This section carries more weight than its length suggests. Rewriting a golden file is the fastest
path from red to green, it produces a diff that looks like data rather than a decision, and an agent
will find it.** Everything here exists because a plausible-looking regenerated fixture is
indistinguishable from a correct one at review time.

What is protected: `test/golden/**` and `test/fixtures/**` — parser goldens, the authz matrix, the
dry-run import reports, `EXPLAIN QUERY PLAN` goldens, the security-header goldens, the strategy
proposal goldens, the public-route allowlist, fuzz crashers, and the schema snapshots.

| Layer | Control |
|---|---|
| **Ownership** | `test/golden/**` and `test/fixtures/**` are CODEOWNERS-protected. Every regeneration needs a human named in the file, not merely a passing build. |
| **`-update` is refused in CI** | The flag works locally and returns an error when `CI=true`. A regenerated golden therefore always arrives as a deliberate local act, committed by a person, in a reviewable diff |
| **Non-decreasing fixture count** | A test asserts the number of files under `test/golden/**` and `test/fixtures/**` never decreases. Deleting an inconvenient case is the quietest possible tamper and this is the cheapest possible detector |
| **Readable diffs by construction** | Goldens are canonical JSON with sorted keys, one field per line, or line-oriented text. A regeneration that produces a one-line blob diff is itself a bug — an unreviewable format defeats every control above |
| **Ratchets that move one way** | Coverage floors, statement budgets, perf budgets, the a11y allowlist length, the PAT-parity volatile-field allowlist, the `coverage:ignore` count and the quarantine-tag count each have a committed number and a test asserting the measured value is on the correct side. Moving one is a one-line, unmissable diff |
| **A test-diff analyser** | A CI job runs `git diff origin/main -- '**/*_test.go' 'test/**'` through an analyser that flags: removed `t.Error`/`t.Fatal`/`require.*` with no replacement; an assertion changed from `Equal` to `Contains`/`NotNil`/`NoError`-only; a `cmp.Diff` gaining `cmpopts.IgnoreFields`/`IgnoreUnexported`; loosened numeric tolerances; new `t.Skip` or build tags; shrunk table-case lists; a lowered coverage floor; a **raised** statement or perf budget; a new parity-allowlist entry. Any hit requires CODEOWNERS review, and the bot posts the before/after assertions side by side. **This does not forbid legitimate loosening; it makes it visible, which is the entire game** |
| **Separate the oracle from the code** | The rapid properties, the slow reference attendance implementation, the reconciliation classifier, the OpenAPI spec and the `EXPLAIN` goldens are independent statements of intent. Weakening a hand-written assertion still leaves an oracle that cannot be edited inconspicuously |
| **Commit-shape convention** | Assertion-weakening changes go in their own commit prefixed `test-relax: `. Trivially bypassable by a determined actor, but agents follow local conventions well, and `git log --grep '^test-relax'` is a two-second audit of every assertion ever loosened |
| **Named in `AGENTS.md`** | Never delete or skip a test to make CI pass; never rewrite anything under `test/golden/` or `test/fixtures/` to go green; never add `cmpopts.Ignore*` to make a diff pass; never raise a budget in the same PR as a feature; never touch the PAT-parity normaliser; never edit `.github/workflows/**` to make a job green. **If a test is wrong, say it is wrong and why, in the PR** |

**The strongest control is structural.** Most of this suite's strength does not live in assertion text
at all: database triggers, append-only enforcement, the compile-time `Queries` assertion, `-race`,
`-shuffle`, `goleak`, spec drift, codegen drift, the bundle-size budget and the licence gate cannot be
weakened by editing a `_test.go` file. Design for that — **prefer a guardrail that fails at compile
time or in the database over an assertion in a test**, and then write a test asserting the guardrail
fires.

---

## Gates: before merge, before release

**Green before merge** — `make check` locally (~60 s); the CI job ~4–6 min:

1. `go build ./...` under **both** dialect tags — this is where the Postgres `Queries` assertion fires.
2. `make vet` and `make lint`: `go vet`, `staticcheck`, `tsc --noEmit`, ESLint, plus the custom rules —
   no floats in `ledger`/`strategy`; no `time.Now` outside `internal/clock`; no `sql.Open`/`.Query(`/
   `.Exec(` outside `internal/store`; no `http.Client` outside `safehttp`; no `time.Sleep` in tests;
   no `testify/assert`; no `fetch`/`XMLHttpRequest` outside `web/src/api`; no bare JSX strings.
3. **Codegen drift**: sqlc, Atlas, `openapi.json`, and both SDKs — all `git diff --exit-code`.
4. `go test -race -shuffle=on ./internal/...` — unit, property, golden, integration, contract and the
   **authz matrix**.
5. Architectural tests over the Huma registry.
6. Migration tests: fresh-install fingerprint, upgrade from the last three releases, the
   destructive-migration detector, trigger survival.
7. `make test-importer` — four version fixtures, the hostile fixture, and the anonymised real dump.
8. `oasdiff breaking`, or the label plus an api-changelog entry.
9. Licence gate and the EQdkp-identifier grep.
10. Golden-file anti-tampering: `-update` absent, fixture count non-decreasing, CODEOWNERS satisfied.
11. Playwright smoke subset — three journeys.
12. Diff coverage ≥ 80%; per-package floors met.
13. Bundle-size budget ≤ 250 KB gzipped for the initial route — a hard gate, since it is the only
    thing between a volunteer's Pi and a 3 MB bundle.

**Green before release** (nightly workflow plus the release workflow on the tagged commit):

1. Everything above.
2. **Full Playwright suite** × {Chromium, Firefox, WebKit} × {desktop, mobile}, all 12 journeys plus
   the a11y gate.
3. Upgrade from **every** prior release fixture; the migration-failure auto-restore test; downgrade
   refusal.
4. Backup → restore → `dkp verify-ledger` on `seed.Perf`, on natively built amd64 **and** arm64.
5. Perf budgets against `seed.Perf`: standings p99, attendance p99, cold start, import time and RSS.
6. Soaks: 10k jobs; 60 SSE clients × 30 min; the 24-hour raid-night simulator with the nightly verify
   job asserting zero drift.
7. Postgres schema convergence.
8. Container: multi-arch build, scratch image starts, healthcheck passes, size budget, cosign + SBOM +
   provenance attached; **fresh-install smoke** (run → wizard → create raid → backup) and **upgrade
   smoke** (previous tag → same volume → new tag → data intact and `verify-ledger` clean).
9. Docs: every snippet executed; every error `type` URL resolves; `dkp doctor` output reviewed.
10. Optional: the mutation ritual triaged, `test/MUTANTS.md` current.

---

## How an agent runs tests

The canonical command table lives in [`AGENTS.md`](../../AGENTS.md) and CI asserts every row is a real
Makefile target. **Needing a command that is not in that table means adding the Makefile target *and*
the table row in the same change — never inventing one.**

```
make check          # THE gate. build (both tags) + vet + lint + gen-drift + unit + property
                    # + golden + integration + contract + authz matrix.              ~60 s
make test-unit      # pure tests only, no database                                   < 5 s
make test           # the full default suite, WITH a real database                   ~30 s
make test-importer  # needs Docker; build-tagged, excluded from make check           ~120 s
make gen            # sqlc + atlas + spec + SDKs. Run after ANY schema or handler change.
make seed           # seed a dev guild
```

Tighter loops inside `make test-unit`'s scope, for when you know where the bug is:

```
go test ./internal/ledger/...                     # ~0.4 s   ← the tightest loop; use it
go test ./internal/strategy/... -run TestZeroSum   # sub-second
go test ./internal/... -race -shuffle=on           # exactly what CI runs
```

E2E, the perf budgets and the mutation ritual have **no Makefile row yet**. They run in the release
workflow. If they earn a local target (`make test-e2e`, `make test-perf`, `make mutate`), the
`AGENTS.md` row ships in the same PR.

**Inner-loop doctrine**, stated as a rule an agent can follow:

> Write the failing test first, **at the highest level that can express the bug.** For anything
> touching the database that is an integration test — it costs 25 milliseconds, not 25 seconds. Run
> the single package. Run `make check` once, before proposing the change. Never run the E2E suite in
> the inner loop.

`AGENTS.md` says explicitly that **integration tests are cheap here and mocks are banned**, because an
agent that assumes otherwise reaches for a mock, and mocks are how the ledger's invariants stop being
tested.
