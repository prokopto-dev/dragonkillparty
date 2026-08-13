#!/usr/bin/env bash
# Apply .github/labels.yml to this repository's labels.
#
# The manifest is the source of truth and repo settings are the copy; this script is the one
# direction that write goes. It exists because the failure it fixes (#43) was invisible: an issue
# form may declare any label it likes, and GitHub drops the ones that do not exist WITHOUT SAYING
# SO — so five forms declared four labels that had never been created and every issue filed from
# them arrived bare.
#
# DRY RUN BY DEFAULT, like `dkp import`. It prints the plan and writes nothing unless given
# --apply. A script that mutates repository settings on a bare invocation is one an agent runs by
# accident while exploring.
#
# IT NEVER DELETES. A label removed from repo settings is removed from every issue that carried it,
# and that history is not recoverable from this file. Labels present in the repo but absent from the
# manifest are REPORTED, and removing one is a deliberate `gh label delete` by a human who has read
# the report.
#
# Needs `gh`, authenticated. The gate that keeps the forms honest — test/repo/labels_test.go — needs
# neither, and runs on every `make check`: this script is for the moment a label is added, not for
# every PR.
#
# Usage:
#   scripts/sync-labels.sh            # plan only
#   scripts/sync-labels.sh --apply    # create and update

set -euo pipefail
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

MANIFEST=".github/labels.yml"

APPLY=0
case "${1:-}" in
    --apply) APPLY=1 ;;
    "") ;;
    *)
        echo "usage: $0 [--apply]" >&2
        exit 2
        ;;
esac

[ -f "$MANIFEST" ] || {
    echo "$MANIFEST not found"
    exit 1
}

command -v gh >/dev/null 2>&1 || {
    echo "gh is not installed — see https://cli.github.com" >&2
    exit 1
}

# Parse the manifest into "name<TAB>color<TAB>description" lines.
#
# STRICT: any non-blank, non-comment line that is not one of the three expected keys is a hard
# error. The alternative — skipping what it does not understand — would let a typo'd key silently
# drop a label from the plan while the script reported success.
parse_manifest() {
    awk '
        /^[[:space:]]*(#|$)/ { next }
        /^- name:/ {
            if (name != "") emit()
            name = substr($0, index($0, ":") + 1); gsub(/^[[:space:]]+|[[:space:]]+$/, "", name)
            color = ""; desc = ""
            next
        }
        /^[[:space:]]+color:/ {
            color = substr($0, index($0, ":") + 1); gsub(/^[[:space:]]+|[[:space:]]+$/, "", color)
            next
        }
        /^[[:space:]]+description:/ {
            desc = substr($0, index($0, ":") + 1)
            gsub(/^[[:space:]]+|[[:space:]]+$/, "", desc)
            gsub(/^"|"$/, "", desc)
            next
        }
        { printf "sync-labels: %s:%d: unparseable line: %s\n", FILENAME, FNR, $0 > "/dev/stderr"; bad = 1 }
        END {
            if (name != "") emit()
            if (bad) exit 1
        }
        function emit() {
            if (color == "" || desc == "") {
                printf "sync-labels: label %s is missing a color or a description\n", name > "/dev/stderr"
                exit 1
            }
            printf "%s\t%s\t%s\n", name, color, desc
        }
    ' "$MANIFEST"
}

manifest_file="$(mktemp)"
existing_file="$(mktemp)"
trap 'rm -f "$manifest_file" "$existing_file"' EXIT

parse_manifest >"$manifest_file"

[ -s "$manifest_file" ] || {
    echo "sync-labels: $MANIFEST declared no labels" >&2
    exit 1
}

# `gh label list` paginates at 30 by default; the manifest is already larger than that.
gh label list --limit 200 --json name --jq '.[].name' | sort >"$existing_file"

created=0
updated=0

while IFS=$'\t' read -r name color desc; do
    if grep -qxF "$name" "$existing_file"; then
        if [ "$APPLY" -eq 1 ]; then
            gh label edit "$name" --color "$color" --description "$desc" >/dev/null
        fi
        printf '  \033[34mup-to-date/edit\033[0m  %-18s #%s\n' "$name" "$color"
        updated=$((updated + 1))
    else
        if [ "$APPLY" -eq 1 ]; then
            gh label create "$name" --color "$color" --description "$desc" >/dev/null
        fi
        printf '  \033[32mcreate\033[0m          %-18s #%s\n' "$name" "$color"
        created=$((created + 1))
    fi
done <"$manifest_file"

# The report half. Not an error: Renovate, GitHub and a human triaging at midnight all create
# labels, and this script is not the authority on whether one of those was a mistake.
extra=0
while IFS= read -r name; do
    if ! cut -f1 "$manifest_file" | grep -qxF "$name"; then
        printf '  \033[33mnot in manifest\033[0m %s\n' "$name"
        extra=$((extra + 1))
    fi
done <"$existing_file"

if [ "$APPLY" -eq 1 ]; then
    printf '\n  \033[32m%d created, %d updated\033[0m' "$created" "$updated"
else
    printf '\n  \033[33mplan only — nothing written\033[0m (%d to create, %d to update)' "$created" "$updated"
fi

if [ "$extra" -ne 0 ]; then
    printf ', %d label(s) in the repo and not in %s' "$extra" "$MANIFEST"
fi

printf '\n'
