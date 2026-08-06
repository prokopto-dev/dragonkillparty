# Contributing

Read [AGENTS.md](AGENTS.md) first — it is the contract every change is held to, for humans and
agents alike. This page is the process around it: how to get a branch merged.

> **Pre-1.0.** There is no working software in this repository yet. Most `make` targets print
> "not yet implemented" and the roadmap phase that fills them in. That is expected; `make check`
> is still green and still the gate. See [ROADMAP.md](ROADMAP.md).

## The short path

```bash
git clone https://github.com/dragonkillparty/dkp && cd dkp
make setup                       # once
git switch -c fix/tick-dedup     # branch off main
# ... change code AND its docs and its tests ...
make check                       # the gate. Run it before you open the PR.
git commit -s                    # -s is not optional, see DCO below
gh pr create --draft             # open as a draft; mark ready when CI is green
```

## Developer Certificate of Origin

There is **no CLA**. Contributions are under the [DCO](https://developercertificate.org/): you
certify you wrote the change, or have the right to submit it under Apache-2.0.

| Situation | Fix |
|---|---|
| One commit | `git commit -s` (or `git commit --amend -s` after the fact) |
| A whole branch missing sign-off | `git rebase --signoff origin/main && git push --force-with-lease` |
| Committing as someone else | You cannot. Sign off with your own name and real email. |

**Enforced by:** the DCO GitHub App, a required status check named `DCO`. `lefthook.yml` installs a
`prepare-commit-msg` hook so a local commit picks up the trailer automatically.

The consequence is real and we accept it knowingly: the project **cannot be relicensed** without
contacting every contributor.

## Branching and merging

Trunk-based. Branch off `main`, keep it short-lived, squash-merge, delete the branch.

| Rule | Mechanism |
|---|---|
| No direct pushes to `main`, for anyone, including admins | Branch protection with "do not allow bypassing" |
| Linear history — squash merge only | Merge commits and rebase-merge disabled at the repo level |
| One approving review; stale approvals dismissed on push | Branch protection |
| Owned paths need their owner | `.github/CODEOWNERS` — `internal/ledger/`, `internal/strategy/`, `db/schema.hcl`, `openapi/openapi.json`, `test/golden/`, `test/fixtures/` |
| Expensive checks run once per merge, not once per push | Merge queue (`merge_group` jobs: arm64 image, full cross-browser e2e, upgrade-from-last-release) |

Today the CODEOWNERS teams contain the same small group of people. The value is the notification and
the pause, not the gatekeeping.

**Conventional commits are enforced on the PR title only**, because squash-merge makes the PR title
the commit subject and that is what release-please parses. Your individual WIP commits can say
whatever you like.

```
<type>(<scope>): <summary>          feat(ledger): post decay as an explicit batch
```

Types: `feat` `fix` `perf` `refactor` `docs` `test` `build` `ci` `chore` `revert`. Scope is a
top-level package (`ledger`, `strategy`, `api`, `importer`, `parse`, `store`, `cms`, `web`, `docs`).
Put `BREAKING CHANGE:` in the PR **body** — it becomes the squash commit body and drives the
release notes. **Enforced by:** `pr-title-lint.yml`.

## The licence firewall — read this before you "match EQdkp's behaviour"

EQdkp Plus core is **AGPL-3.0**. Its game modules and its raidlog XSD are **CC BY-NC-SA 3.0**
(non-commercial). This project is **Apache-2.0**. Those licences are incompatible in the direction
that matters: AGPL code cannot be relicensed into an Apache-2.0 project, and non-commercial terms
would poison a project that guilds and hosts are meant to be able to run however they like.

| Allowed | Not allowed |
|---|---|
| Reading a user's own EQdkp Plus database at runtime — that creates no derivative work | Copying their PHP, in whole or in part, in any language |
| Observing external behaviour and reimplementing it from a written description | Copying their DDL text, `CREATE TABLE` bodies, or column comments |
| Naming their tables and columns in the importer's mapping tables | Copying their language strings, help text, error messages, or templates |
| Documenting what their software does | Copying their icons, CSS, images, or theme assets |

The identifiers `pdh_`, `gen_class`, `plus_exchange`, and `__multidkp2event` may appear in **exactly
two places**: `internal/importer/legacy_names.go` and `internal/api/compat/`.

**Enforced by:** the `lint / repo` CI job greps for those identifiers everywhere else and fails the
build; `security / licenses` fails on any GPL/AGPL runtime dependency; `.github/CODEOWNERS` puts
`LICENSE`, `NOTICE`, and `TRADEMARK.md` behind a named reviewer.

The temptation appears precisely when a task says "match EQdkp's behaviour" or "port their
calculation". If you find yourself with their source open in another tab, close it and write down
the behaviour in prose instead. See [ADR-0010](docs/adr/0010-agpl-clean-room-firewall.md).

## Commands

These are the only ones you are asked to run. CI asserts that every row here is a real Makefile
target — needing a command that is not in the table means adding the target *and* the row in the
same change. (The Makefile also holds plumbing targets such as `build` and `fmt`, invoked by the
rows below rather than by you.)

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

**Inner loop.** Write the failing test first, at the highest level that can express the bug. For
anything touching the database that is an integration test — it costs milliseconds, not seconds, and
`go test ./internal/ledger/...` is under a second. **Integration tests are cheap here and mocks are
banned**: a mocked store does not fire the append-only trigger, which is the thing you are actually
testing. Run `make check` once, before you open the PR.

`make test-importer` needs Docker and real EQdkp Plus fixtures. It is excluded from `make check`
because 120 seconds does not belong in an inner loop; CI runs it whenever the importer, the schema,
or the fixtures change.

## What CI runs

Branch protection requires exactly **two** checks: `ci-required` (an aggregate gate over every job)
and `DCO`. Everything else reports into `ci-required`.

| Group | Jobs |
|---|---|
| `gen` | `verify-generated` — `make gen` then `git diff --exit-code`. Runs always. |
| `lint` | `go` (gofumpt, vet, golangci-lint, staticcheck) · `web` (eslint, prettier, `tsc --noEmit`) · `repo` (grep gates + the licence firewall) |
| `test` | `go-unit` (`-race -shuffle=on`) · `go-integration` · `contract` (oasdiff breaking-change, `operationId` set diff, SDK regen diff) · `migrations` · `importer` · `e2e` |
| `build` | `binary` (pnpm build → `go build`, uploads the artifact every other job reuses) · `image` |
| `security` | `govulncheck` (`govulncheck ./...`, reachable vulnerabilities) · `licences` (GPL/AGPL/LGPL/CC BY-NC in the runtime module graph). Both unconditional and required. `secrets` (gitleaks) is not wired up yet — see SECURITY.md |
| `docs` | link resolution, Pages build, executable fenced blocks |
| `advisory` | image CVEs, bundle size, image size, operation coverage |

**The budget is an SLO, not an aspiration:** a non-draft PR reaches `ci-required` green in **≤ 6 min
p50, ≤ 10 min p95**, push-to-green. No single job may exceed 5 minutes; one that does gets sharded
or moved to the nightly run. `ci-budget.yml` measures the last 200 runs weekly and files an issue
when the target is breached.

Two things follow from that budget, and both are your side of the deal:

- **Open PRs as drafts.** Drafts run lint plus unit tests only, in about 90 seconds. Mark ready when
  it is green locally. Pushing six times in four minutes to a ready PR burns the whole fan-out each
  time.
- **Superseded runs are cancelled** on pull requests, never on `main`, tags, or the merge queue.

## Docs change in the same PR as the behaviour

A PR that changes behaviour and leaves the docs for later is not finished, and the follow-up PR
never comes. Docs live in `docs/` in this repository, in the same commit as the code.

| If you change | Update |
|---|---|
| An endpoint, a field, an error code | `openapi/` via `make gen`, plus the relevant `docs/api/` page and `docs/api-changelog.md` |
| A point strategy | `docs/reference/strategies/<id>.md` and a fixture |
| A config key or a CLI flag | Regenerate the reference page — it is generated from the struct and diff-gated |
| A permission or a PAT scope | `internal/authz/catalogue.go` only; `docs/reference/permissions.md` is generated |
| A log format | `docs/reference/log-formats.md` and a golden directory under `test/golden/` |
| Anything an officer sees | The guide that covers it in `docs/guides/` |

**Enforced by:** `docs / build` resolves every internal link and executes fenced `dkp:exec` blocks;
`verify-generated` fails if a generated reference page drifts from its source.

## Do not weaken a test to go green

A failing test is information. Changing the assertion to match the code inverts the entire point of
having written it, and on a ledger that is somebody's guild losing points they earned.

Never, in a PR whose purpose is something else: delete or skip a test, add `cmpopts.Ignore*` to make
a diff pass, loosen `Equal` to `Contains`/`NotNil`, shrink a table-test case list, raise a coverage
or performance budget, rewrite anything under `test/golden/` or `test/fixtures/`, or edit
`.github/workflows/` to make a job green.

If a test is genuinely wrong, **say so in the PR body, say why, and put the change in its own commit
prefixed `test-relax:`** so `git log --grep '^test-relax'` stays a two-second audit of every
assertion ever loosened. That is allowed. Quietly loosening it is not.

**Enforced by:** a CI analyser diffs `**/*_test.go` and `test/**` against `main` and posts the
before/after assertions side by side, requiring CODEOWNERS review on a hit; committed ratchets for
coverage, budgets, fixture count and golden-file count that a test asserts only ever move one way;
golden `-update` is refused when `CI=true`; and the guardrails that matter most — the append-only
trigger, `-race`, `-shuffle`, codegen drift, the licence gate — cannot be weakened from a `_test.go`
file at all.

## Proposing a dependency

Do not add one in the PR that needs it. Open an issue first; a human decides.

| State | Why it is asked |
|---|---|
| What it does, and the stdlib or existing dependency you tried first | Most proposals die here, correctly |
| Licence | Apache-2.0, MIT, BSD, ISC, MPL-2.0 are fine. **GPL/AGPL runtime dependencies fail `security / licenses`** and there is no exception. |
| Transitive dependency count and module size | It ends up in a static binary a guild officer downloads |
| Maintenance signals — last release, open issue age, bus factor | |
| What happens when it is abandoned | If the answer is "we vendor 200 lines", vendor 200 lines now instead |

Updates are Renovate's job, grouped and rate-limited. There is no `dependabot.yml`; running both
produces duplicate PRs and duplicate review load.

## Reporting things

| | |
|---|---|
| Bug, parser bug, parity gap, import failure | `.github/ISSUE_TEMPLATE/` has a form for each — use them; the fields are the ones we would ask for anyway |
| A P99 log format we get wrong | Attach a redacted log slice. Do **not** guess a regex and ship it — add a golden fixture marked `unverified` and open an issue. Silently wrong attendance is worse than an error. |
| A security vulnerability | **Not** a public issue. [SECURITY.md](SECURITY.md). |
| Conduct | [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) |

## Agent-authored changes

Much of this codebase is written by AI agents under human review. If a PR is agent-authored, say so
in the body. Agents follow [AGENTS.md](AGENTS.md) and the path-scoped rules in `.claude/rules/`; the
same review bar applies either way, and "the agent wrote it" is not a defence for a change nobody
understood.

## When you are uncertain

Stop and ask. Do not guess at a schema column that does not exist, a P99 log line you have not seen
in `test/golden/`, an EQdkp table shape not in `test/fixtures/`, or what a guild's decay rule should
do. If two instructions conflict, **the invariant wins and the conflict is a bug** — say so in the
issue or the PR.
