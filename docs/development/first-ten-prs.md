# The first ten pull requests

**Status:** plan. **Audience:** contributor, agent.
**Normative tie-breaker:** `docs/design/00-canonical-conventions.md`. Where this file and that one
disagree, that one wins and this file has a bug.

Each PR below is written to be handed to an agent verbatim. Together they are Phase 0 of
[`ROADMAP.md`](../../ROADMAP.md) plus the first two Phase 1 items.

**"Phase 0 PR N" means this file's PR N — everywhere in the repository.** `ROADMAP.md` numbers
*deliverables*, and the two sequences do not line up: its deliverable 10 (the release pipeline) is
PR 7 here, and PR 10 here is the ledger batch service. Cite a ROADMAP item as "deliverable N", never
as a PR. PR 12 is the one place the numbers coincide, and it coincides on purpose — see **On the
number** under PR 12.

## How to use this file

Take one PR. Do not take two. Do not do part of the next one "while you're in there".

**Definition of done, identical for all ten:**

| Rule | Mechanism |
|---|---|
| `make check` green | `ci-required` aggregate job |
| ≤ 3,500 lines of hand-written diff (generated files excluded) | reviewer, at their discretion |
| Conventional-commit title, exactly as printed below | `commitlint` in `lint/repo` |
| DCO sign-off on every commit | the DCO GitHub App |
| No new dependency without a separate proposal issue | `AGENTS.md` "Do not"; the licence gate, landed in PR 12 |
| Every acceptance criterion below is a *test or gate in this PR*, not a promise | reviewer |

An acceptance criterion phrased as "an agent could tell" is not an acceptance criterion. Every bullet
below either names a test file, a CI job, or a command whose exit code decides.

**On the line budget.** It was 2,500, and PRs 3, 4 and 5 came in at 4,095, 4,974 and ~3,600 measured
hand-written lines. Three consecutive overruns of 60–100% is a budget that does not describe this
codebase, not three undisciplined PRs. Two mandated properties account for most of it, and both are
measured in [`phase-0-pr5-decisions.md`](phase-0-pr5-decisions.md): house comment density runs 30–74%
of a file (`internal/api/permissions.go` 74%, `meta.go` 50%, `server.go` 44%, `errors.go` 30%), and
"every acceptance criterion is a test or gate **in this PR**" put 1,009 lines of gate-tests in PR 3
and 1,373 in PR 4 — about a quarter of each. AGENTS.md requires both, so a budget that forbids them
is asking for a contract violation.

3,500 is the observed midpoint less what splitting a two-artefact PR saves. **Exceeding it is a
signal to split, not to report and continue** — which is what the number is for, and what it stopped
being.

## Order and dependencies

| # | Title | Depends on | Why here |
|---|---|---|---|
| 1 | `chore: repository skeleton, licence, agent contract, ci-required gate` | — | Nothing merges until the gate that decides "merged" exists |
| 2 | `feat(store): SQLite pools, Tx helper, statement counter, template-DB harness` | 1 | Law 2 gets its lint rule at zero call sites |
| 3 | `feat(db): Atlas schema, goose runner, migrate-on-boot with snapshot and auto-restore` | 2 | **Deliberately third — prove the upgrade path before there is any data to lose** |
| 4 | `feat(api): Huma mount, problem+json, /api/v1/meta, spec drift gate, arch tests` | 3 | Law 1 and the spec gate get installed at route #1, not route #40 |
| 5a | `feat(api): guild settings resource, ETag/If-Match, and the permission catalogue` | 4 | The code the worked example is written from — and the first real `x-dkp-permission` |
| 5b | `docs(api): EXAMPLE_ENDPOINT.md and RECIPES.md, written from PR 5a's code` | **5a** | **The highest-leverage documentation in the project** |
| 6 | `feat(web): SPA scaffold, generated client, go:embed, client-purity gates` | 4 | Law 4 gets its lint rule at zero components |
| 7 | `build: scratch image, goreleaser, release train, smoke gate` | 1 | Every later phase then produces a pullable image |
| 8 | `feat(core): Micros, Centipoints, ULID, cursor codec, arithmetic lint bans` | 1 | The float ban must predate the first arithmetic |
| 9 | `feat(ledger): schema, append-only triggers, seq allocation, balance queries` | 3, 8 | — |
| 10 | `feat(ledger): batch service, invariant engine, first strategy, flagship properties` | 9 | — |

PRs 6, 7 and 8 have no ordering relationship to each other and can run as three parallel agents.

**PR 5 is two PRs, and the order is load-bearing.** It was one until
[`phase-0-pr5-decisions.md`](phase-0-pr5-decisions.md) priced it at ~3,600 hand-written lines across
a resource, two documents, a snippet compiler and a nightly agent eval. 5a ships the code; 5b writes
the documents *from that merged code*, which is what this file has always required of them
("from the actual code in this PR — never from memory"). Writing them in the other order is what
produced today's `internal/api/EXAMPLE_ENDPOINT.md`, which describes `humachi.New`, `problem.From()`
and `store.Q()` — none of which exist. Each half is independently mergeable and green.

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
- `scripts/repo-gates.sh` (the entry point for `internal/repogate`, ADR-0018) asserts every `uses:`
  in `.github/workflows/**` is pinned to a 40-character commit SHA. A fixture workflow containing `actions/checkout@v4` makes the script exit non-zero, and
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
  The script is the entry point; the rules are `internal/repogate` (ADR-0018) — `SQL001`/`SQL002`
  read the parsed Go, `MONEY002` is a text rule declared in `internal/repogate/rules.hcl`.
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
sequence: read version → refuse a downgrade → snapshot → migrate one file at a time, checking after
each → **auto-restore and exit 1 on failure**.

> **Extended after PR 3, by #39.** The check after each migration was `PRAGMA integrity_check`
> alone when this PR was written. It is now four, in order: restore `PRAGMA foreign_keys = ON`,
> `PRAGMA integrity_check`, `PRAGMA foreign_key_check`, and append-only survival — a migration that
> dropped a ledger table or one of its `BEFORE UPDATE OR DELETE` triggers is refused and the
> snapshot restored. See [Upgrade and
> backup](../operations/upgrade-and-backup.md#what-happens-at-boot) for the sequence and every
> message it produces.

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
test/migrations/*_test.go                   # SQL-only; imports no domain package
test/fixtures/migrations/{broken,future}/    # deliberately broken + a valid "next release"
test/golden/migrations/fresh_install_fingerprint.txt
```

**Acceptance criteria.**

- `TestMigrate_FreshInstall_MatchesFingerprint`: a fresh migrate produces a `sqlite_schema` dump whose
  normalised SHA-256 equals `test/golden/migrations/fresh_install_fingerprint.txt`. Changing
  `schema.hcl` without `make gen` fails this test **and** the drift gate.

  > **Corrected in PR 3.** The files-touched block above said `test/migrations/golden/…` while this
  > criterion said `test/golden/…`. Per AGENTS.md the conflict is a bug, so it is recorded rather
  > than quietly picked: `test/golden/` wins, because it is CODEOWNERS-protected
  > (`.github/CODEOWNERS:65`) and hook-gated, and a fingerprint parked somewhere an agent can rewrite
  > unnoticed defeats the only thing a fingerprint does. `docs/design/04-testing.md:531` names a
  > third path, `db/schema.fingerprint`, which is now also wrong and is that document's to fix.
- `TestMigrate_BrokenMigration_RestoresByteIdentical`: a fixture migration that fails
  `PRAGMA integrity_check` causes exit code 1, stderr naming the failing migration file and the
  restore command, and the on-disk database SHA-256 is byte-identical to the pre-migration snapshot.
  The same contract now covers all four post-migration checks — a migration that drops a ledger
  trigger exits 1 and restores byte-identically too (#39).
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

  > **Corrected in PR 4, on both halves of the Hidden bullet.**
  >
  > **It is read from the source, not the registry, because the registry cannot see it.**
  > `huma.Register` runs `else if !op.Hidden { oapi.AddOperation(&op) }`, so a hidden operation is
  > never added to `Paths` and is indistinguishable from one that was never written. The same AST
  > scan that proves law 1 reads `Hidden` and `Path` out of the `huma.Operation` literal.
  > `.github/workflows/ci.yml` had listed this assertion under `make verify-spec`, where it is even
  > less implementable — the committed JSON holds strictly less than the registry — and that comment
  > is corrected in the same change.
  >
  > **The allowlist has four entries, not five, and is a function rather than a `const` slice.** Go
  > has no const slices; a package-level `var` would be the mutable global state
  > `.claude/rules/go-idioms.md` bans, so `api.HiddenOperationAllowlist()` returns a fresh literal —
  > which preserves the property this bullet actually asks for, that a new entry is a visible edit.
  > The fifth path is missing because the OAuth callback's route is not written down anywhere in
  > this repository: canonical §7 and `docs/design/02-api-design.md:609` both name it in prose and
  > neither gives a path. Guessing one would put an unverified value in a merge-blocking gate. The
  > omission is the correct behaviour — the PR that adds that route adds its path here.
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

  > **Corrected in PR 4.** Both paths are served under the version prefix, as
  > `/api/v1/openapi.json` and `/api/v1/docs`. This criterion wrote them at the root while
  > `docs/api/getting-started.md:162-163` — the page a user reads — and
  > `docs/design/02-api-design.md:170-171` both put them under `/api/v1`, and that document's §4.1
  > preamble states every path in its table is relative to `/api/v1` unless marked otherwise, which
  > `/healthz` and `/readyz` are and these are not. Two sources to one, canonical conventions silent,
  > so this line was the outlier. Recorded rather than quietly picked, per AGENTS.md.
  >
  > **The "outbound networking blocked" test was written differently, and deliberately.** Blocking
  > egress from inside a Go test is not portable — it would run on Linux CI and skip on the macOS
  > laptops, which is the half that matters for a contributor. `TestDocs_Page_FetchesNothingFromThe`
  > `Network` asserts the stronger property instead: the served HTML contains no external URL at
  > all, so there is nothing to block. A blocked-network test passes both when a page fetches
  > nothing and when it fetches something and degrades quietly; this one does not.
  > `TestDocs_Page_CSPForbidsEveryExternalOrigin` adds the enforcement half — `default-src 'none'`
  > with a self-only allowlist, so the browser refuses an external fetch even if a future Scalar
  > build attempts one.

**Also resolves.** Item V7 of `verify-before-phase-0.md`: emit one placeholder OpenAPI 3.1 `webhooks`
entry and confirm the document still parses. The three-generator confirmation completes in PR 6.

---

## PR 5a — `feat(api): guild settings resource, ETag/If-Match, and the permission catalogue`

**Scope.** Implement `GET /api/v1/guild` and `PATCH /api/v1/guild` (the singleton `guild` row, strong
`ETag`, `If-Match` on PATCH), and create the permission catalogue that PR 4's SPEC005 tripwire
requires. This is the code half of what was one PR; the two documents are PR 5b, written from this
code once it has merged. Every decision below is evidenced in
[`phase-0-pr5-decisions.md`](phase-0-pr5-decisions.md).

**`internal/authz` at this PR: the Go catalogue only.** `internal/authz/catalogue.go` holds the whole
canonical §6 list — every permission key and every PAT scope — carrying `Key`, `Category`, `Label`
and `Description` per permission and nothing else. **No policy fields**: `RequiresStepUp`,
`IsDangerous` and `SortOrder` wait for Phase 2, which builds the middleware that can test them
against a real consumer. The "declare it whole now" argument applies to *keys*, which both SDKs and
the Phase 2 table seed derive from; nothing derives from a policy flag, and PR 5a's two operations
exercise neither step-up nor PAT-forbidden. **No `permission` table, no `role_permission`, no
seed, no boot reconciliation**: those are ROADMAP Phase 2 deliverable 3, they arrive with the `role`
table that makes the FK meaningful, and a migration cannot be un-shipped. The whole list ships now
for the reason `internal/api/errors.go:145-148` gives about the error enum — the catalogue is what
the PAT scope enum and the Phase 2 table seed derive from, and growing it one key per PR makes every
later endpoint PR trip "adding a permission key is a schema change — stop and ask" for a key already
published in canonical §6.

`scripts/verify-spec.py` — now `checkPermissionsResolve` in `internal/specgate/rules.go`, issue #127,
and the port kept this property verbatim — does a **quoted exact-substring match against the file
text**, so every key must appear as a whole quoted literal (`"roster.read"`). A composed key
(`Resource + "." + Action`) fails the gate — measured, both directions. Say so in the file's header
comment. Note also that `test/repo/spec_gate_test.go:383` writes `var Keys = []string{…}` as a
throwaway fixture; that is not the catalogue's shape, because `.claude/rules/go-idioms.md` bans
package-level mutable state. Use a function returning a fresh literal, as
`api.HiddenOperationAllowlist()` does and for the same reason.

**The `guild` table ships twelve columns**, and only twelve: `id`, `name`, `tag`, `timezone`,
`week_start`, `points_label`, `points_precision`, `inactive_after_days`, `auto_set_inactive`,
`hide_inactive`, `created_at`, `updated_at` — everything `docs/design/02-api-design.md` §4.2 already
promises in the public resource map. `locale`, `public_standings`, `artifact_retention_days`,
`redact_tells` and `settings_json` are in the domain model and are **not** shipped: each has no
reader until Phase 2 or Phase 4. The freeze rule cuts this way, not the other — `ALTER TABLE ADD
COLUMN` is a cheap forward migration, while *removing* or retyping a column is SQLite's 12-step
rebuild, so over-shipping is the expensive mistake.

**`x-dkp-scopes` is declared on every operation**, alongside `Security` and `x-dkp-permission`.
Four documents have required it since before PR 4 and no code emitted it. Three cases, and the arch
test distinguishes them: an operation whose `Security` offers a `pat` alternative carries non-empty
`x-dkp-scopes`, every member resolving in the catalogue (`getGuild` → `["roster:read"]`); an
operation in canonical §6's capability floor carries `x-dkp-pat-forbidden: true` and no scopes; an
operation that is session-only merely because no scope family covers it declares neither
(`updateGuild` — `admin.settings` is **not** in the floor, and marking it PAT-forbidden is a false
positive). Declaring the extension also retires an unverified claim: `02-api-design.md` flags scope
arrays on non-`oauth2` `Security` as of uncertain legality in OpenAPI 3.1, and if `x-dkp-scopes` is
always present that question stops mattering.

**Known and deliberate gap.** There is no auth middleware and no `authz.Check` — authentication is
ROADMAP Phase 2 deliverable 1, and no PR in this file adds it. Both operations are therefore
**served with no credential required**, while the spec declares `security: [{pat:…},{session:{}}]`.
PATCH is the product's first mutating endpoint. This ships that way, with the gap named in
`SECURITY.md` and pinned by `TestGuild_Unauthenticated_IsAKnownPhase0Gap` — a tripwire that goes red
the day auth lands, so closing it is a deliberate deletion rather than a silent discovery.
`make test-authz` and `test / authz-matrix` stay Phase 2 stubs: a matrix over zero principals is not
a weaker test, it is not a test.

**Files touched.**

```
db/schema.hcl                                  # guild singleton
db/migrations-sqlite/000002_guild.sql          # GENERATED
db/queries/guild.sql
internal/store/sqlitegen/**                    # GENERATED
internal/store/{store.go,tx.go}                # Queries growth; Tx -> func(ctx, Queries) error
internal/authz/{doc.go,catalogue.go,catalogue_test.go}
internal/guild/{doc.go,service.go,service_test.go}
internal/api/{guild.go,guild_test.go,etag.go}
internal/api/arch_test.go                      # If-Match / precondition coverage + negative fixture
test/integration/{main_test.go,guild_test.go}
openapi/openapi.json                           # GENERATED
docs/api-changelog.md                          # created; EXAMPLE_ENDPOINT.md step 8 requires it
```

**Acceptance criteria.**

- `GET /api/v1/guild` returns the singleton with a strong `ETag`, at a statement budget of **1**
  (`store.Counted(t).Budget(t, 1)`, declared after the server is constructed). The harness is
  `store.NewDB(t)` + `httptest.NewServer(api.New(...))` over a `TestMain` calling
  `store.InitTemplate(ctx, store.ApplySchema(fsys))` — there is no `testenv` package and no
  `ClientAs`, because there is no auth package and therefore no such thing as an officer.
- `PATCH` without `If-Match` returns **428** `precondition_required`. **`If-Match` must NOT carry
  `required:"true"`**: Huma v2.39.1 raises a missing required parameter as `422`
  (`huma.go:899,980`), so the check is in the handler and the 428 is explicit. A test asserts the
  status *and* the code, because a 422 would otherwise look like a passing negative test.
- `PATCH` with a stale `If-Match` returns **412** with the current representation in `meta.current`
  and the current ETag in `meta.current_etag` (canonical §7), so a bot merges in one round trip.
- `PATCH` with the current `If-Match` succeeds and returns a **different** `ETag` — the positive
  control, without which all three tests above pass against an endpoint that always fails.
- `TestArch_StateChangingOperation_RequiresIfMatch` enumerates the registry and requires an
  `If-Match` header parameter on every `PATCH` and every transition, paired with a negative fixture
  that proves it fires. `.claude/rules/api-endpoints.md` and `EXAMPLE_ENDPOINT.md:227-237` have both
  claimed this test exists since PR 4; this is where it becomes true.
- `TestCatalogue_Permissions_MatchCanonicalConventions` extracts the fenced key list from
  `docs/design/00-canonical-conventions.md` §6 and compares it to `authz.Catalogue()` element by
  element, in both directions — the same mechanism as `TestErrors_Enum_MatchesPublishedCatalogue`
  (`internal/api/errors_test.go:90`). Without it the catalogue is a second hand-maintained list,
  which canonical §6 forbids.
- `make verify-spec` passes with two real `x-dkp-permission` values (`roster.read`,
  `admin.settings`), exercising SPEC005's resolving path against the real repository for the first
  time.
- `store.Tx` takes `func(context.Context, store.Queries) error`. `internal/store/tx.go:18-21`
  assigns this change to PR 5 because PR 5 is the first caller that justifies it.

- `TestArch_ScopeCoverage_MatchesSecurity` enumerates the registry and asserts the three-case rule
  above in both directions, with a negative fixture per case. Adopting an extension without the gate
  that proves it is how the previous four documents ended up describing one that did not exist.
- `internal/authz/catalogue.go` carries `admin.security.manage`, the key canonical §6 gained in the
  documentation change that preceded this PR. Nothing in PR 5a uses it — it guards `/admin/settings`
  and `/feed-tokens`, both Phase 2 — and it ships with the rest of the list for the same reason.

**Prerequisite, not a parallel task.** Run the snippet-compile spike (U3) **before** starting 5a: two
hours, extracting the four Go fences from today's `EXAMPLE_ENDPOINT.md` to find out whether a Go
fragment in that document can reach `go build` without restructuring it. It needs no code from this
PR, and it is what tells you whether 5b is a 900-line PR or a 2,000-line one — which is worth knowing
before 5a fixes the shape the document will transcribe.

---

## PR 5b — `docs(api): EXAMPLE_ENDPOINT.md and RECIPES.md, written from PR 5a's code`

**This is the highest-leverage documentation in the project.** Every subsequent endpoint task becomes
"copy the example, change the nouns". If this PR is mediocre, the next two hundred PRs are mediocre.

**Depends on 5a being merged.** The whole value of these documents is that they are transcribed from
code that exists and is under test. Writing them first is what produced today's version, which
describes `humachi.New`, `problem.From()` and `store.Q()` — none of which exist.

**Scope.** Rewrite the two documents *from the actual code PR 5a merged* — never from memory, never
from a library's README — and install the gate that stops them rotting.

`internal/api/EXAMPLE_ENDPOINT.md` walks the resource end to end with real, copyable excerpts, one
step per artifact:

| Step | Artifact |
|---|---|
| 1 | `db/schema.hcl` entry |
| 2 | `db/queries/guild.sql` |
| 3 | `make gen` — the sqlc output and the `Queries` interface method |
| 4 | the service in `internal/guild/` |
| 5 | handler struct + `huma.Register` with `Security`, `x-dkp-permission`, `OperationID` |
| 6 | `make gen` again — the resulting `openapi/openapi.json` fragment |
| 7 | the tests, handler-level and integration |
| 8 | docs and `docs/api-changelog.md` |
| 9 | verify |

> **Corrected in PR 5b.** This file said "seven steps" against a seven-row table while
> `EXAMPLE_ENDPOINT.md:18-32` said nine against a *different* nine-row table — the two were not the
> same list with a different count. The document's nine-step version wins because it is the artefact
> an agent actually follows, and because it names the service step and the changelog step, which the
> seven-row version silently dropped. The generated TypeScript client call is **`PENDING PR 6`**
> inside step 6 rather than a step of its own; `TestDocs_NoPendingMarkers` in PR 6 removes it.

`db/RECIPES.md` keeps the recipes whose tables exist — the `GetGuild` singleton fetch and the
`dkp_meta` upsert — and **fences or cuts the nine that query `ledger_entry`, `balance_snapshot`,
`person`, `raid`, `item_fts` and friends.** The compile gate below cannot check a query against a
table that does not exist, and a recipe file whose recipes do not run is the same failure as a
document promising code. The `total()` ban stays and keeps its bold note: it returns a float and
silently defeats the centipoint invariant (canonical §1). The three dialect divergences stay and are
explicitly marked forward-looking.

**Corrections `EXAMPLE_ENDPOINT.md` needs, because PR 5a's code makes them false.** Each is
evidenced in [`phase-0-pr5-decisions.md`](phase-0-pr5-decisions.md):

- `humachi.New` → `humago.New`, and the reader never calls the adapter — `api.New(api.Config{…})`
  builds the tree (`internal/api/server.go:76`).
- `problem.From(err)` → `api.NewProblem(status, code, detail)` (`internal/api/errors.go:279`).
  There is no such function as `problem.From`, and `problem` is `internal/api/middleware`.
- `s.store.Q()` and `Tx(func(q store.Queries) error)` → the real signatures 5a ships.
- `If-Match required:"true"` → an optional tag plus an explicit 428, with the Huma 422 behaviour
  explained so the next reader does not "simplify" it back.
- Step 7 loses `testenv.New(t)` and `env.ClientAs(t, testenv.RoleOfficer)` entirely. There is no
  auth package and no such thing as an officer until ROADMAP Phase 2 deliverable 1. The honest shape
  is `store.NewDB(t)` + `httptest.NewServer(api.New(...))`, and the document shows the
  statement-budget call because that is the N+1 tripwire and it already works.
- Step 7 stops claiming a response-validation middleware runs. The validator choice is an **open
  Phase 0 item** (`docs/design/04-testing.md` §"Open item"), and `.claude/rules/api-endpoints.md`'s
  `kin-openapi` assertion was deleted for violating it.
- Step 7's PAT-parity bullet is marked Phase 2 rather than described as current practice.

**Files touched.**

```
internal/api/EXAMPLE_ENDPOINT.md
db/RECIPES.md
internal/api/docs_snippets_test.go             # TestDocs_ExampleEndpointSnippets_Compile
scripts/eval-example-endpoint.sh
.github/workflows/nightly-verify.yml
Makefile  .github/workflows/ci.yml
```

**Acceptance criteria.**

- Every code block in both documents is extracted and checked by
  `TestDocs_ExampleEndpointSnippets_Compile`: Go blocks compile, SQL blocks pass `sqlc` against the
  migration set, HCL blocks pass `atlas schema inspect`. A snippet that drifts from the code it
  documents fails CI — this is the mechanism that stops the example rotting, and without it the PR
  is worthless.
- `TestRecipes_TotalIsBanned`: a fixture query containing `total(` fails `scripts/repo-gates.sh`.
- **The eval gate.** `scripts/eval-example-endpoint.sh` starts a fresh agent session whose entire
  context is the repository plus `internal/api/EXAMPLE_ENDPOINT.md` and `db/RECIPES.md`, and hands it
  one instruction: *add `GET /api/v1/guild/settings` over a new `guild_setting(key, value_json)`
  table*. It passes when the resulting tree has `make check` green, the spec drift gate green, and a
  handler declaring `Security` and `x-dkp-permission` — **with no further guidance given at any
  point**. It runs nightly, not per-PR, because agent evals are slow and non-deterministic; a failure
  files an issue against the two documents, never against the code.

  **Settle before starting (U5 in [`phase-0-pr5-decisions.md`](phase-0-pr5-decisions.md)):** which
  runner, whose API key, what budget. If that is unsettled, ship the script as a local-only target
  and land the nightly lane with the release train in PR 7 — do not write a workflow that needs a
  secret nobody has agreed to. This is the single line item most likely to blow the estimate.

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

## PR 7 — `build: scratch image, goreleaser, release train, and the smoke gate`

**Scope.** Distribution ships in Phase 0 so every later phase produces something pullable.

The supply-chain and licence controls were originally bundled here and have been **extracted into
PR 12** (below), which has landed. They shared nothing with the release train but a phase number,
and PR 7 is already at the 2,500-line budget without them. Renovate's high-risk allowlist stays with
PR 7, because it is release policy rather than a gate.

**Files touched.**

```
Dockerfile  .dockerignore  .goreleaser.yaml
cmd/dkp/healthcheck.go
.github/workflows/{release.yml,edge.yml,ci.yml}
.github/renovate.json5
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
- Renovate is configured with an explicit high-risk allowlist that never automerges `huma`, `goose`,
  `river`, the SQLite driver, or anything under `internal/auth`'s dependency set.
- `THIRD_PARTY_NOTICES.txt` is generated from the runtime dependency graph and attached to every
  release artifact, as `NOTICE` promises. Moved here from PR 12: it is a release artifact, and there
  was no release train to attach it to before this PR.

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
- `guild.updated_at` from PR 5a now round-trips through `core.Micros` end to end — the smallest
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
.github/workflows/{ci.yml,nightly-verify.yml}  # coverage floors, 20k nightly checks
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

## PR 12 — `build(ci): dependency licence gate and govulncheck, wired into ci-required`

Out of numerical order, and landed early: extracted from PR 7 and taken on its own once
`modernc.org/sqlite` took the runtime graph from one dependency to twelve. That audit was done by
hand in PR 2 and would not have survived the next Renovate bump.

**On the number.** `ROADMAP.md` deliverable 12 and four workflow headers already called this work
"Phase 0 PR 12"; this file called it part of PR 7. Two schemes, same three items. Keeping 12 makes
`ROADMAP.md:73-74` and every workflow header correct as already written, and confines the change to
this file. The PR-level plan here is authoritative for PR *content*; ROADMAP numbers deliverables.

**Files touched.**

```
internal/licence/
Makefile  .github/actions/setup-toolchain/action.yml
.github/workflows/ci.yml
test/repo/{licence_gate_test.go,ci_required_test.go}
```

**Acceptance criteria.**

- The licence gate (`make licence-gate`, `internal/licence`) fails on any copyleft (GPL, AGPL, LGPL, EPL, CDDL, CC BY-SA),
  non-commercial (CC BY-NC/ND) or source-available (BUSL, SSPL, Elastic, FSL, PolyForm) licence, and
  on a restriction rider layered over a permissive grant (Commons Clause, the JSON licence, BSD-4's
  advertising clause). Fixture modules in `t.TempDir()` prove each fires, wired in by a filesystem
  `replace` so they resolve offline.
- **Every pattern is evaluated; there is no first-match short-circuit.** A classifier that stops at
  the first licence it recognises cannot see a rider on a permissive base, and lets a GPL module
  through if its preamble names a permissive licence. MPL-2.0's §1.12 cross-reference to the GNU
  licences is handled by removing that one sentence, not by reordering the classifier.
- The verdict is an **allowlist**: Apache-2.0, MIT, ISC, BSD, MPL-2.0, CC0-1.0, Unlicense, Zlib.
  Recognised-but-unlisted is `LIC002`, not a pass — the default is stop.
- It scopes to the **runtime** graph (`go list -deps ./...`, no `-test`), unioned across the release
  platforms. A denied licence reachable only from a `_test.go` import must pass:
  `github.com/hashicorp/golang-lru/v2` (MPL-2.0) is in `go list -m all` today solely because
  `modernc.org/libc`'s tests import it. A linux-only query would miss three modules that ship in the
  darwin and windows binaries.
- `LIC003` catches a permissively-licensed module whose `LICENSE-3RD-PARTY`/`NOTICE` declares
  embedded copyleft — the `modernc.org/libc` shape.
- The allowlist is asserted, not just the ban. A gate that fires on everything would satisfy every
  negative test and get bypassed the first time it ran.
- No vacuous pass. `go list ./...` exits zero when nothing matches, so stderr is captured separately
  and a "matched no packages" result is an error.
- `govulncheck ./...` runs as a required job, is not `continue-on-error`, and hard-fails rather than
  exiting 0 when the binary is missing.
- Both jobs are unconditional and asserted in `ci-required`'s always-on list —
  `test/repo/ci_required_test.go` fails if either is dropped from `needs:` or acquires an `if:`.
- `scripts/eqdkp-identifier-gate.sh` is **not** created. That criterion, formerly PR 7's, is already
  met by rule `AGPL001` in `scripts/repo-gates.sh`, with the allowlist it specifies. A second gate
  for the same rule is duplication, not coverage.

**Deferred, deliberately.** CI secret scanning (gitleaks), `THIRD_PARTY_NOTICES` (moved to PR 7 with
the release train), and a `verify-action-pins` that resolves each action's trailing-comment tag
against upstream — that one needs authenticated GitHub API calls from a lint job and is its own
design question. `ci.yml`'s header now says what that target does and does not do, rather than
promising the version that does not exist.

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
