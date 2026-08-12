#!/usr/bin/env bash
# Architectural gates and the licence firewall — the entry point `make lint-repo` runs.
#
# The RULES live in internal/repogate (issue #123). This file is the shim that points them at a
# tree, and it is deliberately the whole of it: everything that decides whether a rule fires is Go,
# unit-tested directly instead of only through a subprocess, and the negative fixtures in
# test/repo/gates_test.go run through this script exactly as they did when the rules were greps.
#
# The rule ids, so that a search for one lands somewhere useful:
#
#   ROUTE001  an HTTP route declared with huma.Register outside internal/api      (law 1)
#   SQL001    sql.Open / sql.OpenDB outside internal/store                        (law 2)
#   SQL002    .Query / .QueryRow / .Exec outside internal/store                   (law 2)
#   SQL003    a ForTest raw-SQL helper called outside a _test.go file
#   PURE001   internal/strategy imports internal/store                            (law 3)
#   PURE002   math/rand in internal/strategy                                      (law 3)
#   CLOCK001  time.Now outside internal/clock
#   CLOCK002  clock.System constructed in internal/strategy
#   MONEY001  a float type in internal/ledger or internal/strategy
#   MONEY002  SQLite's total() — it returns a float where sum() returns an integer
#   MONEY003  a REAL / NUMERIC / DECIMAL column type
#   WEB001    raw fetch / XMLHttpRequest outside web/src/api                      (law 4)
#   WEB002    dangerouslySetInnerHTML
#   WEB003    an off-origin URL anywhere in the SPA
#   DS001     a raw hex colour outside the token layer
#   DS002     a raw px value outside the token layer
#   MIG001    DDL inside a goose Down block
#   MIG002    a backtick-quoted identifier in a migration
#   MIG003    a migration frozen by db/migrations-sqlite/SHIPPED.lock was modified
#   ENUM001   a hand-written string-enum CHECK in db/schema.hcl
#   ADR001    a change that needs a decision record carries one
#   GOLD001   '-update' in a command a CI job runs — the golden-file rewrite fence
#   PIN001    an action not pinned to a 40-char commit SHA
#   QEMU001   QEMU in a workflow
#   AGPL001   an EQdkp Plus identifier outside the allowlisted files
#   AGPL002   an EQdkp Plus config key used as a DKP schema name
#   GATE000   the engine itself could not run
#
# Rules whose target tree does not exist yet pass vacuously, and say so. That is correct, not a
# hole: the rule is installed before the code it gates (ROADMAP sequencing doctrine #1).

set -euo pipefail

# Resolved BEFORE the target below, because the ENGINE always lives here, next to this file, while
# DKP_REPO_ROOT points at the tree being INSPECTED — which for a negative fixture is a t.TempDir()
# with no Go module in it at all.
gates_dir="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$gates_dir/.." && pwd)"

# DKP_REPO_ROOT lets a test point the gates at a tree other than this checkout. It exists so the
# gates can be *tested rather than trusted*: a test writes a deliberately tainted tree into
# t.TempDir() — an unpinned action, a stray sql.Open — and requires this script to exit non-zero.
# Such a fixture cannot live inside the repo, because the real `make lint-repo` would find it and
# fail the project's own CI. Unset (the CI and local default), the tree is this checkout.
target="$(cd "${DKP_REPO_ROOT:-$module_root}" && pwd)"

# No Go, no gates — and that is a FAILURE, not a skip. It is the same rule MIG003 has always
# applied to itself and now applies to the whole set: a gate that cannot run must not report green.
# ci.yml's `lint / repo` job installs the toolchain for exactly this, and
# TestCI_LintRepoJob_HasTheGoToolchain pins the step.
if ! command -v go >/dev/null 2>&1; then
    echo "repo gates"
    printf '\033[31mFAIL\033[0m [GATE000] go is not on PATH, so no repository gate could run (MIG003 included)\n'
    printf '  install the toolchain with: make setup\n'
    printf '\n\033[31mrepo gates failed\033[0m — see the rule ids above.\n'
    exit 1
fi

exec env DKP_REPO_ROOT="$target" go -C "$module_root" run ./internal/repogate/cmd/repogate
