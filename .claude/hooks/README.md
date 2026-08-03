# Claude Code hooks

Four shell scripts, wired in `.claude/settings.json`, that run around every tool call an agent
makes in this repo. They exist to make the two most expensive mistakes — hand-editing a generated
file and destroying ledger history — structurally impossible rather than merely discouraged.

## Design rules

| Rule | Consequence here |
|---|---|
| Format synchronously, fast | `format-go.sh` runs `goimports` + `gofumpt` on one file, ~40 ms |
| Lint asynchronously, slow | Nothing lints on save. Lint is `make lint`; the gate is `make check` |
| Block only what is unrecoverable | Four block categories, listed below. Everything else is allowed or asks |
| Degrade, never fail closed | A missing `gofumpt` or `gitleaks` warns once and allows |

A `PostToolUse` hook that takes ten seconds turns every edit into a ten-second stall and destroys
the ~25 s integration test loop this architecture exists to protect. Every hook here is under ~1 s
except the commit-time secret scan.

## What is wired

| Event | Matcher | Script | Budget | Blocks? |
|---|---|---|---|---|
| `PreToolUse` | `Edit\|Write` | `guard-protected-paths.sh` | ~20 ms | yes — generated files, shipped migrations, dependency manifests |
| `PreToolUse` | `Bash` | `guard-bash.sh` | ~30 ms | yes — push/tag/publish/deploy, `dkp import --commit`, destructive DB |
| `PreToolUse` | `Bash` | `scan-secrets.sh` | ~15 ms, ~200 ms on a commit | yes — a secret in the diff |
| `PostToolUse` | `Edit\|Write` | `format-go.sh` | ~40 ms | never |

Claude Code matches hooks on the **tool name**, not on the command string, so `scan-secrets.sh` is
registered against all of `Bash` and decides for itself whether the command is a `git commit`. That
decision costs one process and one JSON parse; the scan itself only ever runs at commit time.

## guard-protected-paths.sh

Reads `.tool_input.file_path` from stdin and matches it against the repo-relative path.

| Path | Decision | Reason given to the agent |
|---|---|---|
| `db/migrations-*/…` whose basename is in `db/migrations-sqlite/SHIPPED.lock` | **deny** | It shipped in a tagged release; write a new migration |
| `openapi/openapi.json` | **deny** | Huma derives it from the Go types in `internal/api/` |
| `internal/store/sqlitegen/**`, `internal/store/pggen/**` | **deny** | sqlc output; edit `db/queries/*.sql` |
| `web/src/api/**`, `clients/ts/**`, `clients/python/**` | **deny** | Generated from `openapi/openapi.json` |
| `web/dist/**` | **deny** | Vite build output |
| `db/migrations-*/…` (any other) | **deny** | Atlas generates these; use `make migration NAME=…` |
| `go.mod`, `go.sum`, `web/pnpm-lock.yaml`, `web/package.json` | **deny** | A dependency is a human decision (AGENTS.md) |
| `test/golden/**`, `test/fixtures/**` | **ask** | Expected outputs; rewriting one to go green inverts the test |

The `ask` decision is emitted as `{"hookSpecificOutput":{"permissionDecision":"ask",…}}` on stdout
with exit 0, so the reason is shown and a human chooses.

**Other mechanisms covering the same ground:** `permissions.deny` in `.claude/settings.json`
(prefix layer), CI regenerate-and-diff, the `SHIPPED.lock` hash recomputation in CI, and CODEOWNERS
on `test/golden/` and `test/fixtures/`. This hook is the fastest of the four, not the only one.

## guard-bash.sh

Scans the **whole** command string, not a prefix, so `sh -c "git push"`, `env X=1 git push`,
`/usr/bin/git push` and `make check && git push` are all caught. A `permissions.deny` prefix rule
catches none of those — that list is the ergonomics layer, this script is the guard.

Blocks:

| Category | Examples |
|---|---|
| Publishing history | `git push` (incl. `--force`, `--force-with-lease`), `git tag` (listing forms are fine), `git commit --amend` when `HEAD` is already on a remote branch |
| Publishing or deploying | `gh release`, `gh pr merge`, `gh workflow run`, `docker push`, `goreleaser release`, `cosign`, `npm/pnpm/yarn publish`, `kubectl apply`, `helm install/upgrade`, `terraform apply`, `fly/vercel/netlify/railway/wrangler deploy` |
| Ledger writes | `dkp import --commit` — the importer's default is `--dry-run`, and a bad import can only be corrected by a reversal batch |
| Destructive database | `DROP TABLE/DATABASE/…`, `TRUNCATE`, `DELETE FROM`, `UPDATE ledger_*` **when a DB client is being invoked**; `dropdb`, `atlas schema apply`, `atlas migrate apply`, `goose down/reset/redo`, `rm` of a `*.db` or `dkp-data/` path |
| Repo invariants | `go test … -update` (rewrites golden files), `rm -rf` of `test/golden/`, `test/fixtures/` or `.git` |

The SQL patterns are gated on an actual database client (`sqlite3`, `psql`, `mysql`, …) appearing
in the command, so `rg 'DELETE FROM' db/queries` is not blocked. That gate is deliberate: a guard
with false positives gets switched off, which is worse than a guard with gaps.

Allows, without a prompt: a command composed only of read-only and local-test segments —
`git status`, `git diff`, `rg`, `jq`, `go build/test/vet/list/doc`, `gofumpt`, `golangci-lint run`,
`sqlc generate`, `atlas migrate diff`, `goose status`, `pnpm run`/`pnpm exec`, `docker build`, and
the `make` targets from the AGENTS.md command table. The fast path only fires when the command has
no command substitution, no redirection, no `eval`/`sh -c`/`xargs`/`sudo`, and no backgrounding.
Anything else falls through to the normal permission flow.

## scan-secrets.sh

Runs only on `git commit`. Scans the staged diff, or `git diff HEAD` when the command carries
`-a`/`--all`, because `git commit -a` stages tracked files at commit time and the staged diff would
miss them.

1. If `gitleaks` is installed, it runs it. Version spellings differ (`protect` became `git` in
   8.19, `detect --no-git` became `dir`), so the script probes both with `--exit-code 7`, which
   separates "found leaks" from "did not understand that subcommand".
2. If `gitleaks` cannot run, it falls back to a deliberately narrow built-in pattern set: private
   key blocks, `dkp_pat_…`, AWS access key ids, GitHub tokens, Slack tokens, `sk-…` secrets, and
   Discord bot tokens. It prints a note saying the scan was the fallback.

The hook **never prints the matched value**, only the pattern name — consistent with the `slog`
rule that secrets never reach a log. CI runs gitleaks over the full history regardless, so a
missing local binary is a gap in speed of feedback, not in coverage.

## format-go.sh

`goimports -w -local <module>` then `gofumpt -w`, on the one file that was just written, and only
if it ends in `.go`. Skips `internal/store/sqlitegen/`, `internal/store/pggen/`, `vendor/`,
`*_generated.go` and `*.pb.go`.

`gofumpt` refuses to write a file it cannot parse, so a mid-edit syntax error is a no-op rather
than a corruption. If either tool is missing the hook says so **once per session** (keyed on
`session_id`) and exits 1, which is a non-blocking error: the message reaches you, the edit stands,
the session continues. A one-line nag on every edit is how a useful hook gets deleted.

## Exit-code contract

| Exit | `PreToolUse` | `PostToolUse` |
|---|---|---|
| 0 | Allow. stdout may carry a `hookSpecificOutput` JSON decision (`allow` / `ask`) | Done |
| 2 | **Block.** stderr is fed back to the model as the reason | stderr fed back to the model |
| other | Non-blocking error; stderr is shown to you, execution continues | same |

All four scripts use `set -euo pipefail` and fail **open**: a missing `jq` *and* `python3`, an
unparseable payload, or an internal error allows the tool call rather than wedging the session.
Each prints a one-line warning to stderr when it does that.

## Testing locally

Hooks read the tool input as JSON on stdin, so you test one by piping it a payload:

```bash
# should exit 2 — generated file
echo '{"tool_name":"Write","tool_input":{"file_path":"openapi/openapi.json"}}' \
  | .claude/hooks/guard-protected-paths.sh; echo "exit=$?"

# should exit 0 — ordinary source
echo '{"tool_name":"Edit","tool_input":{"file_path":"internal/ledger/writer.go"}}' \
  | .claude/hooks/guard-protected-paths.sh; echo "exit=$?"

# should exit 0 and print {"hookSpecificOutput":…"ask"…}
echo '{"tool_name":"Edit","tool_input":{"file_path":"test/golden/raidroster/basic.json"}}' \
  | .claude/hooks/guard-protected-paths.sh; echo "exit=$?"

# should exit 2 — evasion via sh -c is caught
echo '{"tool_name":"Bash","tool_input":{"command":"sh -c \"git push\""}}' \
  | .claude/hooks/guard-bash.sh; echo "exit=$?"

# should exit 0 — searching for SQL text is not a database command
echo '{"tool_name":"Bash","tool_input":{"command":"rg \"DELETE FROM\" db/queries"}}' \
  | .claude/hooks/guard-bash.sh; echo "exit=$?"

# should exit 0 immediately — not a commit
echo '{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}' \
  | .claude/hooks/scan-secrets.sh; echo "exit=$?"
```

Set `CLAUDE_PROJECT_DIR` when you test from anywhere other than the repo root; otherwise the
scripts fall back to `git rev-parse --show-toplevel`.

Run these in a **plain terminal**, not from inside a Claude Code session. `guard-bash.sh` scans the
whole command string, so a test payload containing the literal text `git push` is itself blocked —
correctly, since that is the same reasoning that catches `sh -c "git push"`. Put the cases in a
script file and run `bash that-file.sh` if you want them inside a session.

To see what Claude Code actually passes and which hooks fire, run `claude --debug` and use
`/hooks` to inspect the loaded configuration.

## Turning one off

Every script honours two environment variables. Export them in **your shell**, not in a committed
file:

| Variable | Effect |
|---|---|
| `DKP_HOOKS=off` | Disables all four |
| `DKP_HOOK_GUARD_PATHS=off` | Disables `guard-protected-paths.sh` |
| `DKP_HOOK_GUARD_BASH=off` | Disables `guard-bash.sh` |
| `DKP_HOOK_SECRETS=off` | Disables `scan-secrets.sh` |
| `DKP_HOOK_FORMAT_GO=off` | Disables `format-go.sh` |

```bash
DKP_HOOK_GUARD_BASH=off claude   # one session, while you debug the hook itself
```

These exist for **debugging the hook**, not for getting past it. If a hook blocked something it
should not have, the fix is a patch to the pattern with a test in this file, not a session with the
guard off — and AGENTS.md is explicit: *do not disable a lint rule, a hook, or a CI gate to land a
change.* A hook that people routinely disable is a hook with a false-positive bug; report it.

Two related warnings:

- **Never run this repo with `--dangerously-skip-permissions`.** It makes everything in
  `.claude/settings.json` worth exactly nothing. The allowlist is generous specifically so nobody
  reaches for that flag.
- `permissions.deny` entries are **prefix matches and are trivially evaded** (`sh -c '…'`, `eval`,
  a shell alias). They are ergonomics. `guard-bash.sh`, which parses the full command, is the
  actual guard — and neither is a security boundary against a determined process.

## Known gaps

| Gap | Where it is covered instead |
|---|---|
| Nothing builds, vets or type-checks on `Stop` | `make vet`, `make check`, and CI. A `Stop` hook doing regenerate-and-diff is a reasonable future addition |
| Nothing lints on save | `make lint` |
| No `SessionStart` context injection | `CLAUDE.md`, `AGENTS.md`, and `.claude/rules/` |
| The built-in secret patterns are narrow | Install `gitleaks` (`make setup`); CI runs it either way |
| Path matching is textual — a path with `..` in it is not resolved | `permissions.deny` globs, plus CI regenerate-and-diff |
| `guard-bash.sh` matches text, so a command that merely *mentions* a blocked phrase is blocked | Accepted. It is what catches `sh -c "git push"`; see the note under Testing locally |
| With `gitleaks` installed and `git commit -a`, the diff is scanned as a file rather than as a repo | Stage explicitly (`git add`, then `git commit`) for the stronger path |
