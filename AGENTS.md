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
| importer suite (needs Docker) | `make test-importer` | ~120 s |
| lint | `make lint` | ~20 s |
| build + vet + staticcheck + tsc | `make vet` | ~15 s |
| new migration | `make migration NAME=<snake_case>` | — |
| seed a dev guild | `make seed` | — |
| container image | `make docker` | ~90 s |
| **everything CI runs** | **`make check`** | **~60 s** |

Run `make check` before claiming a task is done. If you are told to run a command that is not in
this table, add the Makefile target *and* this row in the same change — never invent one. (The
Makefile also has a few internal helpers, such as `fmt` and `build`; CI checks that every row here
resolves to a real target, not that every target appears here.)

## Repo map — the rule is attached to the directory

- `cmd/dkp/` — the only binary. Cobra wiring only, no logic.
- `internal/api/` — the **only** tree where an HTTP route may be declared. Copy
  `internal/api/EXAMPLE_ENDPOINT.md` end to end before writing a new endpoint.
- `internal/store/` — the **only** package that may hold `*sql.DB` or call `sql.Open`. Every
  mutation goes through `store.Tx`. Query shapes live in `db/RECIPES.md`.
- `internal/ledger/` — append-only writer + invariant engine. Highest-blast-radius code in the repo.
- `internal/strategy/` — **pure** point strategies. No DB, no wall clock, no RNG of its own.
- `internal/importer/` — EQdkp Plus ETL. Two phases: stage verbatim, then transform.
- `internal/parse/` — P99 log adapters. One file + one golden dir per format. Stdlib only.
- `internal/cms/` — articles, comments, media, portal blocks. Untrusted rich text lives here.
- `db/schema.hcl` — the single source of schema truth. Atlas generates the migrations.
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
- Never swallow an error to make a test pass. Never `_ = err`.
- Log with `slog`, structured. No secrets, no PATs, no bid amounts before reveal.

## When you are uncertain

Stop and ask. Do not guess at: a schema column that doesn't exist, a P99 log line format you haven't
seen in `test/golden/`, an EQdkp table shape not in `test/fixtures/`, or what a guild's decay rule
should do.

For unverified P99 formats, add a golden fixture marked `unverified` and open an issue — do not
invent a regex and ship it. Several log formats in the design are explicitly unverified; guessing
one produces silently wrong attendance, which is worse than an error.

If two instructions conflict, the invariant wins and the conflict is a bug: say so.

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
