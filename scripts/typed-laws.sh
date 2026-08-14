#!/usr/bin/env bash
# advisory / typed-laws — the architectural laws read against TYPE information, over a tree that
# builds. The entry point `make lint-laws` runs (issue #172).
#
# The RULES live in internal/repogate/typedlaw, and its package doc is where the argument is. This
# file is the shim that builds them and points them at a tree, exactly as scripts/repo-gates.sh is
# for internal/repogate.
#
# WHAT IT IS FOR, in one line: internal/repogate reads one file at a time with go/parser, so `sql`
# and `time` and `huma` are identifiers somebody happened to type. A dot-imported `. "time"` makes
# `Now()` a bare call, a type alias reaches *sql.DB without the file naming database/sql, and
# `rate := 0.15` is a float64 in a package where floats do not exist. None of the three has anything
# for a syntax rule to match.
#
# ADDITIVE, and never a replacement. Every id below is still a rule in internal/repogate, and
# `make lint-repo` is still what a merge is blocked on — it needs no build, so it reads a
# deliberately tainted tree in t.TempDir() and a repository mid-sequence, which is the property that
# makes it the gate. test/repo/typed_laws_test.go asserts the additivity in code rather than here.
#
# ADVISORY BY CONSTRUCTION, not via `continue-on-error` — .github/workflows/ci.yml bans that string
# and TestCIWorkflow_NoContinueOnError asserts its absence, because it is the quiet way to keep a
# gate in the checks list while removing its teeth. In the default MODE=advise this script prints
# every finding, emits a `::warning::` so it is visible in the PR's checks annotations, and exits 0.
# MODE=enforce exits non-zero on a finding instead. Both paths exist today and both are tested.
#
# Advisory does NOT mean "cannot fail". A pass that exits 0 because it never ran is worse than no
# pass — the rule scripts/migrate-lint.sh states for atlas and `make govulncheck` states for its
# binary. So the two outcomes are kept apart: a FINDING is advisory, and a BROKEN INVOCATION (no Go,
# the engine did not compile, the inspected tree does not build, a package does not type-check) is a
# hard failure in both modes.
#
#   MODE=advise|enforce   advise (default) warns on a finding; enforce fails on one
#   DKP_REPO_ROOT         the tree to INSPECT, the same contract the other gate scripts honour
#
# The ids, so that a search for one lands somewhere useful. All but SQL004 are also rules in
# internal/repogate; SQL004 is law 2 as AGENTS.md states it — a statement about a TYPE, which is the
# one thing a syntax pass cannot make a rule out of.
#
#   ROUTE001  huma.Register reached outside internal/api, by object              (law 1)
#   SQL001    database/sql.Open / OpenDB outside internal/store, by object       (law 2)
#   SQL002    a database/sql method called outside internal/store, by declaring package  (law 2)
#   SQL003    a store ForTest raw-SQL helper reached from the non-test build
#   SQL004    a database/sql handle TYPE held outside internal/store             (law 2, stated)
#   PURE001   internal/strategy reaches internal/store transitively              (law 3)
#   PURE002   internal/strategy reaches math/rand transitively                   (law 3)
#   CLOCK001  time.Now reached outside internal/clock, by object
#   CLOCK002  clock.System reached in internal/strategy, by object
#   MONEY001  an expression whose TYPE is a float in internal/ledger or internal/strategy
#
# Law 4 — no raw fetch outside web/src/api — has no Go in it, so nothing here can be a second
# opinion about it. WEB001 in internal/repogate remains the gate, and the type-aware pass over that
# tree is `tsc`, which `make vet` already runs.
#
# Thin glue around a compiled binary, deliberately kept as bash (issue #125's first bucket).

set -euo pipefail

# Resolved BEFORE the target below, because the ENGINE always lives here, next to this file, while
# DKP_REPO_ROOT points at the tree being INSPECTED.
gates_dir="$(cd "$(dirname "$0")" && pwd)"
module_root="$(cd "$gates_dir/.." && pwd)"

target="$(cd "${DKP_REPO_ROOT:-$module_root}" && pwd)"

MODE="${MODE:-advise}"

case "$MODE" in
    advise | enforce) ;;
    *)
        printf '\033[31m  MODE=%s is not a mode: expected advise or enforce\033[0m\n' "$MODE" >&2
        exit 1
        ;;
esac

# No Go, no pass — and that is a FAILURE, not a skip, in BOTH modes. The whole subject of this
# script is code the compiler has agreed is code; without a compiler there is nothing to say.
if ! command -v go >/dev/null 2>&1; then
    printf '\033[31mFAIL\033[0m [LAW000] go is not on PATH, so the type-aware pass could not run\n'
    printf '  install the toolchain with: make setup\n'
    exit 1
fi

# BUILD, THEN RUN — never `go run` (issue #142, ADR-0022). `go run` collapses the exit code to 1,
# so "a law fired" (1) and "the pass could not run" (2) would be indistinguishable, and it appends
# `exit status 1` after the explanation the pass just wrote.
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT

if ! go -C "$module_root" build -o "$build_dir/dkpvet" ./internal/repogate/cmd/dkpvet; then
    printf '\033[31mFAIL\033[0m [LAW000] the analyzers did not compile, so the type-aware pass could not run\n'
    exit 1
fi

status=0
env DKP_REPO_ROOT="$target" MODE="$MODE" "$build_dir/dkpvet" || status=$?

# 2 is "could not run", and it is a hard failure in both modes: `go list` failed, the inspected tree
# does not build, or a package did not type-check. Passing it through unchanged is what keeps the
# advisory honest — the alternative is a green step that read nothing.
if [ "$status" -eq 2 ]; then
    printf '\033[31m  the type-aware pass could not analyse the tree\033[0m\n' >&2
    printf '  This is a hard failure in BOTH modes: advisory covers a VERDICT about code, never\n' >&2
    printf '  code that was never read. `make vet` will be failing on the same tree.\n' >&2
    exit 2
fi

exit "$status"
