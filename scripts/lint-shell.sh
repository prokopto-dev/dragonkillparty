#!/usr/bin/env bash
# The shell gate (issue #122) — `make lint-shell`, and `make fmt`'s shell half via --write.
#
# WHY. scripts/ is ~4,800 lines of bash across 33 files and nothing linted a byte of it until this
# script. #111 is the case that bought it: `make test-coverage-floor` died on macOS because an
# unquoted `$(wc -w)` word-split the awk invocation — BSD wc right-aligns its count in an
# eight-column field, so awk took a stray `6` as its PROGRAM and the target failed before evaluating
# a single coverage number. That is SC2086, reported in milliseconds, on a line nobody had reason to
# look at twice.
#
# Issue #122 also cites #84 (smoke-local's readyz probe printed "reachable" on both branches of an
# `&& ... ||`, so it asserted nothing). BE PRECISE ABOUT WHAT THIS CATCHES: shellcheck does NOT
# report that shape at any severity. Its useless-echo check, SC2005, covers `echo $(cmd)` — the
# no-op #122's acceptance criterion names — and test/repo/lint_shell_test.go drives the gate with
# exactly that. What caught #84 was a test written for the probe, and this gate does not replace one.
#
# TWO TOOLS, TWO QUESTIONS. shellcheck asks "is this correct" and shfmt asks "is this the shape the
# rest of the tree has". They are one target because a contributor should not have to remember two,
# and because `make fmt` fixing one of them is what makes the other one cheap to obey.
#
# SEVERITY IS DELIBERATELY THE DEFAULT (style — everything). Raising it to `warning` would drop
# SC2086 itself, which is the exact defect #111 was: shellcheck classes it as `info`, and SC2005 as
# `style`. A gate tuned until it stops reporting the bug it was bought for is worse than no gate. The
# one blanket exception lives in .shellcheckrc, is a single rule id, and carries its reason there.
#
# Thin glue around a real CLI, deliberately kept as bash (issue #125's first bucket).
set -euo pipefail

# DKP_REPO_ROOT lets test/repo point this script at a fabricated tree in t.TempDir() — the only way
# a negative fixture can hold a deliberately word-splitting script, since one committed under
# scripts/ would fail this project's own CI. `make lint-shell` strips it with `env -u`.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() {
    printf '\033[31m  %s\033[0m\n' "$*" >&2
    exit 1
}

# THE FORMATTING POLICY, IN ONE PLACE. The check and the fix must be the same flags or `make fmt`
# produces a tree `make lint-shell` rejects.
#
#   -i 4   four-space indent. The tree was mixed — 21 files with four spaces, 8 with tabs — and this
#          is both the majority and the lower-churn choice; picking the other one reformatted 1,339
#          lines instead of 639.
#   -ci    indent `case` arms, which is how every `case` in this repository was already written.
#   -bn    a binary operator may open a continuation line, which preserves the `cmd \` / `|| die`
#          shape the release and install scripts use throughout.
SHFMT_FLAGS=(-i 4 -ci -bn)

write=0
case "${1:-}" in
    "") ;;
    --write) write=1 ;;
    *) die "usage: lint-shell.sh [--write]   (--write reformats in place; make fmt passes it)" ;;
esac

command -v shfmt >/dev/null 2>&1 \
    || die "shfmt is not installed — run make setup
  It is pinned as SHFMT_VERSION in the Makefile."

# Only the checking mode needs shellcheck. `make fmt` passes --write and formats; requiring the
# linter to reformat would make the fix unavailable exactly when the gate is what sent you here.
if [ "$write" -eq 0 ]; then
    command -v shellcheck >/dev/null 2>&1 \
        || die "shellcheck is not installed — run make setup
  It is pinned as SHELLCHECK_VERSION in the Makefile and installed, checksum-verified, by
  scripts/install-shellcheck.sh."
fi

# Every tracked shell script, found by shebang rather than by extension: .githooks/pre-commit and
# .githooks/pre-push have no .sh suffix and are exactly the files a formatting-drift bug hides in,
# since git will not run a hook that fails and nobody reads one that works.
#
# .claude/hooks/ joined the enumeration with issue #187, and it is the tree where a shell defect
# costs most. Those five files are the agent-session hooks, and two of them DECIDE WHETHER A TOOL
# CALL RUNS AT ALL: guard-bash.sh blocks the unrecoverable, publishing and destructive commands, and
# guard-protected-paths.sh blocks edits to protected paths. guard-bash.sh is FAIL-OPEN by design —
# its own header says "a missing JSON parser or an unparseable payload allows the command" — so a
# shell defect there is a guard that stops guarding without anything going red. That is the same
# defect class this gate was bought for (#111, an unquoted expansion), in the one place where
# nothing else would notice. Running the gate over them at the time reported an SC2164 `cd` with no
# `|| exit` in the guard's own self-test: the shape where a failed `cd` runs the rest of the script
# against whatever directory it happened to be in.
files=()
for f in scripts/*.sh .githooks/* .claude/hooks/*; do
    [ -f "$f" ] || continue
    # One pattern, not two: `#!/usr/bin/env bash` ends in `sh` as surely as `#!/bin/sh` does, and
    # SC2221/SC2222 said so about the second, unreachable arm this used to carry — a small
    # demonstration that the gate reads the file it is defined in.
    case "$(head -n 1 "$f")" in
        '#!'*sh) files+=("$f") ;;
    esac
done

# NO VACUOUS PASS, the rule this repository applies to every gate: an empty list means the
# invocation is broken (a wrong DKP_REPO_ROOT, a moved directory), not that every script is clean.
[ ${#files[@]} -gt 0 ] \
    || die "no shell scripts found under scripts/, .githooks/ or .claude/hooks/ — nothing was linted.
  A gate that checked nothing must not report success."

if [ "$write" -eq 1 ]; then
    shfmt -w "${SHFMT_FLAGS[@]}" "${files[@]}"
    printf '  shfmt formatted %d shell script(s)\n' "${#files[@]}"

    exit 0
fi

shellcheck --color=never "${files[@]}" \
    || die "shellcheck failed — see the diagnostics above.
  Each carries an SC id: https://www.shellcheck.net/wiki/SC2086 and neighbours. A finding that is
  genuinely intended gets a '# shellcheck disable=SCxxxx' comment on the line with a reason beside
  it, never a wholesale disable."

shfmt -d "${SHFMT_FLAGS[@]}" "${files[@]}" \
    || die "shell scripts are not shfmt-clean — the diff above is what shfmt would change.
  Fix with: make fmt"

printf '  \033[32mshell lint passed\033[0m — %d script(s), shellcheck + shfmt\n' "${#files[@]}"
