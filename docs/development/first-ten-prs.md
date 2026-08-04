# The first ten pull requests

**Status:** plan. **Audience:** contributor, agent.
**Normative tie-breaker:** `docs/design/00-canonical-conventions.md`. Where this file and that one
disagree, that one wins and this file has a bug.

Each PR below is written to be handed to an agent verbatim. Together they are Phase 0 of
[`ROADMAP.md`](../../ROADMAP.md) plus the first two Phase 1 items.

## How to use this file

Take one PR. Do not take two. Do not do part of the next one "while you're in there".

**Definition of done, identical for all ten:**

| Rule | Mechanism |
|---|---|
| `make check` green | `ci-required` aggregate job |
| ≤ 2,500 lines of hand-written diff (generated files excluded) | reviewer, at their discretion |
| Conventional-commit title, exactly as printed below | `commitlint` in `lint/repo` |
| DCO sign-off on every commit | the DCO GitHub App |
| No new dependency without a separate proposal issue | `AGENTS.md` "Do not"; licence gate in PR 7 |
| Every acceptance criterion below is a *test or gate in this PR*, not a promise | reviewer |

An acceptance criterion phrased as "an agent could tell" is not an acceptance criterion. Every bullet
below either names a test file, a CI job, or a command whose exit code decides.

## Order and dependencies

| # | Title | Depends on | Why here |
|---|---|---|---|
| 1 | `chore: repository skeleton, licence, agent contract, ci-required gate` | — | Nothing merges until the gate that decides "merged" exists |
| 2 | `feat(store): SQLite pools, Tx helper, statement counter, template-DB harness` | 1 | Law 2 gets its lint rule at zero call sites |
| 3 | `feat(db): Atlas schema, goose runner, migrate-on-boot with snapshot and auto-restore` | 2 | **Deliberately third — prove the upgrade path before there is any data to lose** |
| 4 | `feat(api): Huma mount, problem+json, /api/v1/meta, spec drift gate, arch tests` | 3 | Law 1 and the spec gate get installed at route #1, not route #40 |
| 5 | `docs(api): EXAMPLE_ENDPOINT.md and RECIPES.md — one worked end-to-end resource` | 4 | **The highest-leverage documentation in the project** |
| 6 | `feat(web): SPA scaffold, generated client, go:embed, client-purity gates` | 4 | Law 4 gets its lint rule at zero components |
| 7 | `build: scratch image, goreleaser, release train, smoke gate, licence gate` | 1 | Every later phase then produces a pullable image |
| 8 | `feat(core): Micros, Centipoints, ULID, cursor codec, arithmetic lint bans` | 1 | The float ban must predate the first arithmetic |
| 9 | `feat(ledger): schema, append-only triggers, seq allocation, balance queries` | 3, 8 | — |
| 10 | `feat(ledger): batch service, invariant engine, first strategy, flagship properties` | 9 | — |

PRs 6, 7 and 8 have no ordering relationship to each other and can run as three parallel agents.

---

## PR 1 — `chore: repository skeleton, licence, agent contract, and the ci-required aggregate gate`

**Scope.** The tree, the paperwork, the agent contract, a `Makefile` whose targets match the canonical
command table exactly, a cobra root that serves `/healthz` without touching a database, and a CI
workflow whose aggregate job is the only thing branch protection ever needs to name. Every package in
the repo map exists as an empty package with a `doc.go`, so no later PR has to invent a location.

**Files touched.**

```
LICENSE  NOTICE  TRADEMARK.md  CONTRIBUTING.md  CODEOWNERS  README.md
AGENTS.md  CLAUDE.md  .editorconfig  .gitignore
.github/pull_request_template.md
.github/ISSUE_TEMPLATE/{parser-bug,import-failure,parity-gap}.yml
Makefile  go.mod
cmd/dkp/{main.go,root.go,version.go,serve.go}
internal/{api,store,ledger,strategy,importer,parse,cms,core,clock}/doc.go
internal/api/health.go
scripts/repo-gates.sh
.github/workflows/ci.yml
```

**Acceptance criteria.**

- `make check` exits 0 on a clean checkout with no network access after `make setup`.
- `TestMakefile_CanonicalCommandTable_EveryRowIsARealTarget` parses the command table in `AGENTS.md`,
  extracts every `make <target>`, and asserts `make -n <target>` exits 0. Deleting a target or adding
  an undocumented row to the table fails `make test-unit`.
- `dkp serve` started with `DKP_DB_PATH` pointing at an unreadable path still answers
  `GET /healthz` with 200 and `{"status":"ok"}`. This is canonical §13 and it is a test, not a
  convention: a DB-touching healthcheck lets Docker kill the container mid-migration.
- `ci-required` is a single job with `if: always()` that reads `needs.*.result`, treats `skipped` as
  success, **and** fails when a job that the path filter should have selected reports `skipped`.
  Branch protection requiring exactly `ci-required` and `DCO` is then sufficient.
- `scripts/repo-gates.sh` asserts every `uses:` in `.github/workflows/**` is pinned to a 40-character
  commit SHA. A fixture workflow containing `actions/checkout@v4` makes the script exit non-zero, and
  a test asserts that.
- `CLAUDE.md` contains the line `@AGENTS.md` and does not restate the four laws. A test asserts the
  four-laws heading appears in exactly one tracked file.
- `dkp version` prints the version, commit and build date, all injected by `-ldflags`, and
  `TestVersion_UnstampedBuild_ReportsDev` asserts the unstamped default is `dev`, never empty.

---

## PR 2 — `feat(store): SQLite pools, Tx helper, statement counter, and the template-DB test harness`

**Scope.** `internal/store` becomes the only package that may hold `*sql.DB` (law 2), with the exact
pragmas, a `Tx` helper that uses the write pool exclusively, a `database/sql` wrapper that counts
statements per test (every later statement budget depends on it), and a `TestMain` that builds a
template database once and clones it per test.

**Files touched.**

```
internal/store/{store.go,pragma.go,tx.go,counting.go,testing.go,main_test.go}
internal/store/{store_test.go,pragma_test.go,tx_test.go}
scripts/repo-gates.sh          # extend
.github/workflows/ci.yml       # add lint/repo job
```

**Acceptance criteria.**

- `TestPragmas_BothPools_MatchSpec` asserts, by querying each pool: `journal_mode=wal`,
  `busy_timeout=10000`, `synchronous=1` (NORMAL), `foreign_keys=1`, and — on the write pool only —
  `_txlock=immediate` and `db.Stats().MaxOpenConnections == 1`. The read pool has `max(4, NumCPU)`.
- `scripts/repo-gates.sh` bans `sql.Open`, `.Query(`, `.Exec(` and `total(` outside `internal/store`.
  `TestRepoGates_MisplacedSQLOpen_FailsGate` writes a tainted tree into `t.TempDir()`, runs the
  script against it, and requires a non-zero exit. The gate is tested, not trusted.
- `store.Tx(ctx, fn)` rolls back on any returned error and on panic, and re-panics. A test asserts a
  panicking `fn` leaves zero rows and no leaked connection.
- `newDB(t)` clones `template.db` (built once by `TestMain` via migrate + `VACUUM INTO`) into
  `t.TempDir()`. `BenchmarkNewDB_Clone` prints the p50 in the CI log. **This is the measurement that
  resolves item V4 of `verify-before-phase-0.md`** — record the number there.
- `goleak.VerifyTestMain` runs in `internal/store`. A deliberately leaked goroutine fails the package.
- The statement counter retains SQL text and is addressable as `store.Counted(t)`, because PR 9's
  `EXPLAIN QUERY PLAN` goldens and every later statement budget read from it.

---

## PR 3 — `feat(db): Atlas schema, goose runner, migrate-on-boot with snapshot and auto-restore`

**Deliberately third.** The upgrade path is proven before there is any data to lose. Retrofitting
snapshot-and-restore after a guild has six years of ledger in the file is a different, worse project.

**Scope.** `db/schema.hcl` as the single source of schema truth containing exactly one table
(`dkp_meta`, `STRICT`); `make gen` diffing it into goose migrations; the embedded runner; and the boot
sequence: read version → refuse a downgrade → snapshot → migrate with `PRAGMA integrity_check` after
each step → **auto-restore and exit 1 on failure**.

**Files touched.**

```
db/schema.hcl
db/migrations-sqlite/000001_init.sql        # GENERATED — never hand-edited
internal/migrate/{runner.go,snapshot.go,restore.go,version.go}
internal/migrate/*_test.go
internal/api/ready.go
cmd/dkp/migrate.go
Makefile                                     # gen, migration targets
.github/workflows/ci.yml                     # gen/verify-generated job
test/migrations/{fixture_broken/,golden/fresh_install_fingerprint.txt}
```

**Acceptance criteria.**

- `TestMigrate_FreshInstall_MatchesFingerprint`: a fresh migrate produces a `sqlite_schema` dump whose
  normalised SHA-256 equals `test/golden/…/fresh_install_fingerprint.txt`. Changing `schema.hcl`
  without `make gen` fails this test **and** the drift gate.
- `TestMigrate_BrokenMigration_RestoresByteIdentical`: a fixture migration that fails
  `PRAGMA integrity_check` causes exit code 1, stderr naming the failing migration file and the
  restore command, and the on-disk database SHA-256 is byte-identical to the pre-migration snapshot.
- `TestMigrate_NewerSchemaThanBinary_RefusesToStart`: a DB stamped above the binary's maximum exits 1
  with a message naming both the image tag that can read it and the snapshot path. It never migrates
  downward.
- `TestReady_MigrationsPending_Returns503`: with `DKP_AUTO_MIGRATE=false` and a pending migration,
  `/readyz` returns 503 with `{"check":"migrations","state":"pending","command":"dkp migrate"}` while
  `/healthz` still returns 200.
- `gen/verify-generated` runs `make gen` and fails on any diff with the literal message
  `run 'make gen' and commit`.
- `make migration NAME=<snake_case>` produces `NNNNNN_<snake_case>.sql` and refuses a name that is not
  `snake_case` or that would renumber an existing file (canonical §16 — migrations are append-only).
- Snapshots are `VACUUM INTO` + zstd into `/data/backups/pre-<ver>-<ts>.db.zst`, and a test asserts
  the snapshot is a *valid readable database*, not merely a file that exists.

**Also resolves.** Item V6 of `verify-before-phase-0.md` (does Atlas preserve hand-added triggers,
partial indexes and CHECKs across `migrate diff`). Add a trigger and a partial index to `dkp_meta`,
change an unrelated column, re-diff, and record the answer there. If Atlas drops them, PR 9's
append-only guarantee needs a preservation strategy and that must be known now.

---

## PR 4 — `feat(api): Huma mount, problem+json, request-id, /api/v1/meta, spec drift gate, arch tests`

**Scope.** The HTTP contract and the machine-checked guarantees, installed at route #1. One route
exists (`getMeta`); the point of the PR is the harness around it.

**Files touched.**

```
internal/api/{server.go,router.go,meta.go,errors.go}
internal/api/middleware/{problem.go,requestid.go}
internal/api/arch_test.go
internal/api/{meta_test.go,errors_test.go}
cmd/dkp/openapi.go
openapi/openapi.json                          # GENERATED, committed
.github/workflows/ci.yml                      # spec drift job
```

**Acceptance criteria.**

- `arch_test.go` enumerates the Huma registry and asserts, per operation:
  - `OperationID` is non-empty, unique across the registry, and `lowerCamelCase`;
  - `Hidden: true` appears only on the canonical §7 allowlist — `/healthz`, `/readyz`, `/metrics`,
    the OAuth callback, and the compat shim. The allowlist is a `const` slice; adding a sixth entry
    is a deliberate edit a reviewer sees.
  - the route is declared inside `internal/api`, proven by an AST scan of every package for
    `huma.Register` and comparing the set to the registry (law 1).
- `TestArch_MissingOperationID_FailsBuild`: deleting `OperationID` from `getMeta` fails CI.
- `TestArch_RouteOutsideAPIPackage_FailsBuild`: a fixture package calling `huma.Register` fails CI.
- `go run ./cmd/dkp openapi` output equals the committed `openapi/openapi.json` byte for byte.
  Changing a handler's response struct without `make gen` fails the drift job.
- Every error is RFC 9457 `application/problem+json` carrying `type`, `title`, `status`, `code`,
  `request_id`, and optional `meta`. `code` comes from the closed enum in `internal/api/errors.go`.
  `TestErrors_NoHandlerReturns200WithErrorBody` asserts no 2xx response body has an `error` key.
- `X-Request-Id` is echoed when supplied and generated (ULID) when not; it appears in every problem
  body and in every `slog` line for the request.
- `/openapi.json` and `/docs` (embedded Scalar) are served from the binary with no network fetch —
  asserted by a test that runs with outbound networking blocked.

**Also resolves.** Item V7 of `verify-before-phase-0.md`: emit one placeholder OpenAPI 3.1 `webhooks`
entry and confirm the document still parses. The three-generator confirmation completes in PR 6.

---

## PR 5 — `docs(api): EXAMPLE_ENDPOINT.md and RECIPES.md — one worked end-to-end resource`

**This is the highest-leverage documentation in the project.** Every subsequent endpoint task becomes
"copy the example, change the nouns". If this PR is mediocre, the next two hundred PRs are mediocre.

**Scope.** Implement `GET /api/v1/guild` and `PATCH /api/v1/guild` (the singleton `guild` row, `ETag`
+ `If-Match` on PATCH) and then write the two documents *from the actual code in this PR* — never
from memory, never from a library's README.

`internal/api/EXAMPLE_ENDPOINT.md` walks seven steps with real, copyable excerpts:

| Step | Artifact |
|---|---|
| 1 | `db/schema.hcl` entry |
| 2 | `db/queries/guild.sql` |
| 3 | the sqlc output it produces |
| 4 | handler struct + `huma.Register` with `Security`, `x-dkp-permission`, `OperationID` |
| 5 | the resulting `openapi/openapi.json` fragment |
| 6 | the generated TypeScript client call — **marked `PENDING PR 6`; PR 6 fills it in** |
| 7 | the integration test |

`db/RECIPES.md` is seeded with the first three query shapes: singleton fetch, upsert, and
`sum()`-with-`COALESCE` — the last carrying a bold note that `total()` is banned repo-wide because it
returns a float and silently defeats the centipoint invariant (canonical §1).

**Files touched.**

```
db/schema.hcl                        # guild singleton
db/migrations-sqlite/000002_guild.sql          # GENERATED
db/queries/guild.sql
internal/store/sqlitegen/**                    # GENERATED
internal/api/{guild.go,guild_test.go}
internal/api/EXAMPLE_ENDPOINT.md
db/RECIPES.md
openapi/openapi.json                           # GENERATED
scripts/eval-example-endpoint.sh
.github/workflows/nightly.yml
```

**Acceptance criteria.**

- `GET /api/v1/guild` returns the singleton with a strong `ETag`. `PATCH` without `If-Match` returns
  `428`; with a stale `If-Match` returns `412` with the current representation in `meta.current`
  (canonical §7). Three tests, one per case, against a real migrated SQLite file in `t.TempDir()`.
- `TestRecipes_TotalIsBanned`: a fixture query containing `total(` fails `scripts/repo-gates.sh`.
- Every code block in both documents is extracted and compiled by
  `TestDocs_ExampleEndpointSnippets_Compile`. A snippet that drifts from the code it documents fails
  CI — this is the mechanism that stops the example rotting, and without it the PR is worthless.
- **The eval gate.** `scripts/eval-example-endpoint.sh` starts a fresh agent session whose entire
  context is the repository plus `internal/api/EXAMPLE_ENDPOINT.md` and `db/RECIPES.md`, and hands it
  one instruction: *add `GET /api/v1/guild/settings` over a new `guild_setting(key, value_json)`
  table*. It passes when the resulting tree has `make check` green, the spec drift gate green, and a
  handler declaring `Security` and `x-dkp-permission` — **with no further guidance given at any
  point**. It runs nightly, not per-PR, because agent evals are slow and non-deterministic; a failure
  files an issue against the two documents, never against the code.

---

## PR 6 — `feat(web): SPA scaffold, generated client, go:embed, and the client-purity gates`

**Scope.** Vite 7 + React 19 + TanStack Router/Query, a generated and committed API client, the Go
side that embeds and serves it, and law 4's lint rule installed at zero components.

**Files touched.**

```
web/{package.json,pnpm-lock.yaml,vite.config.ts,tsconfig.json,eslint.config.js}
web/src/{main.tsx,routes/,components/}
web/src/api/**                                 # GENERATED
web/bundle-budget.json
internal/ui/{embed.go,embed_test.go}
internal/api/config_json.go
internal/api/EXAMPLE_ENDPOINT.md               # fill in step 6
.github/workflows/ci.yml
```

**Acceptance criteria.**

- ESLint `no-restricted-globals` bans `fetch` and `XMLHttpRequest` everywhere under `web/src` except
  `web/src/api`, plus a rule banning a `useEffect` body containing a fetch call. A fixture component
  with a bare `fetch()` makes `make lint` exit non-zero, asserted by a test.
- `pnpm run generate:client` output under `web/src/api/` is committed and diff-gated in the same job
  as the spec. Changing a handler without regenerating fails.
- `advisory/bundle-size` measures the gzipped initial route against `web/bundle-budget.json`
  (250 KB). The job **prints the measured number into the CI summary** — that number resolves item
  V13 of `verify-before-phase-0.md`. The budget is a one-way ratchet: raising it requires a CODEOWNERS
  review.
- CI installs with `pnpm install --frozen-lockfile --ignore-scripts`. A test fixture with a
  `postinstall` script proves the script does not execute.
- `internal/ui` serves hashed assets with `Cache-Control: public, max-age=31536000, immutable`, falls
  back to `index.html` for any non-`/api` path, and honours `DKP_WEB_DIR` for development.
  `TestEmbed_UnknownPath_ServesIndex` and `TestEmbed_APIPath_Returns404` cover both directions.
- `/config.json` supplies `API_BASE` at runtime so the SPA can be pointed at another instance.
- `TestDocs_NoPendingMarkers`: the literal string `PENDING PR 6` appears nowhere under `docs/` or
  `internal/api/*.md`, and step 6's snippet type-checks under `tsc --noEmit`.

**Also resolves.** The rest of item V7: run `openapi-typescript`, `openapi-python-client` and Scalar
over the committed document including its `webhooks` block, and record in
`verify-before-phase-0.md` whether all three consume it.

---

## PR 7 — `build: scratch image, goreleaser, release train, smoke gate, and the licence gate`

**Scope.** Distribution ships in Phase 0 so every later phase produces something pullable. Also the
supply-chain and licence controls, which cost nothing at zero dependencies and a week at two hundred.

**Files touched.**

```
Dockerfile  .dockerignore  .goreleaser.yaml
cmd/dkp/healthcheck.go
.github/workflows/{release.yml,edge.yml,ci.yml}
.github/renovate.json
scripts/{licence-gate.sh,eqdkp-identifier-gate.sh}
deploy/systemd/dkp.service
```

**Acceptance criteria.**

- The image is multi-stage ending `FROM scratch` with CA certs, tzdata, the binary, `USER 65532` and
  `VOLUME /data`. `advisory/image-size` enforces a 30 MB compressed budget.
- `HEALTHCHECK` invokes `dkp healthcheck`, which performs a plain loopback `GET /healthz` and touches
  no database (canonical §13 — **not** `dkp doctor`).
  `TestHealthcheck_UnreadableDatabase_StillSucceeds` asserts it.
- Multi-arch is produced by cross-compilation joined with `docker buildx imagetools`. A gate asserts
  the string `qemu` appears in no workflow file: QEMU builds are slow enough that someone will
  eventually disable the arm64 leg to make CI fast.
- Moving tags `:1`, `:1.x` and `:latest` advance only after a smoke job pulls the **immutable digest**
  on `linux/amd64` and `linux/arm64` and passes first boot plus `/readyz`. A release built from a
  deliberately broken binary leaves `:1` pointing at the previous digest — asserted by comparing the
  tag's resolved digest before and after.
- `cosign verify-attestation` succeeds for the SBOM and the `mode=max` provenance on the published
  digest, run in CI against the artifact CI just published.
- `scripts/licence-gate.sh` fails on any dependency under GPL, AGPL or a CC BY-NC variant. A fixture
  `go.mod` entry proves it fires.
- `scripts/eqdkp-identifier-gate.sh` fails on `pdh_`, `gen_class`, `plus_exchange` or
  `__multidkp2event` outside `internal/importer/legacy_names.go` and `internal/api/compat/`
  (canonical §15). Both allowlisted paths are empty today; the gate ships before the temptation does.
- `govulncheck` is wired into `ci-required` and is not `continue-on-error`.
- Renovate is configured with an explicit high-risk allowlist that never automerges `huma`, `goose`,
  `river`, the SQLite driver, or anything under `internal/auth`'s dependency set.

---

## PR 8 — `feat(core): Micros, Centipoints, ULID, cursor codec, and the arithmetic lint bans`

**Scope.** The four boundary types every later package depends on, plus the lint bans that make the
money invariant mechanical rather than aspirational. The bans must predate the first arithmetic.

**Files touched.**

```
internal/core/{micros.go,centipoints.go,ulid.go,cursor.go}
internal/core/{micros_test.go,centipoints_test.go,ulid_test.go,cursor_test.go}
internal/clock/{clock.go,fake.go}
.golangci.yml
internal/api/guild.go                          # retype updated_at
testdata/lintfixtures/{float_in_ledger.go,timenow_outside_clock.go,total_in_query.sql}
```

**Acceptance criteria.**

- Property tests at 200 checks: cursor encode/decode round-trip; **cursor order preservation**
  (`encode(a) < encode(b)` iff `a < b` for ULID keys, so cursors sort in a URL); `Centipoints` ↔
  float round-trip under round-half-even; `Micros` ↔ RFC 3339 at microsecond precision, always `Z`.
- `TestCentipoints_MarshalJSON_IsUnquotedInteger` and a JSON-Schema contract test asserting the type
  of every `*_centipoints` field in `openapi/openapi.json` is `integer` and never `string`
  (canonical §1).
- `.golangci.yml` bans `float32`/`float64` in `internal/ledger` and `internal/strategy`, `time.Now`
  outside `internal/clock`, and `total(` repo-wide. Each of the three fixtures under
  `testdata/lintfixtures/` is asserted to make `make lint` exit non-zero — one test per ban, so a
  disabled rule is a red test rather than a silent hole.
- Cursors are HMAC-signed with the instance key. A tampered cursor returns `400` with code
  `invalid_cursor`; a cursor whose embedded filter set differs from the request's is rejected with the
  same code, not silently honoured.
- ULIDs are 26-character Crockford base32, generated in Go, and monotonic within a millisecond.
  `TestULID_SameMillisecond_IsMonotonic` asserts it over 10⁵ generations.
- `guild.updated_at` from PR 5 now round-trips through `core.Micros` end to end — the smallest
  possible proof that the type is adoptable rather than merely defined.

---

## PR 9 — `feat(ledger): schema, append-only triggers, seq allocation, balance queries, snapshot`

**Scope.** The highest-blast-radius schema in the repo, with its guardrails, before any service writes
to it.

**Files touched.**

```
db/schema.hcl
db/migrations-sqlite/000003_ledger.sql         # GENERATED
db/queries/ledger.sql
internal/ledger/{balance.go,seq.go,snapshot.go,account.go}
internal/ledger/{trigger_test.go,balance_test.go,seq_test.go,strict_test.go}
db/RECIPES.md                                  # append the dialect-divergence note
test/golden/explain/ledger_balance.txt
```

**Acceptance criteria.**

- `TestTriggers_MutatingLedger_Raises` — four assertions executed as raw SQL, one per trigger:
  `UPDATE ledger_entry`, `DELETE FROM ledger_entry`, `UPDATE ledger_batch`, `DELETE FROM
  ledger_batch` each raise. Canonical §10 requires both the trigger *and* this test, so the guardrail
  cannot be silently regressed by a future migration.
- `TestSchema_LedgerTables_AreStrict` enumerates `sqlite_schema` and asserts every ledger table is
  `STRICT` and no column declares `BIGINT`, `BOOLEAN`, `DATETIME`, `NUMERIC`, `DECIMAL` or `REAL`
  (canonical §8).
- `BalanceAsOfSeq` uses `sum()`, never `total()`. `test/golden/explain/ledger_balance.txt` is the
  committed `EXPLAIN QUERY PLAN` output and shows the covering index `ix_entry_balance` with no table
  access. A plan regression fails the test; `-update` is refused when `CI=true`.
- `TestSeq_ConcurrentCommits_NoGapsNoDuplicates`: 100 concurrent commits against one pool yield
  exactly `1..100`. `seq` is allocated inside the write transaction and is **per pool**, never global
  (canonical §4).
- `TestSnapshot_TenThousandEntries_MatchesFold`: `balance_snapshot`, upserted synchronously in the
  same transaction as the batch, equals a naive Go fold over all entries.
- The unique indexes exist and are tested by inserting a duplicate: `ux_batch_seq`, `ux_batch_srcref`,
  `ux_batch_idem`, `ux_batch_reverses`.
- The four system accounts — `residue`, `guild_bank`, `write_off`, `import_opening` — exist after
  migration and are addressable by id, because the `Conserved` invariant must be verifiable from
  outside the package.
- `db/RECIPES.md` gains a section naming `seq` allocation as one of exactly three deliberate SQLite ⇄
  Postgres divergences (the others being the bid-hold lock and FTS). R11 depends on that list being
  written the day the first divergence appears, not the day of the port.

---

## PR 10 — `feat(ledger): batch service, invariant engine, first strategy, and the flagship properties`

**Scope.** The write path, the invariant engine as executable objects, the largest-remainder
allocator, the `PointStrategy` interface with its purity proof, and one strategy (`fixed_price`) to
prove the shape.

**Files touched.**

```
internal/ledger/{proposal.go,commit.go,invariant.go,allocate.go,hashchain.go}
internal/ledger/{commit_test.go,allocate_test.go,property_test.go}
internal/strategy/{strategy.go,fixed_price.go}
internal/strategy/{fixed_price_test.go,arch_test.go}
test/golden/strategy/fixed_price/*.json
.github/workflows/{ci.yml,nightly.yml}         # coverage floors, 20k nightly checks
```

**Acceptance criteria.**

- Four properties at 200 checks per PR and 20,000 nightly:
  - **P1** zero-sum conservation over random batch sequences;
  - **P2** `Σ credits == P` exactly for every `(P, N)` including `N = 1`, `P < N`, and `P` prime;
  - **P5** reversal is an exact inverse;
  - **P8** determinism — the same `rng_seed` produces a byte-identical batch.
- `ledger.Commit` writes batch, entries, `balance_snapshot`, audit row and outbox row **in one
  transaction**. `TestCommit_FaultInjectedMidWrite_LeavesNothing` asserts that after an injected
  failure none of the five exists.
- `TestCommit_DuplicateIdempotencyKey_ReturnsFirstBatch`: the second call is a no-op returning the
  first batch id. Uniqueness is `(principal_id, key)` where the principal is the service account or
  user — **never the token** — and a test rotates the token mid-retry and asserts the replay still
  hits (canonical §7).
- `TestAllocate_LargestRemainder_SumsToDebit` over 10⁵ random `(P, N)`: credits sum to exactly the
  debit, the residue lands on the `residue` account, and the tiebreak is `account_id` ascending —
  asserted explicitly, so it is a specified behaviour rather than an accident of map iteration order.
- `arch_test.go` proves `internal/strategy` imports none of `internal/store`, `time`, `math/rand`, by
  walking the import graph transitively. Adding any of the three fails CI (law 3).
- Every batch persists its `config_snapshot` and `rng_seed`, and `actor_is_beneficiary` is set on the
  batch at commit time. A replay from `rng_seed` reproduces the batch byte for byte.
- Coverage floors enforced as a CI job, not a report: `internal/ledger` ≥ 95%,
  `internal/strategy` ≥ 95%.

---

## After PR 10

What remains of Phase 0 is the `fixtures.yml` lane (started on day one, blocks Phase 5) and the
Phase 1 items PR 9 and PR 10 did not cover — the remaining invariants, the pool tables, `seed.Perf`
v1 and the strategy list. Those run as parallel agents against the shapes these ten PRs froze: one
new file plus one new test file each, no shared registry, no concurrent edit to `db/schema.hcl`.

See [`ROADMAP.md`](../../ROADMAP.md) for the phase table and the parallel-lane rules, and
[`verify-before-phase-0.md`](verify-before-phase-0.md) for the assumptions these ten PRs exist partly
to resolve — items V4, V6, V7 and V13 are answered by PRs 2, 3, 4 and 6 respectively, and the answers
belong in that file, not in a commit message.
