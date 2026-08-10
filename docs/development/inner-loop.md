# Inner loop

**Status:** the toolchain targets exist in the `Makefile` but most call `notyet` until the code they
drive lands. Wall times below are the budgets the implementation must meet, taken from the design; CI
asserts the command table, not the timings.

The design constraint for this repository is the inner loop, not throughput. A contributor — human or
agent — must be able to write a failing test, run it, fix it, and re-run inside one attempt.

## Clone to green

```bash
git clone https://github.com/dragonkillparty/dkp
cd dkp
make setup      # once — gofumpt, goimports, golangci-lint, govulncheck, sqlc and atlas.
                #        oasdiff, vale, lychee and the pnpm dependencies arrive with the
                #        phases that need them; `make setup` prints what is still missing.
make check      # everything CI runs
```

**No Docker is required for development.** That is the payoff of SQLite: integration tests run against
a real database in `t.TempDir()`, with no container, no port allocation, no `docker compose up` and no
teardown. The only target that needs Docker is `make test-importer`, which boots real EQdkp Plus
fixture databases.

Prerequisites: Go 1.26 and Node 24 via corepack. That is the whole list.

## The commands

CI asserts that every row here is a real target. Needing a command that is not in the table means
adding the target **and** the row in the same change. The Makefile also carries plumbing targets
(`build`, `fmt`, `clean`, `verify-commands`, `verify-generated`) that the rows below invoke; those
are deliberately not listed.

| Task | Command | Budget |
|---|---|---|
| install toolchain | `make setup` | once |
| dev server — Go on `:8080`, Vite on `:5173` | `make dev` | — |
| regenerate all generated code | `make gen` | ~15 s |
| unit tests | `make test-unit` | < 5 s |
| integration tests against real SQLite | `make test` | ~30 s |
| importer suite — needs Docker | `make test-importer` | ~120 s |
| lint | `make lint` | ~20 s |
| build, vet, staticcheck, tsc | `make vet` | ~15 s |
| new migration | `make migration NAME=<snake_case>` | — |
| seed a dev guild | `make seed` | — |
| container image | `make docker` | ~90 s |
| **everything CI runs** | **`make check`** | **~60 s** |

Run `make check` before claiming a task is done.

## The loop you actually run

```bash
go build ./...                    # ~1.5 s warm
go test ./internal/ledger/...     # ~0.4 s — the package you are changing
go test ./internal/...            # ~25 s — WITH a real database
make check                        # ~60 s — before you push
```

Work at the narrowest level that proves your change, and widen only when it passes.

## What runs where

| Layer | Tool | Covers | Wall time |
|---|---|---|---|
| Unit | stdlib `testing`, table-driven | Strategy arithmetic, ledger invariants, parsers, importer transforms | < 3 s |
| Property | `testing/quick` + a seeded generator (`make test-property`) | Zero-sum conservation under random award and reversal sequences; positions stay a permutation; largest-remainder always sums to the debit | ~8 s |
| Golden file | stdlib, `-update` refused in CI | Every Project 1999 parser, one directory per format | < 1 s |
| Integration | Real SQLite in `t.TempDir()`, full server via `httptest` | Every endpoint, real migrations, real workers, real triggers | ~25 s |
| Contract | Response-validation middleware across the whole suite, plus `oasdiff` | Every request and response against the spec | folded in |
| Importer | Real EQdkp fixture databases in containers | Fingerprint, stage, load, reconcile | ~90 s |
| End to end | Playwright against the **released binary** | ~12 journeys, first run through backup and restore | ~3 min |

Deliberately absent: no database mocking, no isolated HTTP-handler unit tests, no React component
snapshots. Handlers are thin and the integration suite covers them; component snapshots are brittle
and prove nothing.

## Test infrastructure worth knowing about

| Thing | What it does to you |
|---|---|
| **Statement budget** | A `database/sql` wrapper counts statements per test against a declared budget. `GET /standings` with 200 members is budgeted at four. Introduce an N+1 and the test fails, naming the count. |
| **Ledger replay** | Rebuilds every balance from zero over a 100,000-entry synthetic ledger and asserts it matches the incrementally maintained cache. Runs in CI and nightly in production. |
| **Determinism** | With an injected clock and seeded generator, planning the same event twice must produce byte-identical proposals. Asserted by hashing. |
| **Concurrency** | Two clients bidding an account's whole balance into two simultaneous sessions must produce exactly one success. Cannot be mocked. |
| **Idempotency** | The same tick upload, bid and settle replayed 100× concurrently must produce one effect and 99 replays. |
| **Trigger assertions** | `UPDATE ledger_entry` must raise — after the *full* migration set has been applied, because table rebuilds drop triggers. |
| **Golden-file anti-tampering** | `test/golden/` and `test/fixtures/` are CODEOWNERS-protected, `-update` is refused when `CI=true`, and a test asserts the fixture count never decreases. |

That last row exists because rewriting a golden file is the fastest path to green, and an agent will
find it. A failing test is information; changing the assertion to match the code inverts the point of
the test.

## Generated code

Never hand-edit these. Change the source, run `make gen`, commit the diff.

| Generated | Source |
|---|---|
| `openapi/openapi.json` | The Go handler types, via Huma |
| `internal/store/sqlitegen/`, `internal/store/pggen/` | `db/queries/*.sql`, via sqlc |
| `db/migrations-*/` | `db/schema.hcl`, via Atlas (`make migration NAME=…` writes them; `make gen` only re-hashes and checks) |
| `web/src/api/` | `openapi/openapi.json` |
| `clients/` | `openapi/openapi.json` |

CI regenerates everything and fails on any diff, so a handler change without a spec update is not
possible.

## Editor setup

| Tool | For |
|---|---|
| `gopls` | Go. A PostToolUse hook runs `gofumpt` and `goimports` on save — do not format by hand. |
| The sqlc extension | Type-checked SQL as you write it |
| Vale | Prose linting on markdown, matching what CI runs |

## Before your first PR

- [ ] `make check` is green
- [ ] Sign off your commits: `git commit -s`. DCO, not a CLA.
- [ ] You have not edited a generated file, a golden file, or a shipped migration
- [ ] You have not copied anything from EQdkp Plus — see the licence firewall in [CONTRIBUTING.md](../../CONTRIBUTING.md)
- [ ] Any behaviour change has a docs change in the same PR

## Common first-run problems

| Symptom | Cause | Fix |
|---|---|---|
| `make setup` fails on pnpm | corepack is not enabled | `corepack enable` |
| `make gen` produces a diff you did not expect | Your generator versions differ from CI's | Re-run `make setup`; the versions are pinned |
| Integration tests are slow the first time | The Go build cache is cold | The second run is the honest number |
| `make test-importer` fails immediately | Docker is not running | It is the only target that needs it |
| A golden test fails and `-update` fixes it | You changed parser behaviour | That is the test doing its job. Decide whether the *new* output is correct before you update anything. |

## Next

- [Architecture overview](architecture-overview.md) — where code goes and which law governs it
- [The first ten PRs](first-ten-prs.md) — Phase 0, in order
- [Invariants](../concepts/invariants.md) — every rule and its mechanism
- [AGENTS.md](../../AGENTS.md) — the contract loaded before any work starts
