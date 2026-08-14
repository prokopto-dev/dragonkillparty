#!/usr/bin/env bash
#
# scan-secrets.sh — PreToolUse on Bash, active only for `git commit`.
#
# Claude Code matches hooks on the tool name, not on the command string, so this is
# registered against the whole Bash matcher and the first thing it does is decide whether
# this is a commit and exit. That early exit costs ~15 ms; the scan only runs at commit time.
#
# Contract:  exit 0 = allow · exit 2 + stderr = block
# Budget:    ~15 ms for non-commits, ~200 ms for a normal staged diff.
# Fails open on a missing tool: with no gitleaks it runs a narrow built-in pattern set and
# says so. It never blocks a commit because a tool is missing.
#
# NOTE: there is no CI secret-scanning job yet — SECURITY.md lists gitleaks as planned. So this
# fallback is currently the ONLY scan, not a fast local approximation of one that CI repeats. That
# makes failing open a real gap rather than a latency trade-off. It stays open because a hook that
# blocks every commit on a machine without gitleaks is a hook people disable, but do not read the
# fallback as "CI will catch it" until the `security / secrets` job exists.
#
# This hook never prints a matched secret, only the pattern that matched.
#
set -euo pipefail

if [[ "${DKP_HOOKS:-on}" == "off" || "${DKP_HOOK_SECRETS:-on}" == "off" ]]; then
    exit 0
fi

payload="$(cat || true)"

if command -v jq >/dev/null 2>&1; then
    JSON_TOOL=jq
elif command -v python3 >/dev/null 2>&1; then
    JSON_TOOL=python3
else
    printf 'scan-secrets.sh: neither jq nor python3 found; commits are UNSCANNED.\n' >&2
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

c="$(printf '%s' "$cmd" | tr '\n\t' '  ' | tr -s ' ')"

# Not a commit? Done.
if ! printf '%s\n' "$c" | grep -Eq '(^|[^[:alnum:]_-])git( +-[^ ]+)* +commit( |$)'; then
    exit 0
fi

root="${CLAUDE_PROJECT_DIR:-}"
if [[ -z "$root" ]]; then
    root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi
cd "$root" 2>/dev/null || exit 0

# `git commit -a` / `-am` / `--all` commits tracked files that were never staged, so the
# staged diff is the wrong thing to scan.
commit_all=0
if printf '%s\n' "$c" | grep -Eq '(^| )-[a-zA-Z]*a[a-zA-Z]*( |$)|(^| )--all( |$)'; then
    commit_all=1
fi

if [[ "$commit_all" -eq 1 ]]; then
    diff_text="$(git diff HEAD 2>/dev/null || true)"
    diff_label="the working tree (git commit -a stages tracked files at commit time)"
else
    diff_text="$(git diff --cached 2>/dev/null || true)"
    diff_label="the staged diff"
fi

if [[ -z "$diff_text" ]]; then
    exit 0
fi

deny() {
    printf 'BLOCKED by .claude/hooks/scan-secrets.sh\n\n%s\n' "$1" >&2
    exit 2
}

# ---------------------------------------------------------------------------
# gitleaks, when it is installed.
#
# --exit-code 7 separates "found leaks" (7) from "I do not understand that subcommand"
# (anything else). That is what makes the version probing safe: gitleaks renamed `protect`
# to `git` in 8.19 and `detect --no-git` to `dir`, and both spellings are still in the wild.
# ---------------------------------------------------------------------------
gl_rc=99
gl_out=""
gl_ran=0

try_gl() {
    set +e
    gl_out="$("$@" 2>&1)"
    gl_rc=$?
    set -e
}

if command -v gitleaks >/dev/null 2>&1; then
    cfg=()
    if [[ -f "$root/.gitleaks.toml" ]]; then
        cfg=(--config "$root/.gitleaks.toml")
    fi

    if [[ "$commit_all" -eq 1 ]]; then
        tmp="$(mktemp -d 2>/dev/null || true)"
        if [[ -n "$tmp" ]]; then
            printf '%s\n' "$diff_text" >"$tmp/pending.diff"
            try_gl gitleaks dir "$tmp" --no-banner --redact --exit-code 7 ${cfg[@]+"${cfg[@]}"}
            if [[ "$gl_rc" -ne 0 && "$gl_rc" -ne 7 ]]; then
                try_gl gitleaks detect --no-git --source "$tmp" --no-banner --redact --exit-code 7 ${cfg[@]+"${cfg[@]}"}
            fi
            rm -rf "$tmp"
        fi
    else
        try_gl gitleaks git --staged --no-banner --redact --exit-code 7 ${cfg[@]+"${cfg[@]}"}
        if [[ "$gl_rc" -ne 0 && "$gl_rc" -ne 7 ]]; then
            try_gl gitleaks protect --staged --no-banner --redact --exit-code 7 ${cfg[@]+"${cfg[@]}"}
        fi
    fi

    if [[ "$gl_rc" -eq 0 || "$gl_rc" -eq 7 ]]; then
        gl_ran=1
    fi
fi

if [[ "$gl_ran" -eq 1 && "$gl_rc" -eq 7 ]]; then
    deny "gitleaks found a secret in $diff_label.

$gl_out

Unstage the file, remove the value, and read it from an environment variable or a
file that .gitignore covers. If this is a fixture that only looks like a secret, add
it to .gitleaks.toml with a comment saying why — do not disable the hook.

Rotate the credential: a secret that reaches a commit is compromised even if the
commit is never pushed."
fi

if [[ "$gl_ran" -eq 1 ]]; then
    exit 0
fi

# ---------------------------------------------------------------------------
# Built-in fallback, used only when gitleaks could not run.
#
# Deliberately narrow: only shapes that are near-impossible to hit by accident. A commit
# hook with false positives gets disabled, which is worse than a commit hook with gaps.
# ---------------------------------------------------------------------------
added="$(printf '%s\n' "$diff_text" | grep -E '^\+' | grep -Ev '^\+\+\+' || true)"
if [[ -z "$added" ]]; then
    exit 0
fi

hits=""
check() { # check <label> <regex>
    if printf '%s\n' "$added" | grep -Eq -e "$2"; then
        hits="${hits}  - $1
"
    fi
}

check 'private key block' '-----BEGIN [A-Z ]*PRIVATE KEY-----'
check 'DKP personal access token (dkp_pat_...)' '(^|[^A-Za-z0-9])dkp_pat_[A-Za-z0-9_-]{20,}'
check 'AWS access key id' 'AKIA[0-9A-Z]{16}'
check 'GitHub token' 'gh[pousr]_[A-Za-z0-9]{36,}'
check 'Slack token' 'xox[abprs]-[0-9A-Za-z-]{10,}'
check 'generic sk- API secret' '(^|[^A-Za-z0-9])sk-[A-Za-z0-9]{32,}'
check 'Discord bot token' '[MNO][A-Za-z0-9_-]{22,25}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{27,}'

if [[ -n "$hits" ]]; then
    deny "A high-confidence secret pattern appears in $diff_label:

$hits
The matched value is not printed here on purpose. Find it with \`git diff --cached\`.

Unstage the file, remove the value, read it from an environment variable or a file
that .gitignore covers, and rotate the credential — a secret that reaches a commit is
compromised even if the commit is never pushed.

gitleaks is not installed, so this was the built-in fallback only, and it is narrow by
design. Install gitleaks (\`brew install gitleaks\`) for the real scan — and note that
there is no CI secret-scanning job yet, so nothing else is checking this."
fi

printf 'scan-secrets.sh: gitleaks not installed — ran the narrow built-in pattern set only, and no CI job repeats it.\n' >&2
exit 0
