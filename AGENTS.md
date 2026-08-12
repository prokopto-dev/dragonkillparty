# Dragon Kill Party

DKP and guild management for Project 1999 EverQuest raiding guilds. One Go binary, SQLite,
embedded React SPA, API-first, run by volunteer officers. Apache-2.0.

**Full conventions:** `docs/design/00-canonical-conventions.md` is normative. When this file and
another document disagree, this file and that one win over the other document — and the conflict
is a bug worth reporting.

## Canonical commands — CI asserts every row is a real Makefile target

| Task | Command | Budget |
|---|---|---|
| install toolchain | `make setup` | once |
| dev server (Go :8080 + Vite :5173) | `make dev` | — |
| regenerate ALL generated code | `make gen` | ~15 s |
| unit tests | `make test-unit` | < 5 s |
| integration tests (real SQLite in `t.TempDir`) | `make test` | ~30 s |
| ledger + strategy properties (200 checks; 20 000 nightly) | `make test-property` | ~10 s |
| coverage floor for the ledger, strategy and enum catalogues | `make test-coverage-floor` | ~10 s |
| importer suite (needs Docker) | `make test-importer` | ~120 s |
| browser suite (Playwright vs the built binary) | `make test-e2e` | ~60 s |
| lint | `make lint` | ~20 s |
| build + vet + staticcheck + tsc | `make vet` | ~15 s |
| new migration | `make migration NAME=<snake_case>` | — |
| seed a dev guild | `make seed` | — |
| container image | `make docker` | ~90 s |
| inner loop — laws + linters + type checks, no tests | `make check-fast` | ~25 s |
| **everything CI runs** | **`make check`** | **~60 s** |

Run `make check` before claiming a task is done. If you are told to run a command that is not in
this table, add the Makefile target *and* this row in the same change — never invent one. (The
Makefile also has a few internal helpers, such as `fmt` and `build`; CI checks that every row here
resolves to a real target, not that every target appears here.)

**`check-fast` is for the edit-compile-lint cycle, `check` is the gate.** `check-fast` is
`lint-repo` + `lint-go` + `vet`: the four laws, the money rules, gofumpt, golangci-lint, `go vet`,
staticcheck and `tsc`. It runs no tests, no coverage floor, no licence gate and no eslint, so it
cannot tell you the change works — only that the tree is coherent. Reach for it between edits;
`make check` is still what "done" means, and the pre-push hook does not accept the faster one
instead.

The test suite **caches**: `go test`'s result cache is on for every package that cannot spawn a
subprocess, so a second `make test` over an unchanged package prints `(cached)` rather than running
it again. Packages that shell out (`test/repo`, `internal/api`, `internal/core`, `internal/licence`,
`internal/repogate`, `internal/specgate`) always re-run — the cache cannot see what a subprocess
reads, so a cached pass there would be a gate reporting green on the change it exists to catch.
`-shuffle=on` runs nightly over the whole suite rather than on every push; if you want it locally,
`DKP_TEST_SHUFFLE=on make test`.

## Repo map — the rule is attached to the directory

- `cmd/dkp/` — the only SHIPPED binary. Cobra wiring only, no logic. (`internal/licence/cmd/` and
  `internal/repogate/cmd/` below are repo tooling and never reach an operator.)
- `internal/api/` — the **only** tree where an HTTP route may be declared. Copy
  `internal/api/EXAMPLE_ENDPOINT.md` end to end before writing a new endpoint.
- `internal/store/` — the **only** package that may hold `*sql.DB` or call `sql.Open`. Every
  mutation goes through `store.Tx`. Query shapes live in `db/RECIPES.md`.
- `internal/ledger/` — append-only writer + invariant engine. Highest-blast-radius code in the repo.
- `internal/strategy/` — **pure** point strategies. No DB, no wall clock, no RNG of its own.
- `internal/importer/` — EQdkp Plus ETL. Two phases: stage verbatim, then transform.
- `internal/parse/` — P99 log adapters. One file + one golden dir per format. Stdlib only.
- `internal/cms/` — articles, comments, media, portal blocks. Untrusted rich text lives here.
- `internal/licence/` — the dependency licence gate (LIC001/002/003) and the ONE `go list`
  runtime-graph platform union, shared with `scripts/third-party-notices.sh`. Tooling, not
  product: stdlib only, and not linked into `dkp` — a test asserts the binary's package graph
  never reaches it. Adding a licence to the allowlist is a human decision.
- `internal/repogate/` — the architectural gate engine `make lint-repo` runs: the laws above, the
  money rules, the supply-chain pins and the AGPL firewall (`scripts/repo-gates.sh` is the shim
  that points it at a tree). Config-shaped rules are a typed catalogue; the Go-syntax laws are
  `go/parser` analyzers. Same terms as `internal/licence`: tooling, stdlib only, not linked into
  `dkp`. Every rule keeps a negative fixture in `test/repo/` — a gate nobody has seen go red is a
  gate nobody knows works.
- `db/schema.hcl` — the single source of schema truth. Atlas generates the migrations. The regions
  you do not edit are the enum CHECKs between the `GENERATED` markers: the `ledger_batch` pair from
  `internal/ledger/kinds`, the `audit_log` pair (`actor_kind`, `outcome`) from `internal/audit/kinds`
  and the `account` pair (`kind`, `system_key`) from `internal/account/kinds`, all written by
  `make gen` (canonical §5). A *new* string-enum CHECK goes in a catalogue too, not in a literal:
  `ENUM001` fails one written outside the markers.
- `db/embed.go` — the `go:embed` of the migration set. No logic; `//go:embed` cannot reach
  upwards, so the directive has to live beside the files.
- `openapi/openapi.json`, `internal/store/sqlitegen/`, `web/src/api/`, `clients/` — **GENERATED**.
  Never hand-edit. Change the source, run `make gen`, commit the diff.
- `docs/design/mockups/` — five HTML surfaces, ~55 screens, plus the Nocturne stylesheet. The design
  reference, vendored byte-exact. Read the screen before building it; `make mockup-site` publishes
  them.
- `test/golden/`, `test/fixtures/` — expected outputs. CODEOWNERS-protected.
- `test/repo/` — tests about the repository itself, not the product: they assert the gates in this
  file actually fire. Add one when you add a gate, not when you add a feature.

## The four laws — each enforced by a test or a lint rule, not by trust

1. Routes are declared only in `internal/api`.
2. `*sql.DB` is held only by `internal/store`.
3. `internal/strategy` is pure: no `internal/store`, no `time.Now`, no `math/rand`. The clock and a
   seeded RNG are injected; the seed is persisted into the batch.
4. `web/src` contains no `fetch`/`XMLHttpRequest` outside `web/src/api`.

## Non-negotiable invariants

- **The ledger is append-only.** Never `UPDATE` or `DELETE` a `ledger_batch` or `ledger_entry` row,
  in Go, in SQL, or in a migration. To fix a mistake, write a reversal batch with
  `reverses_batch_id` set. A DB trigger raises if you try; a test asserts the trigger fires.
  "Fixing" a balance by editing history destroys the product's entire trust argument.
- **Point arithmetic is `Centipoints` (int64) only.** No `float64`, no `float32`, no `NUMERIC`,
  no decimal strings — not in Go, not in SQL, not on the wire. Time is `Micros` (int64 Unix
  microseconds). Keys are ULIDs in TEXT.
- **Zero-sum splits use largest-remainder allocation.** Credits must sum to exactly the debit.
  Rounding each credit independently mints or destroys points.
- **There is exactly one guild per instance and no `guild_id` column.** Do not add one "for later".
  Scope comes from the request principal.
- **Every endpoint declares `Security` and `x-dkp-permission`.** No exceptions "just for this one".
- **Every mutating POST that creates domain state requires `Idempotency-Key`.** Bots retry;
  duplicated ticks and double-charged bids are the top support burden.
- **The SPA is an API client.** If a UI feature needs a new capability, add it to the public API and
  the spec. There is no back door and CI proves there isn't.
- **There is no all-powerful token.** Token minting, role edits, backup download, bulk PII read and
  import commit are session + step-up only, with no scope at all.
- **The importer's default is `--dry-run`.** Writing requires an explicit `--commit`.
- **Passwords are never migrated from EQdkp.** Usernames and emails only.

## Code style and naming

- `gofumpt` + `goimports`; a PostToolUse hook formats on save — don't format by hand. Tracked git
  hooks (installed by `make setup`) also auto-format staged files on `git commit` and block a `git
  push` that would land anything unformatted, so nothing unformatted reaches CI.
- Update a PR branch **locally** — `git merge origin/main && make fmt && git push` — not with
  GitHub's "Update branch" button. A server-side merge bypasses the commit hook, so a merge that
  stitches two edits of the same file together can land it gofumpt-dirty and fail CI; merging
  locally runs the hooks and `make fmt` before the push.
- Errors wrap with `%w` and context: `fmt.Errorf("load pool %s: %w", id, err)`. Sentinel errors in
  the owning package as `ErrNotFound`, `ErrConflict`.
- Handlers return Huma error types; they never write to `http.ResponseWriter`.
- Every operation sets an explicit `OperationID` in lowerCamelCase (`createRaidTick`). The generated
  SDK method name derives from it, so **it is public API and must never be renamed**.
- Tables singular (`ledger_entry`), columns `snake_case`, times suffixed `_at`, money suffixed `_cp`
  in SQL and `_centipoints` on the wire, ratios suffixed `_bp`.
- Enum values are lowercase `snake_case` and identical in the DB CHECK, the JSON, and the OpenAPI.
- One exported type per concept. If two files would define "the shape of a bid", one is wrong.
- Tests are table-driven, named `TestThing_Condition_Expectation`. Use `require`; `assert` is banned
  because it continues after failure and produces cascading noise.

## Error handling

- Public errors are RFC 9457 `application/problem+json` with a stable machine `code` from the closed
  enum in `internal/api/errors.go`. Adding a code is a spec change and needs a docs page.
- Never return 200 with an error body — EQdkp did that and every bot author suffered.
- Never swallow an error to make a test pass. In production code, do not call and discard: an error
  is returned, handled, or logged. `errcheck` fires on an error return that is never assigned at
  all; `_ = f()` is the deliberate, reviewable waiver, reserved for a call whose failure there is
  nothing to do about — a deferred `Close` on a read-only body, a write to a response already
  committed — and never for an error you could have returned.
- Log with `slog`, structured. No secrets, no PATs, no bid amounts before reveal.

## When you are uncertain

Stop and ask. Do not guess at: a schema column that doesn't exist, a P99 log line format you haven't
seen in `test/golden/`, an EQdkp table shape not in `test/fixtures/`, or what a guild's decay rule
should do.

For unverified P99 formats, add a golden fixture marked `unverified` and open an issue — do not
invent a regex and ship it. Several log formats in the design are explicitly unverified; guessing
one produces silently wrong attendance, which is worse than an error.

If two instructions conflict, the invariant wins and the conflict is a bug: say so.

## Out-of-scope findings: file an issue

Uncertainty about what your change should do stops you and asks. A finding you are *not* uncertain
about — real, actionable, and genuinely not this PR's job — does not stop anything: **file the issue
yourself, with `gh issue create`, and carry on.** You are empowered to do that. Do not ask
permission, do not wait for a human to notice, and do not leave it in a chat transcript nobody reads
again. A deferral, a latent bug you should not fix here, a doc conflict, an unverified behaviour, a
follow-up you can already name — each of those is an issue.

- **One issue per distinct actionable item.** Do not bundle unrelated findings into one, and do not
  split one finding across several. Say what blocks it and roughly what it costs, so triage does not
  have to re-derive that from the diff.
- **Use the existing labels** — `bug`, `documentation`, `enhancement` — and the matching form in
  `.github/ISSUE_TEMPLATE/` where one fits: `parser-bug`, `import-failure`, `parity-gap`,
  `bug_report`, `feature_request`. **Add the roadmap-phase label too**: `phase-0` … `phase-9`, one
  per `## Phase N` heading in `ROADMAP.md`. Pick the phase that would have to *ship* the fix, not
  the phase you are standing in — something needing infrastructure Phase 3 builds is `phase-3`,
  something to clear before the next boundary is the current phase. If you genuinely cannot tell,
  leave it off and say so in the body; triage adds it. `.github/labels.yml` is the whole set — a
  label that is not in that file does not exist, so do not invent one.
- **Do not expand the PR to fix it.** Keep the PR scoped and link the issue from the body's
  **Issues filed** table. A "risks and follow-ups" paragraph does not count on its own — the issue
  is the durable artefact and the PR body is only the pointer to it.
- **Filing is expected, not noise.** These get cleared before the next PR or stage boundary, so an
  issue filed today is looked at soon. Distinguish honestly between "cheap, do before the next
  boundary" and "needs infrastructure a later phase builds": labelling everything urgent is the same
  as labelling nothing urgent.

This is the unverified-P99-format rule above, generalised — a golden fixture marked `unverified`
plus an issue is the same move, and that rule still governs log formats specifically. It is not a
way to defer work that *is* in scope: the task you were given still gets finished.

## Do not

- Do not edit generated files (`openapi/openapi.json`, `internal/store/sqlitegen/`,
  `internal/store/pggen/`, `web/src/api/`, `clients/`, `db/migrations-*/`). Run `make gen`.
- Do not edit a migration that has shipped in a tagged release. Write a new one.
- Do not weaken, skip, delete, or `-update` a test to make CI green. A failing test is information;
  changing an assertion to match the code inverts the point of the test.
- Do not rewrite anything under `test/golden/` or `test/fixtures/` to go green.
- Do not add a dependency. Propose it with the reason and the licence; a human decides.
- **Do not copy code, DDL, language strings, or assets from EQdkp Plus.** It is AGPL-3.0 (its game
  modules are CC BY-NC-SA non-commercial) and this project is Apache-2.0. Reading a user's database
  at runtime is fine; transcribing their PHP is a licence violation. This applies especially when
  the task is "match EQdkp's behaviour" — that is when the temptation appears.
- Do not write raw SQL outside `db/queries/`, or call `db.Query`/`db.Exec` outside `internal/store`.
- Do not add a bespoke pagination shape, error shape, or list envelope. Use the helpers.
- Do not push to `main`, force push, push a tag, publish, deploy, or run `dkp import --commit`.
  Pushing a feature branch and opening a PR is the expected flow, not a violation — `main` is
  protected and the PR is the review.
- Do not disable a lint rule, a hook, or a CI gate to land a change.
