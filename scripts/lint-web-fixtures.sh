#!/usr/bin/env bash
# The negative half of the web lint gate: prove the law-4 fixtures actually TRIP eslint.
#
# Law 4 (web/src holds no fetch/XMLHttpRequest outside web/src/api, and a useEffect body never
# contains a fetch) is guarded by two mechanisms — the WEB001 grep in scripts/repo-gates.sh, and the
# AST-aware eslint config in web/eslint.config.js. The eslint half is the only one that can see a
# useEffect-wrapped fetch. But the deliberate-violation fixtures under web/test-fixtures/lint/ are in
# eslint.config.js's `ignores`, so a bare `eslint .` (pnpm run lint) never lints them — and nothing
# in CI otherwise proves the rules fire, because the Go TestWebLint_* tests only run under `make
# test`, which no CI job runs with pnpm present. This script closes that hole: it runs eslint with
# --no-ignore over the fixtures and asserts a NON-ZERO exit naming the expected rule.
#
# Invoked by `make lint-web` (via the `lint:fixtures` package.json script), so it runs in CI's
# `lint / web` job. The Go TestWebLint_* tests remain the laptop-side check with the same assertions;
# this is the CI-side lock that does not depend on `make test` running with a Node toolchain.
#
# A fixture that PASSES eslint is the failure this gate exists to catch: it means law 4's AST-aware
# half has gone blind. So a zero exit from eslint on a fixture is a FAILURE here, and a non-zero exit
# that names the expected rule is success.

set -euo pipefail

# Run from web/ (the pnpm script sets the cwd) or fall back to the repo's web/ directory.
if [ -f eslint.config.js ]; then
	web_dir="$(pwd)"
else
	web_dir="$(cd "$(dirname "$0")/../web" && pwd)"
fi
cd "$web_dir"

eslint="node_modules/.bin/eslint"
[ -x "$eslint" ] || {
	printf '\033[31m  eslint not installed — run pnpm install in web/\033[0m\n' >&2
	exit 1
}

# fixture <file> <expected-rule>: eslint MUST exit non-zero and the output MUST name the rule.
fail=0
check_fixture() {
	local file="$1" rule="$2" out rc

	[ -f "$file" ] || {
		printf '\033[31mFAIL\033[0m [WEBFIX] fixture missing: %s\n' "$file" >&2
		fail=1
		return
	}

	set +e
	out="$("$eslint" --no-ignore "$file" 2>&1)"
	rc=$?
	set -e

	if [ "$rc" -eq 0 ]; then
		printf '\033[31mFAIL\033[0m [WEBFIX] %s passed eslint — law 4 (%s) has gone blind\n' "$file" "$rule" >&2
		printf '%s\n' "$out" | sed 's/^/  /' >&2
		fail=1
		return
	fi

	if ! grep -q "$rule" <<<"$out"; then
		printf '\033[31mFAIL\033[0m [WEBFIX] %s failed eslint but not on %s\n' "$file" "$rule" >&2
		printf '%s\n' "$out" | sed 's/^/  /' >&2
		fail=1
		return
	fi

	printf '  \033[32m%s trips %s\033[0m\n' "$file" "$rule"
}

check_fixture "test-fixtures/lint/bare-fetch.tsx" "no-restricted-globals"
check_fixture "test-fixtures/lint/useeffect-fetch.tsx" "no-restricted-syntax"
# Canonical §17's token-layer rule. DS001/DS002 in scripts/repo-gates.sh cover CSS; this is the AST
# half that covers code, and it is the only half that can tell `style={{ padding: "4px" }}` from a
# sentence containing "4px".
check_fixture "test-fixtures/lint/raw-token-values.tsx" "no-restricted-syntax"

if [ "$fail" -ne 0 ]; then
	printf '\n\033[31mweb lint negative-fixture gate failed\033[0m — a deliberate law-4 violation was not caught.\n' >&2
	exit 1
fi

printf '  \033[32mweb law-4 negative fixtures all trip their rule\033[0m\n'
