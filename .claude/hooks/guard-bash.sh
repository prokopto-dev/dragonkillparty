#!/usr/bin/env bash
#
# guard-bash.sh — PreToolUse on Bash.
#
# Blocks the small set of shell commands that are unrecoverable, publish something, or
# destroy data. Everything else is allowed; read-only and local-test commands are
# fast-path allowed so a normal session prompts about zero times.
#
# It scans the WHOLE command string, not a prefix, so `sh -c "git push"`, `env X=1 git push`,
# `/usr/bin/git push` and `a && git push` are all caught where a permissions.deny prefix
# rule is not. permissions.deny is the ergonomics layer; this script is the guard.
#
# Contract:  exit 0 = defer to the permission system · exit 2 + stderr = block
#            stdout JSON {permissionDecision:"allow"} = fast-path allow (read-only only)
# Budget:    ~30 ms.
# Fails open: a missing JSON parser or an unparseable payload allows the command.
#
set -euo pipefail

if [[ "${DKP_HOOKS:-on}" == "off" || "${DKP_HOOK_GUARD_BASH:-on}" == "off" ]]; then
	exit 0
fi

payload="$(cat || true)"

if command -v jq >/dev/null 2>&1; then
	JSON_TOOL=jq
elif command -v python3 >/dev/null 2>&1; then
	JSON_TOOL=python3
else
	printf 'guard-bash.sh: neither jq nor python3 found; shell commands are UNGUARDED.\n' >&2
	exit 0
fi

hook_field() {
	case "$JSON_TOOL" in
	jq) printf '%s' "$payload" | jq -r "$1 // empty" 2>/dev/null || true ;;
	python3)
		printf '%s' "$payload" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for k in sys.argv[1].strip(".").split("."):
    d = d.get(k) if isinstance(d, dict) else None
    if d is None:
        break
sys.stdout.write(d if isinstance(d, str) else "")
' "$1" 2>/dev/null || true
		;;
	esac
}

cmd="$(hook_field .tool_input.command)"
if [[ -z "$cmd" ]]; then
	exit 0
fi

# Single line, single-spaced, for regex matching.
c="$(printf '%s' "$cmd" | tr '\n\t' '  ' | tr -s ' ')"

root="${CLAUDE_PROJECT_DIR:-}"
if [[ -z "$root" ]]; then
	root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi

m() { printf '%s\n' "$c" | grep -Eq "$1"; }
mi() { printf '%s\n' "$c" | grep -Eqi "$1"; }

deny() {
	printf 'BLOCKED by .claude/hooks/guard-bash.sh\n\n%s\n' "$1" >&2
	exit 2
}

# Word boundaries. `.` and `/` are allowed before a command name so that /usr/bin/git and
# ./bin/dkp still match; alnum, _ and - are not, so `mygit` and `not-git` do not. The end
# boundary must accept a quote, so that sh -c "git push" is caught too.
w='(^|[^[:alnum:]_-])'
e='([^[:alnum:]_-]|$)'

# ---------------------------------------------------------------------------
# 1. Publishing history. There is no undo for a push and no undo for a tag
#    someone has already fetched.
# ---------------------------------------------------------------------------
# A blanket push ban blocks the very flow this repo requires — branch, push, PR, review — and so
# makes an agent unable to propose a change at all. What actually needs guarding is history that
# cannot be undone: a push to a protected branch, a force push, and a tag push. Pushing a feature
# branch is proposing, not publishing; `main` is protected server-side and the PR is the review.
if m "${w}git( +-[^ ]+)* +push${e}"; then
	if m ' (-f|--force|--force-with-lease|--force-if-includes)($|[^[:alnum:]_-])'; then
		deny "Force push rewrites history someone may already have fetched. There is no undo.

If a branch needs reshaping, make a new commit or a new branch and let the maintainer decide."
	fi

	if m ' (--tags|--follow-tags)($|[^[:alnum:]_-])' || m 'push +[^ ]+ +(refs/tags/|v[0-9])'; then
		deny "Pushing a tag cuts a release: tags drive the release workflow, the container tags and
the reference database. That is a human action taken from a signed tag.

AGENTS.md: \"Do not git push [to main], tag, publish, deploy, or run dkp import --commit.\""
	fi

	# Pushing main/master directly, or setting it as the upstream target.
	if m 'push +[^ ]+ +(main|master)($|[^[:alnum:]_/-])' \
		|| m 'push +[^ ]+ +HEAD:(refs/heads/)?(main|master)($|[^[:alnum:]_/-])' \
		|| m 'push( +-[^ ]+)* +(origin +)?(main|master)($|[^[:alnum:]_/-])'; then
		deny "Direct push to main. main is protected and every change goes through a PR.

Push a branch instead, then open a PR:
  git checkout -b <type>/<slug> && git push -u origin <type>/<slug> && gh pr create --fill"
	fi
fi

if m "${w}git( +-[^ ]+)* +tag${e}"; then
	if ! m 'tag +(-l|--list|-n[0-9]*|--contains|--points-at|--sort|--merged)'; then
		deny "git tag creates or deletes a release marker. Tags drive the release workflow, the
version stamped into the binary, and db/migrations-sqlite/SHIPPED.lock.

Listing is fine (git tag -l). Creating is not — ask the maintainer."
	fi
fi

if m "${w}git( +-[^ ]+)* +commit${e}" && m "(^| )--amend${e}"; then
	head_sha="$(git -C "$root" rev-parse HEAD 2>/dev/null || true)"
	if [[ -n "$head_sha" ]] && [[ -n "$(git -C "$root" branch -r --contains "$head_sha" 2>/dev/null | head -n 1)" ]]; then
		deny "HEAD is already on a remote branch, so --amend would rewrite published history and
break every clone and every open PR.

Write a new commit instead. Amending an unpushed commit is fine and is not blocked."
	fi
fi

# ---------------------------------------------------------------------------
# 2. Publishing or deploying anything.
# ---------------------------------------------------------------------------
if m "${w}(gh +release|gh +pr +merge|gh +workflow +run|gh +secret|goreleaser +release|cosign|docker +push|docker +login|npm +publish|pnpm +publish|yarn +publish|kubectl +apply|helm +(install|upgrade)|terraform +apply|(fly(ctl)?|vercel|netlify|railway|serverless|wrangler) +(deploy|up|publish)|ansible-playbook)${e}"; then
	deny "This publishes or deploys. Releases are cut by the release workflow from a signed tag,
by a human, never from an agent session.

If the change is ready, say so and stop."
fi

# ---------------------------------------------------------------------------
# 3. dkp import --commit. The importer's default is --dry-run for a reason:
#    a bad commit writes thousands of ledger rows that cannot be deleted.
# ---------------------------------------------------------------------------
if m "${w}dkp${e}" && m "(^| )import${e}" && m "(^| )--commit${e}"; then
	deny "dkp import --commit writes to the ledger, and the ledger is append-only: a bad import
can only be corrected by a reversal batch, never removed.

Run the import WITHOUT --commit (the default is --dry-run), show the reconciliation
report, and let an officer commit it. Committing an import is session + step-up only."
fi

# ---------------------------------------------------------------------------
# 4. Destructive database commands.
#    SQL patterns are gated on an actual DB client being invoked, so that
#    `rg 'DELETE FROM'` searching the codebase is not blocked.
# ---------------------------------------------------------------------------
if m "${w}(sqlite3|psql|mysql|mariadb|litecli|usql)${e}"; then
	if mi '(drop +(table|database|schema|index|view|trigger)|truncate +(table +)?[a-z_]|delete +from|update +ledger_)'; then
		deny "Destructive SQL through a database client.

The ledger is append-only, enforced by a BEFORE UPDATE OR DELETE trigger that
RAISE(ABORT)s — and an integration test asserts the trigger fires. Reaching around
that with a client is the one way to actually destroy guild history.

Corrections are REVERSAL BATCHES (a new batch with reverses_batch_id set), never
edits. Schema changes go through db/schema.hcl and \`make migration\`."
	fi
fi

if m "${w}(dropdb|mysqladmin +drop|atlas +schema +apply|atlas +migrate +apply|atlas +migrate +hash +--force|goose +(down|down-to|reset|redo))${e}"; then
	deny "This drops, rewrites, or force-applies schema state.

Migrations are applied by the binary at boot, in CI, or by an operator — not from an
agent session. \`make migration NAME=<snake_case>\` writes the file; nothing here
applies it."
fi

if m "${w}rm( +-[a-zA-Z]+)* +[^ ]*(\.db|\.db-wal|\.db-shm|dkp-data)"; then
	deny "This deletes a live database file. Guild point history has no backup you can assume
exists.

For a scratch database use a temp directory — integration tests use t.TempDir()
precisely so nothing points at a real file."
fi

# ---------------------------------------------------------------------------
# 5. Repo invariants that are cheap here and expensive to catch in review.
#    AGENTS.md: do not weaken a test; do not rewrite golden files to go green.
# ---------------------------------------------------------------------------
if m "${w}go +test${e}" && m "(^| )-+update([ =\"']|$)"; then
	deny "go test -update rewrites golden files so the assertion matches whatever the code now
does. That inverts the point of the test.

The golden harness also refuses -update when CI=true. If the expected output really
changed, edit the golden file deliberately and say why in the commit message."
fi

if m "${w}rm( +-[a-zA-Z]+)* +[^ ]*(test/golden|test/fixtures|\.git( |/|$))"; then
	deny "test/golden/ and test/fixtures/ are expected outputs and are CODEOWNERS-protected; a
test asserts the fixture count never decreases. Deleting them is not a fix."
fi

# ---------------------------------------------------------------------------
# 6. Fast-path allow for read-only and local-test commands.
#
#    Only fires when the command is plainly composed: no command substitution, no
#    redirection, no eval/sh -c/xargs/sudo, no backgrounding. Every segment must match
#    the allowlist. Anything else falls through to the normal permission flow, which is
#    where permissions.allow in .claude/settings.json does the same job less precisely.
# ---------------------------------------------------------------------------
if m '(\$\(|`|>|<|(^|[^&])&([^&]|$)|(^| )eval |(^| )(sh|bash|zsh) -c |(^| )xargs |(^| )sudo |(^| )nohup )'; then
	exit 0
fi

allow_re='^(ls|pwd|echo|printf|cat|head|tail|wc|sort|uniq|cut|tr|basename|dirname|realpath|file|stat|find|rg|grep|fd|jq|yq|tree|which|type|date|true|test|column|diff|comm|shasum|sha256sum)( |$)'
allow_re="$allow_re"'|^git (status|diff|log|show|branch|rev-parse|blame|ls-files|describe|shortlog|grep|remote -v|stash list|worktree list|tag -l|tag --list|config --get)( |$)'
allow_re="$allow_re"'|^go (build|vet|list|doc|version|env|fmt)( |$)'
allow_re="$allow_re"'|^go test( |$)|^go mod (verify|graph|why|download)( |$)'
allow_re="$allow_re"'|^(gofumpt|goimports|staticcheck|oasdiff|gitleaks)( |$)'
allow_re="$allow_re"'|^golangci-lint (run|version)( |$)'
allow_re="$allow_re"'|^sqlc (generate|vet|diff|version)( |$)'
allow_re="$allow_re"'|^atlas (migrate (diff|lint|validate|status)|schema inspect|version)( |$)'
allow_re="$allow_re"'|^goose (status|version)( |$)'
allow_re="$allow_re"'|^make (help|gen|test-unit|test|test-importer|lint|vet|build|fmt|clean|check|verify-commands|verify-generated)( |$)'
allow_re="$allow_re"'|^pnpm (install --frozen-lockfile|run |exec )'
allow_re="$allow_re"'|^docker build( |$)'

segments="$(printf '%s\n' "$c" | awk '{ gsub(/&&|[;|]/, "\n"); print }')"

all_readonly=1
while IFS= read -r seg; do
	seg="${seg#"${seg%%[![:space:]]*}"}"
	seg="${seg%"${seg##*[![:space:]]}"}"
	if [[ -z "$seg" ]]; then
		continue
	fi
	if ! printf '%s\n' "$seg" | grep -Eq "$allow_re"; then
		all_readonly=0
		break
	fi
done <<<"$segments"

if [[ "$all_readonly" -eq 1 ]]; then
	printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"read-only or local-test command, allowlisted in guard-bash.sh"}}\n'
	exit 0
fi

exit 0
