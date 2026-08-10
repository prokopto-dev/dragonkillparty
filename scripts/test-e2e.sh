#!/usr/bin/env bash
# Run the Playwright end-to-end suite against the BUILT BINARY.
#
# `make test-e2e` calls this. Playwright's own webServer boots bin/dkp (web/playwright.config.ts);
# this script's job is everything around that — making sure the binary, the browser and the Node
# toolchain are actually present, and proving afterwards that the run was not vacuous.
#
# NO VACUOUS PASS, which is the entire reason this target was promoted from a stub. Until now
# `make test-e2e` printed "not yet implemented" and exited 0, so CI's `test / e2e (1)` and
# `test / e2e (2)` reported green while executing nothing — honest as a stub, but a checks list that
# implies browser coverage nobody has. Every way this script could exit 0 without running a browser
# is closed:
#
#   - a missing pnpm is fatal, not a skip;
#   - a missing bin/dkp is BUILT, not skipped past;
#   - a run that selected zero tests fails on the test count read back from the JSON reporter.
#
# SHARD is the "n/m" Playwright shard from the CI matrix; unset runs everything.

set -euo pipefail

cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() { printf '\033[31m  %s\033[0m\n' "$*" >&2; exit 1; }

command -v pnpm >/dev/null 2>&1 ||
	die "pnpm is not installed — the e2e harness is a Node tool. See make setup (Node + pnpm)."

[ -f web/package.json ] || die "web/ is not scaffolded — there is no SPA to exercise"

# The binary under test. CI downloads it from the `build / binary` artifact precisely so this job
# does not pay for a second Go+Vite build; a laptop usually has to make one.
#
# Building when it is absent is not the "guard that skips the work" the Makefile header bans — the
# work still runs, unconditionally. What it must never become is `[ -f bin/dkp ] || exit 0`.
if [ ! -f bin/dkp ]; then
	printf '  no bin/dkp — building it first (make build)\n'
	make build
	[ -f bin/dkp ] || die "make build produced no bin/dkp"
fi

printf '  installing web dependencies (frozen, no scripts)\n'
(cd web && pnpm install --frozen-lockfile --ignore-scripts)

# The browser. Chromium only: the {Chromium, Firefox, WebKit} matrix is the pre-release lane
# (docs/design/04-testing.md), and three downloads per PR buys coverage this suite is not asserting.
#
# --with-deps installs the shared libraries a headless Chromium needs, via the system package
# manager. That only exists on Linux — on macOS the flag is an error, not a no-op — so it is applied
# by platform rather than unconditionally.
#
# Spelled out as two branches rather than an optional-flag array: macOS ships bash 3.2, where an
# EMPTY array expanded as "${flags[@]}" is an unbound-variable error under `set -u`. The portable
# array idiom for that is unreadable, and this script has exactly two such flags.
printf '  installing the Chromium build Playwright pins\n'
if [ "$(uname -s)" = "Linux" ]; then
	(cd web && pnpm exec playwright install --with-deps chromium)
else
	(cd web && pnpm exec playwright install chromium)
fi

# --retries=0 is the config's default and is restated on the command line because the CI step's
# comment promises it by name. A flaky e2e is QUARANTINED, never retried: a retried test is a test an
# agent learns to trust when it should not.
if [ -n "${SHARD:-}" ]; then
	printf '  shard %s\n' "$SHARD"
	(cd web && pnpm exec playwright test --retries=0 --shard="$SHARD")
else
	(cd web && pnpm exec playwright test --retries=0)
fi

# The anti-vacuum check, in the same spirit as `make test-property`'s test counter. Playwright exits
# 0 when a filter or a shard selects nothing, so a suite that quietly stopped matching any file would
# report a green browser job that never opened a browser.
results=web/test-results/results.json
[ -f "$results" ] || die "Playwright wrote no $results — the JSON reporter is not configured"

ran=$(node -e '
	const report = require("./" + process.argv[1]);
	const count = (suite) =>
		(suite.suites ?? []).reduce((n, s) => n + count(s), 0) +
		(suite.specs ?? []).reduce((n, spec) => n + spec.tests.length, 0);
	console.log((report.suites ?? []).reduce((n, s) => n + count(s), 0));
' "$results")

if [ "$ran" -eq 0 ]; then
	die "no e2e tests ran$([ -n "${SHARD:-}" ] && printf ' in shard %s' "$SHARD")
A browser suite that selects zero tests reports green. Check web/e2e/ and the testDir in
web/playwright.config.ts."
fi

printf '  \033[32m%s e2e test(s)\033[0m against bin/dkp\n' "$ran"
