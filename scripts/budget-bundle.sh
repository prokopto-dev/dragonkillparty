#!/usr/bin/env bash
# The bundle-size budget: the gzipped initial-route JS must fit under web/bundle-budget.json's
# max_gzip_bytes (250 KB). Prints the measured number into the CI summary (verify-before-phase-0
# item V13) and fails on a breach.
#
# The budget is a ONE-WAY RATCHET: raising max_gzip_bytes needs a CODEOWNERS review, because
# web/bundle-budget.json is CODEOWNERS-protected. That turns a size regression into a decision in the
# diff instead of a drift nobody noticed.
#
# "Initial route" = the entry chunk the browser must download before the first paint, plus the
# static chunks it statically imports. Vite content-hashes every chunk, so this measures the entry
# JS the index.html references. Measuring the whole dist would count lazily-loaded routes that the
# first paint never fetches; measuring only one file would miss a statically-split vendor chunk. The
# honest number is the transitive static-import closure of the entry, which for the scaffold is the
# entry chunk itself.

set -euo pipefail

cd "$(dirname "$0")/.."

die() { printf '\033[31m  %s\033[0m\n' "$*" >&2; exit 1; }

budget_file=web/bundle-budget.json
[ -f "$budget_file" ] || die "$budget_file is missing — the budget is the contract"

# Read the budget. python3 is already a required tool (check-links.py, dc-publish.py).
max=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["max_gzip_bytes"])' "$budget_file") \
	|| die "could not read max_gzip_bytes from $budget_file"

# Pre-scaffold / not-yet-built: nothing to measure. This branch disappears once `make build` stages
# a real dist. It is NOT a silent pass over a broken toolchain — it is the honest "there is no bundle
# yet", and it prints so a reader sees why the number is absent.
if [ ! -d web/dist ] && [ ! -d internal/ui/dist/assets ]; then
	printf '  no built SPA to measure — run `make build` first (bundle budget: %s bytes)\n' "$max"
	exit 0
fi

# Prefer web/dist (the fresh Vite output); fall back to the staged embed copy.
dist=web/dist
[ -d "$dist/assets" ] || dist=internal/ui/dist

# The entry chunk(s): the JS files index.html loads with <script type="module" src=...>. Grep the
# script srcs out of index.html rather than guessing a filename, because the hash makes the name
# unknowable ahead of time.
index="$dist/index.html"
[ -f "$index" ] || die "$index is missing — was the SPA built?"

# Extract the module script srcs. Written to a temp file rather than a bash-4 `mapfile` or a
# process-substitution-fed array, because the laptops run macOS's bash 3.2 which has neither in the
# portable subset — and this gate must produce the same number on a laptop and on CI.
entries=$(mktemp)
trap 'rm -f "$entries"' EXIT

grep -oE '<script[^>]+src="[^"]+"' "$index" \
	| grep -oE 'src="[^"]+"' | sed -E 's/^src="//; s/"$//' | grep -E '\.m?js$' >"$entries" || true

# Placeholder dist (no real hashed entry) still measures something so the pipeline is exercised: the
# placeholder asset. A scaffold with no real bundle is under budget by a wide margin, which is the
# correct verdict.
if [ ! -s "$entries" ]; then
	(cd "$dist" && ls assets/*.js 2>/dev/null | sed 's#^#/#') >"$entries" || true
fi

[ -s "$entries" ] || die "found no entry JS in $index — cannot measure the initial route"

total=0
while IFS= read -r src; do
	[ -n "$src" ] || continue
	# Resolve a leading-slash absolute path against dist.
	rel="${src#/}"
	file="$dist/$rel"
	[ -f "$file" ] || die "index.html references $src but $file does not exist"
	sz=$(gzip -c -9 "$file" | wc -c | tr -d ' ')
	total=$((total + sz))
done <"$entries"

printf '  initial-route bundle: %s bytes gzipped (budget %s bytes)\n' "$total" "$max"

# Print into the CI job summary when running under GitHub Actions. This is the number that resolves
# item V13 — recorded in the summary, not just enforced.
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	{
		printf '### Bundle size\n\n'
		printf '| Initial-route JS (gzip) | Budget | Headroom |\n'
		printf '|---|---|---|\n'
		printf '| %s bytes | %s bytes | %s bytes |\n' "$total" "$max" "$((max - total))"
	} >>"$GITHUB_STEP_SUMMARY"
fi

if [ "$total" -gt "$max" ]; then
	die "initial-route bundle $total bytes exceeds the budget $max bytes.
Raising the budget needs a CODEOWNERS review of web/bundle-budget.json — it is a one-way ratchet.
See .claude/rules/web.md > Budgets and gates."
fi

printf '  \033[32mbundle is within budget\033[0m\n'
