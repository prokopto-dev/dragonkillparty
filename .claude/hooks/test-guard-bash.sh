#!/usr/bin/env bash
# Self-test for guard-bash.sh. A guard nobody tests is a guard that silently stops guarding —
# and this one has already had two bugs: it blocked the branch/PR flow it exists to enable, and
# it fires on command TEXT, so writing this test inline blocked the test itself.
#
# Run: bash .claude/hooks/test-guard-bash.sh

set -uo pipefail
# `|| exit` is not ceremony here (SC2164, issue #187). Without it a failed cd leaves the script in
# whatever directory it was invoked from, where the relative GUARD path below resolves to nothing —
# and every `check` then reports `block`, because the guard that "blocked" the command was a shell
# that could not find a file. A self-test for a fail-open guard, passing without running it.
cd "$(dirname "$0")/../.." || exit 1
GUARD=.claude/hooks/guard-bash.sh

pass=0
fail=0

# check <expect: block|allow> <command>
check() {
    local expect="$1" cmd="$2" got
    if printf '{"tool_name":"Bash","tool_input":{"command":%s}}' \
        "$(printf '%s' "$cmd" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')" \
        | bash "$GUARD" >/dev/null 2>&1; then
        got=allow
    else
        got=block
    fi
    if [ "$got" = "$expect" ]; then
        printf '  \033[32mok\033[0m    %-6s %s\n' "$got" "$cmd"
        pass=$((pass + 1))
    else
        printf '  \033[31mFAIL\033[0m  want %s, got %s: %s\n' "$expect" "$got" "$cmd"
        fail=$((fail + 1))
    fi
}

echo "guard-bash self-test"

# Unrecoverable history — must block.
check block 'git push --force origin feat/x'
check block 'git push --force-with-lease origin feat/x'
check block 'git push origin main'
check block 'git push -u origin main'
check block 'git push origin master'
check block 'git push origin HEAD:main'
check block 'git push origin --tags'
check block 'git push origin v1.0.0'
check block 'git tag v1.2.3'

# The reviewed flow — must allow. Blocking these makes an agent unable to propose a change.
check allow 'git push -u origin ci/green-main'
check allow 'git push origin feat/add-ledger'
check allow 'git push -u origin fix/parser-who-header'
check allow 'gh pr create --fill'
check allow 'git tag -l'
check allow 'git status'
check allow 'make check'

# Releases and deploys stay blocked regardless of branch.
check block 'gh release create v1.0.0'
check block 'docker push ghcr.io/prokopto-dev/dragonkillparty:1'
check block 'npm publish'

# Data destruction.
check block 'rm -f /data/dkp.db'
check block 'dkp import eqdkp --source x --commit'

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
