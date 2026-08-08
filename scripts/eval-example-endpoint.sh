#!/usr/bin/env bash
# eval-example-endpoint.sh — the agent-eval for the two worked-example documents.
#
# It is the ultimate test of whether internal/api/EXAMPLE_ENDPOINT.md and db/RECIPES.md are good
# enough: it hands a FRESH agent session nothing but this repository and those two documents, gives it
# ONE instruction — add a new endpoint — and checks whether the result has `make check` and the
# spec-drift gate green, with a handler that declares Security and x-dkp-permission. No hints, no
# follow-ups. If the agent needs guidance the documents did not give, the documents have a bug, and
# the failure is filed against THEM, never against the code.
#
# LOCAL-ONLY, ON PURPOSE (decision record §U5). This ships as a runnable Makefile target
# (`make eval-example-endpoint`) with a documented invocation, and NOTHING ELSE:
#   - no .github/workflows/*.yml entry
#   - no CI secret, no API key in repo secrets
#   - no issue-filing path
# The nightly lane — which runner, whose key, what budget, and the issue-filing — lands with the
# release train in Phase 0 PR 7, where the CI-credentials question is being answered anyway. Writing a
# workflow here that needs a secret nobody has agreed to is exactly the overreach U5 forbids. Until
# then this runs on a developer's laptop, against a runner they configure, at their own cost.
#
# USAGE
#   make eval-example-endpoint                      # uses the default runner (the `claude` CLI)
#   DKP_EVAL_RUNNER='my-agent --flag' make eval-example-endpoint
#   DKP_EVAL_KEEP=1 make eval-example-endpoint      # keep the scratch worktree for inspection
#
# The runner is invoked as:  <runner> "<prompt>"   in the scratch worktree's directory. Any CLI that
# takes a single prompt argument, edits the working tree in place, and exits 0 works — set
# DKP_EVAL_RUNNER to yours. The prompt tells the agent its whole context is the repo plus the two
# documents.

set -euo pipefail

cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"
REPO_ROOT="$(pwd)"

note()  { printf '  \033[36m%s\033[0m\n' "$*"; }
warn()  { printf '  \033[33m%s\033[0m\n' "$*"; }
die()   { printf '\033[31m  %s\033[0m\n' "$*" >&2; exit 1; }

# The runner. Default to the `claude` CLI; overridable so any single-prompt agent CLI can drive it.
RUNNER="${DKP_EVAL_RUNNER:-claude}"
RUNNER_BIN="${RUNNER%% *}"

if ! command -v "$RUNNER_BIN" >/dev/null 2>&1; then
    warn "no agent runner on PATH (looked for '$RUNNER_BIN')."
    warn "This eval is LOCAL-ONLY and needs an agent CLI plus its API credentials — neither ships in"
    warn "this repo (decision record §U5). Configure one and set DKP_EVAL_RUNNER, e.g.:"
    warn "    DKP_EVAL_RUNNER='claude -p' make eval-example-endpoint"
    die  "runner '$RUNNER_BIN' not found — nothing to evaluate against."
fi

# The task. Exactly the instruction the decision record specifies, and NOTHING more: add a read
# endpoint over a NEW table, so the agent must walk every one of the nine steps in EXAMPLE_ENDPOINT.md
# (schema, query, gen, service, handler+register, gen, tests, docs, verify) with no shortcuts.
read -r -d '' TASK <<'EOF' || true
Your entire context is this repository plus two documents: internal/api/EXAMPLE_ENDPOINT.md and
db/RECIPES.md. Read them first. Then add ONE new read endpoint:

    GET /api/v1/guild/settings

backed by a NEW table `guild_setting(key TEXT, value_json TEXT)` — the singleton guild's freeform
settings, keyed by name. Follow EXAMPLE_ENDPOINT.md's nine steps end to end: schema in db/schema.hcl,
a query in db/queries/, `make gen`, a service, the handler + huma.Register with an explicit
OperationID, Security and an x-dkp-permission, `make gen` again, tests, and a docs/changelog line.

Do not ask questions. Everything you need is in the two documents. When you are done, `make check`
and the spec-drift gate must be green.
EOF

# An isolated worktree so the eval never touches the developer's checkout. HEAD, detached, disposable.
SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/dkp-eval-XXXXXX")"
WORKTREE="$SCRATCH/tree"

cleanup() {
    if [ "${DKP_EVAL_KEEP:-0}" = "1" ]; then
        warn "DKP_EVAL_KEEP=1 — leaving the scratch worktree at $WORKTREE"
        return
    fi
    git -C "$REPO_ROOT" worktree remove --force "$WORKTREE" >/dev/null 2>&1 || true
    rm -rf "$SCRATCH" || true
}
trap cleanup EXIT

note "creating a disposable worktree at $WORKTREE"
git -C "$REPO_ROOT" worktree add --detach "$WORKTREE" HEAD >/dev/null

note "handing the task to: $RUNNER"
note "the agent gets the two documents and the one instruction, and NO further guidance."
( cd "$WORKTREE" && $RUNNER "$TASK" ) || die "the agent runner exited non-zero before finishing the task."

# The verdict. Three assertions, all in the resulting tree, all from the acceptance criteria:
#   1. make check green
#   2. the spec-drift gate green (make verify-spec)
#   3. the new operation declares Security and x-dkp-permission
fail=0

note "asserting: make check"
if ! ( cd "$WORKTREE" && make check ); then
    warn "make check is RED in the agent's tree — the documents did not carry the agent to a passing"
    warn "build. File the failure against EXAMPLE_ENDPOINT.md / RECIPES.md, not against the code."
    fail=1
fi

note "asserting: spec-drift gate (make verify-spec)"
if ! ( cd "$WORKTREE" && make verify-spec ); then
    warn "the spec-drift gate is RED — the agent changed the API without regenerating openapi.json,"
    warn "which means step 6 of EXAMPLE_ENDPOINT.md did not land the agent where it should."
    fail=1
fi

note "asserting: the new handler declares Security and x-dkp-permission"
if ! grep -rq "guild/settings" "$WORKTREE/internal/api"; then
    warn "no handler mentioning guild/settings was found under internal/api — the agent did not add"
    warn "the endpoint at all. EXAMPLE_ENDPOINT.md step 5 is where the trail went cold."
    fail=1
elif ! grep -rq "ExtensionPermission" "$WORKTREE/internal/api"; then
    warn "the endpoint exists but declares no x-dkp-permission — arch_test.go would reject it, and so"
    warn "does this eval."
    fail=1
fi

if [ "$fail" -ne 0 ]; then
    die "EVAL FAILED — the worked-example documents need work. (Set DKP_EVAL_KEEP=1 to inspect the tree.)"
fi

printf '\033[32m  EVAL PASSED\033[0m — a fresh agent added the endpoint from the two documents alone.\n'
