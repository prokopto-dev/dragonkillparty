# Dragon Kill Party — canonical commands.
#
# CI asserts that every row in the AGENTS.md command table resolves to a real target here. The
# reverse is not asserted: the targets below the divider are called by CI, not by hand, and listing
# them in a file agents read every session would be noise.
#
# Targets that cannot do real work yet call `notyet` with the roadmap phase that fills them in, and
# exit 0 so `make check` and CI stay green through Phase 0. `make status` derives the list of what
# is still stubbed straight from those call sites — there is no hand-maintained progress list, and
# adding one would be a third place to forget.
#
# A target that CAN do real work must do it unconditionally. A guard that skips the work when its
# inputs are missing turns into a guard that hides a broken toolchain the moment the inputs exist,
# and a green `make check` that ran nothing is worse than a red one.

SHELL := /bin/bash
.DEFAULT_GOAL := help

GO             ?= go
PKG            := ./...
BIN            := dkp
BUILD_DIR      := ./bin
IMAGE          ?= ghcr.io/prokopto-dev/dragonkillparty
# Each is filtered to [A-Za-z0-9.+_-] before it reaches -ldflags. A git tag is attacker-influenced
# in the sense that it is arbitrary text a maintainer can be socially engineered into creating, and
# these values land inside a double-quoted shell word in the build recipe: a tag containing a quote
# and a semicolon would execute in the build shell. Not reachable from PR CI (checkout fetches no
# tags, so --always yields a bare SHA), but release.yml does fetch tags and runs this same recipe.
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null | tr -cd 'A-Za-z0-9.+_-' || echo dev)
COMMIT         ?= $(shell git rev-parse --short HEAD 2>/dev/null | tr -cd 'A-Za-z0-9' || echo none)
# The COMMIT date, not the wall clock: two builds of the same commit must be byte-identical.
DATE           ?= $(shell git log -1 --format=%cI 2>/dev/null | tr -cd 'A-Za-z0-9:.+_-' || echo unknown)
LDFLAGS        := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Build args threaded into the image so a `make docker` build reports the same version/commit/date
# stamps as `make build`. The Dockerfile mirrors these into its own -ldflags; keep the two in step.
# Kept as a single variable so the `docker` and (Phase 0 PR 7) release-image recipes cannot drift.
DOCKER_BUILD_ARGS ?= --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE)

# Export the image identity so the shell scripts that boot/measure the built image
# (scripts/smoke-local.sh, scripts/image-size.sh) see the SAME IMAGE:VERSION that `make docker`
# tagged. Without this, those scripts fall back to their `:dev` default and, in CI's `build / image`
# job, look for an image that was never built — Docker then tries to pull `:dev` from ghcr and is
# denied (make smoke-local Error 125). The script comments already promise these "mirror the
# Makefile"; exporting makes that true instead of aspirational.
export IMAGE VERSION COMMIT DATE

# The `go` directive in go.mod is the single source of the minimum toolchain. `make setup` asserts
# the installed Go satisfies it; it cannot install Go itself.
GO_REQUIRED    := $(shell awk '/^go /{print $$2; exit}' go.mod)

# python3 is the third toolchain this repository needs, and until issue #83 it was the only one that
# was never declared: `make docs-links` is Python, and a too-old interpreter made a gate fail on a
# tree whose subject was fine. 3.9 because macOS's /usr/bin/python3 is 3.9.6 and the gate is
# deliberately runnable on a laptop's stock interpreter — nothing here installs Python. Each script
# re-declares this number and checks it itself, because most of them are also run directly;
# test/repo/python_floor_test.go fails if the copies disagree.
#
# The gate #83 actually broke was the SPEC gate, and that one is no longer Python: `make verify-spec`
# is internal/specgate now (issue #127), so a wrong interpreter can no longer be reported as spec
# drift. `make mockup-site` left too — internal/mockup since issue #126. The floor stays because
# `make docs-links` is still Python and the failure shape was never specific to the spec.
#
# Deliberately no PYTHON ?= override variable to go with it. The recipes below name `python3`
# literally, for the same reason verify-spec's recipe strips DKP_SPEC_BASE_REF: an interpreter a
# developer's environment can redirect is a merge-blocking gate a developer's environment can turn
# into a no-op.
PYTHON_REQUIRED := 3.9

# Where `go install` puts things, and therefore where `make setup` puts the dev tools. GOBIN wins
# when set, otherwise GOPATH/bin.
GOTOOLS_BIN    := $(shell $(GO) env GOBIN 2>/dev/null)
ifeq ($(GOTOOLS_BIN),)
GOTOOLS_BIN    := $(shell $(GO) env GOPATH 2>/dev/null)/bin
endif

# Put it on PATH for every recipe in this file.
#
# Without this, `make setup` installs golangci-lint into GOTOOLS_BIN and the very next `make check`
# reports "golangci-lint not installed — run make setup" — the command it just ran. The tool is
# there; make cannot see it, because GOPATH/bin is not on a default macOS or Ubuntu PATH. A setup
# target that does not close its own loop is worse than no setup target: it teaches you to distrust
# the error message. Telling contributors to edit their shell profile is not a fix, it is a support
# burden that a one-line export removes.
#
# Appended, not prepended: a deliberately chosen system tool still wins.
export PATH := $(PATH):$(GOTOOLS_BIN)

# Pinned dev tools. Never @latest — an unpinned tool makes CI and the laptop disagree silently.
# renovate: datasource=go depName=mvdan.cc/gofumpt
GOFUMPT_VERSION        ?= v0.11.0
# renovate: datasource=go depName=golang.org/x/tools
GOIMPORTS_VERSION      ?= v0.48.0
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION  ?= v2.12.2
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION    ?= v1.6.0
# renovate: datasource=go depName=github.com/sqlc-dev/sqlc
SQLC_VERSION           ?= v1.31.1
# Atlas is the one pinned tool that is NOT `go install`able: ariga.io/atlas/cmd/atlas carries a
# `replace` directive in its own go.mod, which `go install <pkg>@<version>` refuses, and the last
# version ever published to the module proxy for that path is v0.13.1 from 2023. It is fetched as a
# checksum-verified binary instead — see scripts/install-atlas.sh and scripts/atlas.sha256.
#
# The `NAME ?= value` shape is load-bearing for BOTH consumers: setup-toolchain/action.yml parses
# every pin with `$1==k { print $3 }`, and install-atlas.sh reads this line the same way.
# renovate: datasource=github-releases depName=ariga/atlas
ATLAS_VERSION          ?= v1.3.0
# osv-scanner (issue #132) covers the dependency graphs govulncheck cannot see — above all the ~275
# package npm graph in web/pnpm-lock.yaml, which had NO vulnerability scanning at all until it
# landed. It does not replace govulncheck: govulncheck is reachability-aware (call-graph analysis
# over the Go module graph), osv-scanner is not, so on the Go side osv-scanner is the noisier of the
# two and the complementary one.
#
# Not installed by `make setup` and not `go install`ed by the CI action, unlike every pin above it.
# osv-scanner's own go.mod requires a Go PATCH version (1.26.5 for v2.5.0), and CI runs
# GOTOOLCHAIN=local — so `go install` would couple this project's compiler floor to the scanner's
# release cadence and break the day the scanner asks for a patch newer than ours. This repository now
# pins a patch of its own (`go 1.26.6`, a security floor rather than a preference), which happens to
# clear v2.5.0's — that is a coincidence of two independent cadences and not a reason to couple them.
# CI runs the upstream action instead, which carries the scanner in a container with its own
# toolchain. This pin is what `make osv-scan` reports when the binary is
# missing, and TestOSV_ActionPin_MatchesTheMakefilePin keeps it equal to the version ci.yml runs.
# renovate: datasource=github-releases depName=google/osv-scanner
OSV_SCANNER_VERSION    ?= v2.5.0
# The two static gates over the files CI itself is written in (issues #121 and #122). actionlint and
# shfmt are ordinary Go programs and install exactly like gofumpt above; shellcheck is Haskell and
# joins Atlas as the second pin that cannot be `go install`ed, so it is fetched as a
# checksum-verified archive — see scripts/install-shellcheck.sh and scripts/shellcheck.sha256, which
# read this pin with the same `$1==k { print $3 }` parse, so the `NAME ?= value` shape is
# load-bearing here too.
#
# renovate: datasource=go depName=github.com/rhysd/actionlint
ACTIONLINT_VERSION     ?= v1.7.12
# renovate: datasource=go depName=mvdan.cc/sh/v3
SHFMT_VERSION          ?= v3.13.1
# renovate: datasource=github-releases depName=koalaman/shellcheck
SHELLCHECK_VERSION     ?= v0.11.0

# notyet <phase> <what> — a target that is declared but not yet implemented.
# No leading '@': call sites add it, so this also works inside shell if/else blocks.
define notyet
printf '\033[33m  not yet implemented\033[0m  %s\n  lands in: %s\n' "$(2)" "$(1)"
endef

# go_gate <package> <args> [<extra env -u flags>] — build a Go GATE and run it. Never `go run`
# (ADR-0022, issues #142 and #180).
#
# `go run` is right for a generator and wrong for a gate, for two reasons that both land on the
# person reading a red check:
#
#   1. It collapses the exit code. Whatever the child exits with, `go run` exits 1 — so a gate that
#      FAILED and a gate that was INVOKED WRONGLY arrive identically. lockmanifest.Run deliberately
#      separates the two (2 is usage, 1 is a finding) and TestRun_UnknownArguments_AreAUsageError
#      asserts it; only the invocation was discarding the distinction.
#   2. It appends `exit status 1` to stderr, INSIDE the failure block, after the gate's own
#      explanation. It reads like a crash in the tool rather than a finding about the tree.
#
# Same shape as scripts/repo-gates.sh, which is the other half of the same decision: build into a
# temp directory, run the binary, propagate its status. Nothing has to keep a checked-in binary
# fresh, and Go's build cache makes the rebuild about as cheap as the link `go run` already did.
#
# A BUILD FAILURE EXITS 2, not 1: a gate that could not compile checked nothing, and reporting that
# as "a rule fired" is the same conflation the macro exists to remove. `set -e` and the trap are not
# decoration either — a recipe is one shell invocation, so without them a failed build would run the
# binary that was never written and the scratch directory would outlive the run.
#
# `env -u DKP_REPO_ROOT` wraps BOTH halves, as the `go run` invocations it replaces did: every gate
# below honours that variable so its negative fixtures can run against a tree in t.TempDir(), and a
# value leaking in from a developer's shell would inspect somebody else's directory while printing
# that it passed. $(3) carries the per-gate additions — GOFLAGS for the licence gate (it decides
# which module graph `go list` resolves), DKP_SPEC_BASE_REF for the spec gate (empty disables the
# operationId-rename check).
define go_gate
set -e; \
dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
if ! env -u DKP_REPO_ROOT $(3) $(GO) build -o "$$dir/gate" $(1); then \
	printf '\033[31m  %s did not compile\033[0m — the gate could not run, so nothing was checked\n' '$(1)'; \
	exit 2; \
fi; \
status=0; env -u DKP_REPO_ROOT $(3) "$$dir/gate" $(2) || status=$$?; \
exit $$status
endef

.PHONY: help setup dev gen test-unit test test-property test-perf test-coverage-floor test-importer \
        test-lanes lint vet migration seed docker check check-fast \
        build clean fmt web-deps verify-generated verify-commands labels-sync \
        lint-repo lint-go lint-web lint-migrations lint-actions lint-shell lint-laws licence-gate \
        govulncheck osv-scan mod-tidy-check \
        bench-clone verify-action-pins \
        install-atlas install-shellcheck generated-digest \
        docs-build docs-links verify-spec \
        api-breaking api-changelog-comment budget-bundle verify-postgres test-golden \
        test-authz test-migrations test-e2e test-upgrade test-upgrade-ladder \
        upgrade-ladder-enumerate soak-jobs smoke-local nightly-report status \
        fixture-build fixture-seed fixture-capture fixture-verify fixture-manifest \
        fixture-publish fixture-gate release-version release-notes release-image \
        release-manifest release-sign release-sbom release-smoke release-promote \
        release-promote-rc release-refdb release-failure-issue \
        shipped-lock-seal release-shipped-lock \
        eval-example-endpoint third-party-notices image-size subset-fonts verify-fonts \
        verify-image-arm64 verify-ledger

## help: list every target with its description
help:
	@printf 'Dragon Kill Party — make targets\n\n'
	@grep -E '^## [a-zA-Z_-]+:' $(MAKEFILE_LIST) \
		| awk -F': ' '{ sub(/^## /,"",$$1); printf "  \033[36m%-17s\033[0m %s\n", $$1, $$2 }'
	@printf '\nRun \033[36mmake check\033[0m before claiming a task is done.\n'

## setup: install the toolchain (run once)
setup:
	@command -v $(GO) >/dev/null 2>&1 || { \
		printf '\033[31m  Go is not installed or not on PATH\033[0m\n'; \
		printf '  go.mod requires go %s — install it from https://go.dev/dl/ (or: brew install go)\n' '$(GO_REQUIRED)'; \
		exit 1; }
	@have=$$($(GO) env GOVERSION | sed 's/^go//'); want='$(GO_REQUIRED)'; \
	if [ "$$(printf '%s\n%s\n' "$$want" "$$have" | sort -V | head -1)" != "$$want" ]; then \
		printf '\033[31m  Go %s is too old\033[0m — go.mod requires go %s\n' "$$have" "$$want"; \
		printf '  Upgrade from https://go.dev/dl/ (or: brew upgrade go), then re-run make setup.\n'; \
		exit 1; \
	fi; \
	printf '  go %s satisfies the go %s directive in go.mod\n' "$$have" "$$want"
	@# python3, for `make docs-links`. Checked here rather than discovered when a gate fails: an
	@# interpreter below the floor made the old verify-spec.py die at import and read as spec drift
	@# (issue #83). That gate is Go now (#127); this check remains because the docs-link gate is still
	@# Python and the failure shape was never specific to the spec. Nothing is installed — the floor is
	@# low enough that every supported platform's stock python3 clears it.
	@command -v python3 >/dev/null 2>&1 || { \
		printf '\033[31m  python3 is not installed or not on PATH\033[0m\n'; \
		printf '  `make docs-links` needs python3 >= %s\n' '$(PYTHON_REQUIRED)'; \
		exit 1; }
	@have=$$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])'); want='$(PYTHON_REQUIRED)'; \
	if [ "$$(printf '%s\n%s\n' "$$want" "$$have" | sort -V | head -1)" != "$$want" ]; then \
		printf '\033[31m  python3 %s is too old\033[0m — `make check` needs python3 >= %s\n' "$$have" "$$want"; \
		printf '  The docs-link gate is Python; an older one fails it for the wrong reason.\n'; \
		exit 1; \
	fi; \
	printf '  python3 %s satisfies the >= %s floor\n' "$$have" "$$want"
	@printf '  installing gofumpt $(GOFUMPT_VERSION)\n'
	@$(GO) install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	@printf '  installing goimports $(GOIMPORTS_VERSION)\n'
	@$(GO) install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)
	@printf '  installing golangci-lint $(GOLANGCI_LINT_VERSION)\n'
	@$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@printf '  installing govulncheck $(GOVULNCHECK_VERSION)\n'
	@$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@printf '  installing sqlc $(SQLC_VERSION)\n'
	@$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	@printf '  installing actionlint $(ACTIONLINT_VERSION)\n'
	@$(GO) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	@printf '  installing shfmt $(SHFMT_VERSION)\n'
	@$(GO) install mvdan.cc/sh/v3/cmd/shfmt@$(SHFMT_VERSION)
	@$(MAKE) --no-print-directory install-shellcheck
	@$(MAKE) --no-print-directory install-atlas
	@printf '\033[32m  toolchain installed\033[0m into %s\n' '$(GOTOOLS_BIN)'
	@# Install the tracked git hooks: pre-commit auto-formats staged files, pre-push blocks a push
	@# that would fail CI on formatting. core.hooksPath points git at the tracked directory instead
	@# of .git/hooks, so the hooks are versioned and a clone gets them the moment it runs `make
	@# setup`. chmod because a checkout does not always preserve the executable bit (a zip export, a
	@# umask, a filesystem that drops it) and git will not run a hook that is not +x.
	@git config core.hooksPath .githooks
	@chmod +x .githooks/*
	@printf '\033[32m  git hooks installed\033[0m — pre-commit auto-formats staged files, pre-push blocks unformatted pushes\n'
	@command -v golangci-lint >/dev/null 2>&1 || { \
		printf '\033[31m  installed but not on PATH\033[0m — every make target adds %s itself, so\n' '$(GOTOOLS_BIN)'; \
		printf '  `make check` will work. To run the tools directly, add this to your shell profile:\n'; \
		printf '    export PATH="$$PATH:%s"\n' '$(GOTOOLS_BIN)'; }
	@if command -v pnpm >/dev/null 2>&1; then \
		printf '  installing web dependencies\n'; (cd web && pnpm install --frozen-lockfile --ignore-scripts); \
	else printf '\033[33m  pnpm not found — install Node 22 + pnpm 9 for the SPA (corepack enable)\033[0m\n'; fi
	@printf '  not installed yet, and deliberately so — nothing needs them at this phase:\n'
	@printf '    oasdiff              lands in: Phase 2 (spec breaking-change gate)\n'
	@printf '    vale, lychee         lands in: Phase 0 PR 11 (the docs site and its gates)\n'
	@printf '  the goose CLI is deliberately absent: Atlas authors the migrations and the goose\n'
	@printf '  LIBRARY applies them from inside the binary, so nothing ever invokes `goose` on PATH.\n'

# web-deps: install the SPA's dependencies. ALWAYS — never guarded on a directory.
#
# This used to be `[ -d web/node_modules ] || pnpm install`, in `dev` and in `vet`, and a directory
# that EXISTS was being read as a directory that is CURRENT. It is not: any merge that adds a web
# dependency leaves every existing checkout with a stale node_modules, the guard skips the install,
# and `make check` fails thirty lines deep in tsc with implicit-any errors on files the reader has
# never opened — one buried `Cannot find module '@playwright/test'` explaining all of them (issue
# #64). CI never saw it, because a fresh checkout has no node_modules and the guard always fired
# there; it landed only on laptops and agent worktrees, the population least able to tell it apart
# from something they broke themselves.
#
# `pnpm install --frozen-lockfile` is already idempotent and takes ~0.3 s when the tree matches the
# lockfile, and by definition it cannot rewrite pnpm-lock.yaml. The guard bought that third of a
# second at the cost of a misdiagnosable failure — which is the trade this file's own header
# forbids: a target that CAN do real work must do it unconditionally.
#
# Make runs it once per invocation however many targets need it, so `make check` pays for one.
web-deps:
	@[ -f web/package.json ] || exit 0; \
	command -v pnpm >/dev/null 2>&1 || { \
		printf '\033[31m  pnpm is not installed\033[0m — Node + pnpm are needed for the SPA. See make setup.\n'; \
		exit 1; }; \
	printf '  installing web dependencies (frozen, no scripts)\n'; \
	cd web && pnpm install --frozen-lockfile --ignore-scripts

## dev: run the dev servers — Go on :8080, Vite on :5173
dev: web-deps
	@printf '  Go API on :8080, Vite on :5173 — Ctrl-C stops both\n'
	@trap 'kill 0' EXIT INT TERM; \
		( DKP_DB_PATH=$${DKP_DB_PATH:-./data/dev.db} $(GO) run ./cmd/dkp serve --addr :8080 ) & \
		( cd web && pnpm run dev ) & \
		wait

## gen: regenerate ALL generated code — migrations, sqlc, openapi, clients
# Today: db/schema.hcl's ledger enum CHECKs, atlas.sum, the schema/migration sync assertion, sqlc,
# `dkp openapi -> openapi/openapi.json`, and openapi-typescript -> web/src/api/schema.d.ts.
# openapi-python-client and the SDKs land with the clients/ tree in a later phase; each arrives as
# another line here.
#
# gen-enums.sh runs FIRST and the order is load-bearing: it writes the enum CHECKs into
# db/schema.hcl, and gen-db.sh's next step asserts that db/schema.hcl and the committed migrations
# describe the same schema. Reversed, a catalogue change would read as clean on the run that
# introduced it.
#
# `env -u DKP_REPO_ROOT` for the reason given above lint-repo: the script honours that variable so
# its negative fixtures can run against a tree in t.TempDir(), and a value leaking in from a
# developer's shell would regenerate somebody else's tree while reporting success here.
gen:
	@env -u DKP_REPO_ROOT bash scripts/gen-enums.sh
	@env -u DKP_REPO_ROOT bash scripts/gen-db.sh
	@env -u DKP_REPO_ROOT bash scripts/gen-openapi.sh
	@env -u DKP_REPO_ROOT bash scripts/gen-client.sh
	@env -u DKP_REPO_ROOT bash scripts/gen-docs.sh

# THE TWO TEST LANES (issues #153 and #155). `-count=1` is a GATE on one of them and a pure cost on
# the other, and until #155 it was on both.
#
# `go test`'s RESULT cache is keyed on the files and environment a test reads THROUGH GO. It cannot
# see what a SUBPROCESS reads. Change the very script, fixture or tree a gate polices and that
# gate's Go inputs are byte-identical, so the package reports `ok (cached)` having executed nothing
# — green on exactly the change it exists to catch. test/repo/gate_cache_test.go demonstrates that
# false hit on a fixture rather than asserting it.
#
# THE GATE LANE is every package that can spawn a subprocess at all, from its tests or from the
# code under them, and it keeps `-shuffle=on -count=1` unconditionally:
#
#   test/repo          bash scripts/repo-gates.sh, make licence-gate, python3, eslint, make itself
#   internal/core      TestLintBan_* exec golangci-lint over testdata/lintfixtures
#   internal/api       the snippet-compile gate shells out to go build, sqlc and atlas
#   internal/licence   RuntimeModules runs `go list` over a module tree
#   internal/repogate  the ADR and SHIPPED.lock rules run git, and typedlaw runs `go list -export`
#                      over a fabricated module in t.TempDir()
#   internal/specgate  the operationId-rename check runs git against the base revision
#
# Everything else may cache. The cacheable lane is the COMPLEMENT, computed with `go list` rather
# than listed — a new package is cacheable by default and nothing has to remember to add it
# anywhere. `make test-lanes` prints the split; test/repo asserts the two lanes partition
# `go list ./...` exactly (no package can fall out of the suite) and that every package whose
# sources call exec.Command is in the gate lane or in a named exemption.
#
# Two such packages are deliberately NOT in the gate lane, because what they run is HERMETIC:
# internal/store re-execs its OWN test binary (content-addressed, so the cache tracks the only input
# that subprocess reads), and internal/migrate/shippedlock drives `git` over a repository the test
# creates in t.TempDir(). Both are argued by name in test/repo/gate_cache_test.go, where a third one
# would have to be argued too — the exemption is the reviewable part, not the rule.
GATE_DIRS      := test/repo internal/api internal/core internal/licence internal/repogate \
                  internal/specgate
empty          :=
space          := $(empty) $(empty)
MODULE         := $(shell awk '/^module /{print $$2; exit}' go.mod)
GATE_RE        := ^$(MODULE)/($(subst $(space),|,$(GATE_DIRS)))(/|$$)
#
# -shuffle=on IS NOT ON THE PR PATH ANY MORE, and that is the cost #155 weighed rather than a
# tidy-up. Its seed is the wall clock and `-test.shuffle` is not in `go test`'s cacheable flag set,
# so a shuffled run is never cached even with `-count=1` gone — the two flags have to leave the
# cacheable lane together or the lane does not cache at all. What shuffle buys is order-dependence
# found OVER TIME, which is a property of running it repeatedly, not of running it on your diff:
# nightly-verify.yml's `suite / shuffled` job runs the whole suite with DKP_TEST_SHUFFLE=on every
# night and files an issue on a failure. One env var and no second code path — the shape nightly's
# property job already uses for DKP_PROPERTY_CHECKS — so the nightly suite cannot compile
# differently from the one a PR runs. A malformed value fails loudly in `go test` rather than
# falling back to no shuffle, because a nightly that quietly ran the PR configuration would report
# a run that never happened.
TEST_CACHE_FLAGS := $(if $(DKP_TEST_SHUFFLE),-shuffle=$(DKP_TEST_SHUFFLE) -count=1,)

# lanes_cmd — print the split. ONE definition, two consumers: the `test-lanes` target below and the
# `gotest` macro, so the split test/repo asserts is the split that runs.
#
# Deliberately not a `$(MAKE) test-lanes` sub-make: make executes a recipe line containing $(MAKE)
# even under `-n`, and test/repo dry-runs these targets to read their expanded `go test` flags — a
# sub-make there would silently run the whole suite from inside a test.
define lanes_cmd
$(GO) list $(PKG) | awk -v re='$(GATE_RE)' '{ print ($$0 ~ re ? "gate " : "cache ") $$0 }'
endef

# gotest <flags> — run the suite in its two lanes.
#
# `set -e` is not decoration, and the reason is the one verify-generated records: a recipe is ONE
# shell invocation and make judges it by the LAST command's status, so `;`-chained lanes would
# report green whenever the gate lane failed and the cacheable lane passed.
#
# An empty lane is a hard failure: a `go test` with no package argument tests the current directory,
# and a suite that quietly stopped running half of itself is the vacuous green this Makefile fails
# targets for elsewhere.
define gotest
set -e; \
lanes=$$($(lanes_cmd)); \
gate=$$(printf '%s\n' "$$lanes" | awk '$$1 == "gate" { print $$2 }'); \
cache=$$(printf '%s\n' "$$lanes" | awk '$$1 == "cache" { print $$2 }'); \
[ -n "$$gate" ]  || { printf '\033[31m  the gate lane is empty\033[0m — `make test-lanes` matched no %s package\n' '$(GATE_DIRS)'; exit 1; }; \
[ -n "$$cache" ] || { printf '\033[31m  the cacheable lane is empty\033[0m — `make test-lanes` printed no packages outside the gate lane\n'; exit 1; }; \
$(GO) test $(1) $(TEST_CACHE_FLAGS) $$cache; \
$(GO) test $(1) -shuffle=on -count=1 $$gate
endef

## test-unit: fast unit tests only (budget < 5s)
test-unit:
	@$(call gotest,-short)

## test: integration tests against a real SQLite database (budget ~30s)
test:
	@$(call gotest,-race)

# test-lanes: print the two lanes of the Go test suite, one `gate <pkg>` or `cache <pkg>` per line.
#
# The single authority for the split: the recipes above consume it, and
# test/repo/gate_cache_test.go asserts what it prints. Deliberately absent from the AGENTS.md
# canonical table — nobody types it; it exists so the Makefile and the test cannot disagree about
# which packages may be served from `go test`'s result cache.
test-lanes:
	@$(lanes_cmd)

## test-property: the ledger and strategy properties — 200 checks per PR (budget ~10s)
# The four flagship properties plus their strategy-level twins: P1 conservation, P2 exact splits,
# P5 reversal is an exact inverse, P8 determinism, and "no float appears anywhere in a proposal".
#
# DKP_PROPERTY_CHECKS is read by the tests themselves and inherited from this recipe's environment;
# nightly-verify.yml sets it to 20000. A malformed value FAILS the tests rather than falling back to
# 200 — a typo'd nightly value that quietly ran the PR count would report a deep run that never
# happened. DKP_PROPERTY_SEED replays a reported counterexample.
#
# NO VACUOUS PASS. `-run '^TestProperty_'` exits 0 with "no tests to run" when the filter matches
# nothing, which is a green report for a suite that did not execute — the same failure mode the
# licence gate's "matched no packages" check exists for. So the recipe counts the tests that actually
# ran and fails at zero. `set -o pipefail` is not needed here because the output is captured rather
# than piped, and the capture's exit status is checked directly.
#
# Cacheable (issue #155): neither package reaches its subject through a subprocess, so a cached
# result here is a correct one. `-v` and `-run` are both in `go test`'s cacheable flag set and a
# cached package REPLAYS its stored output, so the `=== RUN` count below still sees every property
# that ran; DKP_PROPERTY_CHECKS is read with os.Getenv, which the cache tracks, so the nightly
# 20,000-case run can never be served from the 200-case entry.
test-property:
	@out=$$($(GO) test $(TEST_CACHE_FLAGS) -v -run '^TestProperty_' \
		./internal/ledger/... ./internal/strategy/... 2>&1) || { printf '%s\n' "$$out"; exit 1; }; \
	printf '%s\n' "$$out"; \
	n=$$(printf '%s\n' "$$out" | grep -cE '^=== RUN[[:space:]]+TestProperty_' || true); \
	if [ "$$n" -eq 0 ]; then \
		printf '\033[31m  no property tests ran\033[0m — `-run ^TestProperty_` matched nothing.\n'; \
		printf '  A property suite that selects zero tests reports green. Check the test names.\n'; \
		exit 1; \
	fi; \
	printf '  \033[32m%s properties\033[0m at %s checks each\n' "$$n" "$${DKP_PROPERTY_CHECKS:-200}"

## test-perf: seed 520k ledger entries and measure the standings read (budget ~3 min)
# The V5 spike (docs/development/verify-before-phase-0.md, issue #190): internal/seed builds a
# realistic-guild-scale ledger through the real commit path, and internal/ledger's TestPerf_ suite
# measures the standings read over it, replays every entry against balance_snapshot, and holds the
# read to 4 statements and a 150 ms p99 on SD-card-class storage.
#
# DKP_PERF_RAIDS is the ONE knob, and it is the same code path at every size — the shape
# test-property uses for DKP_PROPERTY_CHECKS. The suite runs on every PR under `make test` at its
# small default so it cannot rot; this target is the full 3,400-raid, 527,164-entry article.
#
# NO -race, and that is the whole reason this target exists rather than being folded into `make
# test`. The detector instruments modernc.org/sqlite — the SQLite engine compiled to Go — so seeding
# costs ~30x and a measured latency is a measurement of the detector. The suite knows: under -race it
# asserts the I/O half of the budget (page counts, which instrumentation cannot change) and says in
# its output that the wall-clock half was deferred to here.
#
# The grep drops internal/ledger's per-batch "committed ledger batch" line. It is the right log for a
# raid night and 20,461 lines of noise for a seed, and the alternative — turning it off — would mean
# the measured path differed from the production one by one write.
#
# NO VACUOUS PASS, twice over.
#
# `-run '^TestPerf_'` exits 0 with "no tests to run" when the filter matches nothing, so the recipe
# counts what actually ran and fails at zero — same guard, same reason, as test-property.
#
# And `-count=1` UNCONDITIONALLY, where the cacheable lane's targets take $(TEST_CACHE_FLAGS): this
# target's output IS the result. `go test`'s cache would happily replay a stored `ok` — with the
# stored p50 and p99 in its stored output — for a tree whose Go inputs had not changed, and print
# last week's latency as this run's measurement. A cached benchmark is a benchmark that did not
# happen. (DKP_PERF_RAIDS is read with os.LookupEnv, which the cache does track, so the two scales
# could not alias; the reason here is the timing, not the scale.)
test-perf:
	@out=$$(DKP_PERF_RAIDS=$${DKP_PERF_RAIDS:-3400} $(GO) test -v -count=1 -timeout 30m \
		-run '^TestPerf_' ./internal/ledger/... 2>&1) || { \
		printf '%s\n' "$$out" | grep -v 'committed ledger batch'; exit 1; }; \
	printf '%s\n' "$$out" | grep -v 'committed ledger batch' || true; \
	n=$$(printf '%s\n' "$$out" | grep -cE '^=== RUN[[:space:]]+TestPerf_' || true); \
	if [ "$$n" -eq 0 ]; then \
		printf '\033[31m  no perf tests ran\033[0m — `-run ^TestPerf_` matched nothing.\n'; \
		printf '  A perf suite that selects zero tests reports green. Check the test names.\n'; \
		exit 1; \
	fi; \
	printf '  \033[32m%s perf test(s)\033[0m at %s raids\n' "$$n" "$${DKP_PERF_RAIDS:-3400}"

## test-coverage-floor: fail if the ledger, strategy or enum-catalogue packages fall below 95% covered
# A JOB, not a report (Phase 0 PR 10's acceptance criterion). The packages this covers are the ones
# where a plausible-looking wrong change reallocates points across the whole guild, and an uncovered
# branch there is a branch nobody has ever watched execute.
#
# THE ENUM CATALOGUES ARE HERE FOR THE SAME REASON, one step removed: a branch of the region rewrite
# nobody executes is a `make gen` that silently writes the wrong CHECK, and the CHECK is what stops
# the database accepting a kind no code knows how to read. internal/schemaenum joined the list when
# that rewrite moved out of internal/ledger/kinds — otherwise the extraction would have quietly taken
# the logic out from under a floor it had been sitting behind.
#
# It is a ONE-WAY RATCHET in intent and a fixed floor in mechanism: raise COVERAGE_FLOOR when the
# real number has been comfortably above it for a while, never lower it to land a change.
#
# The awk asserts it measured the packages it expected to. `go test` prints "[no test files]" rather
# than a coverage line for a package with no tests, so a package whose tests were deleted would
# vanish from the output and the floor would pass having checked nothing. The expected count is a
# word count of the list below, so keep it as explicit package paths — a `...` pattern expands to
# several packages and one word, and the assertion would then be wrong in the safe-looking direction.
#
# BOTH the quotes and the `tr` on that word count are load-bearing; neither is tidying (issue #111).
# BSD `wc` right-aligns its count in an eight-column field, GNU `wc` does not, so on a stock macOS
# the substitution expands to `       6`. Unquoted, that word-splits into `-v want=` plus a separate
# argument `6`, and awk then takes `6` as its PROGRAM and the program text as a FILENAME — the whole
# target died with "awk: can't open file" before it evaluated a single coverage number, and with it
# `make check`. The quotes close that. The `tr` closes the subtler half: quoted-but-padded leaves
# want as the string "       6", and whether that compares equal to a numeric seen is an awk
# implementation detail. test/repo/coverage_floor_portability_test.go stages a BSD-shaped `wc` on
# any host — Linux CI included — and holds both halves, in both directions.
COVERAGE_FLOOR          := 95
COVERAGE_FLOOR_PACKAGES := ./internal/ledger ./internal/ledger/kinds ./internal/audit/kinds \
                           ./internal/account/kinds ./internal/decay/kinds ./internal/schemaenum \
                           ./internal/authz/role/kinds ./internal/authz/roleassignment/kinds \
                           ./internal/strategy
#
# Cacheable (issue #155), for the same reason as test-property: none of these packages shells out.
# A cached package replays its `ok ... coverage: N%` line, so the awk below measures the same
# number it would have measured on a re-run — and the "measured fewer packages than expected" guard
# still fires, because a package with no test files prints no coverage line whether cached or not.
test-coverage-floor:
	@out=$$($(GO) test $(TEST_CACHE_FLAGS) -cover $(COVERAGE_FLOOR_PACKAGES) 2>&1) || { printf '%s\n' "$$out"; exit 1; }; \
	printf '%s\n' "$$out" | awk -v floor='$(COVERAGE_FLOOR)' -v want="$$(printf '%s' '$(COVERAGE_FLOOR_PACKAGES)' | wc -w | tr -d '[:space:]')" ' \
		/^ok/ && /coverage:/ { \
			for (i = 1; i <= NF; i++) if ($$i == "coverage:") { pct = $$(i + 1); sub(/%$$/, "", pct) } \
			seen++; \
			printf "  %-52s %6.1f%%  (floor %s%%)\n", $$2, pct, floor; \
			if (pct + 0 < floor + 0) { \
				printf "\033[31m  %s is below the %s%% coverage floor\033[0m\n", $$2, floor; bad++ } \
		} \
		END { \
			if (seen != want) { \
				printf "\033[31m  measured %d package(s), expected %d\033[0m — a package with no test\n", seen, want; \
				printf "  files prints no coverage line, so a deleted suite would pass this silently.\n"; \
				exit 1 } \
			if (bad) { exit 1 } \
			printf "  \033[32mcoverage floor met\033[0m in %d package(s)\n", seen \
		}'

## test-importer: EQdkp Plus importer suite — needs Docker (budget ~120s)
test-importer:
	@$(call notyet,Phase 5,runs against real EQdkp 2.0.5/2.1.5/2.2.27/2.3.39 fixtures plus the hostile fixture)

## test-e2e: Playwright against the built binary — needs Node (budget ~60s)
# The browser half of the pyramid. It boots bin/dkp — the SHIPPED binary with the SPA embedded, not
# a Vite dev server — and drives /_design, which renders every token and every base component class
# on one page. What it locks are the Nocturne guarantees no other kind of test can see: Escape
# closing a modal, focus containment, focus RETURNING to the control that opened a dialog (that one
# has already regressed once), the segmented control's radio semantics, label association, the
# three focus-ring offsets, the layered row hover, the virtualizer's row-keyed measurement cache,
# and axe over the whole page.
#
# NOT in `check`, deliberately: it downloads a browser and needs a built binary, and
# docs/design/04-testing.md's inner-loop doctrine is "never run the E2E suite in the inner loop".
# CI runs it as its own sharded, required job. SHARD ("n/m") selects a shard.
test-e2e:
	@bash scripts/test-e2e.sh

## lint: the repo gates, the dependency licence gate, migration lint, golangci-lint and eslint
# lint-migrations is here rather than in a CI job of its own because it is ~30 ms, needs no network
# and reads no live database — and because the person who can act on a destructive-change diagnostic
# is the one still holding the migration. It landed ADVISORY under issue #131 and became a GATE
# under #136: `make lint-migrations` passes MODE=enforce, so a diagnostic fails here and in
# `test / migrations` alike. Read a migration set without being blocked by it with
# `MODE=advise bash scripts/migrate-lint.sh`.
#
# It does mean `make lint`, and therefore `make check`, now needs atlas on PATH. `make setup`
# installs it, and `make gen` has always required it, so this adds no new toolchain obligation —
# only a new place that reports its absence.
#
# lint-actions and lint-shell (issues #121 and #122) are here for the reason lint-migrations is: they
# are milliseconds, need no network and read no database, and the person who can act on a workflow
# expression error or an unquoted expansion is the one still holding the file. Both hard-fail when
# their tool is missing rather than skipping — see lint-go for why a linter that exits 0 because it
# is absent is worse than no linter — so `make setup` now installs actionlint, shellcheck and shfmt.
#
# lint-laws (issue #172) joined for the inner-loop reason all three above give: it costs one
# `go list -export` over a warm build cache, and the person who can act on a law they just broke is
# the one still holding the file. It is the only ADVISORY member of this list — it prints findings
# and exits 0, the posture lint-migrations landed in under #131 and left under #136. `make lint-repo`
# above it is the gate on the same laws; issue #241 is where this one's promotion is argued.
lint: lint-repo licence-gate lint-migrations lint-actions lint-shell lint-laws lint-go lint-web

## vet: build + go vet + staticcheck + tsc
# Runs build + vet, then tsc over the SPA. staticcheck is folded into golangci-lint, so it runs
# under `make lint`. AGENTS.md documents the composition.
#
# web-deps installs the SPA's dependencies first, unconditionally — see that target for why the
# `[ -d web/node_modules ] ||` guard that used to live here was worse than the install it skipped.
vet: web-deps
	@$(GO) build $(PKG) && $(GO) vet $(PKG)
	@if [ -f web/package.json ]; then \
		(cd web && pnpm run typecheck); \
	fi

## fmt: gofumpt + goimports over the tree, shfmt over the shell scripts
# The shell half is `scripts/lint-shell.sh --write`, so the flags that FORMAT and the flags that
# CHECK are one definition in one file. Two copies of a formatter's flags is a `make fmt` that
# produces a tree `make lint-shell` rejects, which is the worst version of both.
#
# A missing formatter WARNS here rather than failing, unlike the lint targets: `make fmt` is a
# convenience that mutates the tree, and the gate that must not pass without its tool is
# `make lint-shell`.
fmt:
	@bin=$$(command -v gofumpt 2>/dev/null || echo '$(GOTOOLS_BIN)/gofumpt'); \
	if [ -x "$$bin" ]; then "$$bin" -l -w .; \
	else printf '\033[33m  gofumpt not installed — run make setup\033[0m\n'; fi
	@if command -v shfmt >/dev/null 2>&1; then env -u DKP_REPO_ROOT bash scripts/lint-shell.sh --write; \
	else printf '\033[33m  shfmt not installed — run make setup\033[0m\n'; fi

## migration: create a migration — make migration NAME=add_bid_hold
# The `ifndef` stays even though the script also checks NAME: it makes `make migration` with no
# argument fail at parse time with the usage line, before any tool runs.
#
# Two tools, and the script checks for both before it generates anything: `atlas` authors the SQL
# from db/schema.hcl, and `go` runs internal/migrate/migrationfmt over what Atlas wrote (issue #128
# — the rewrite guards a file that is permanent once committed, so it is tested Go rather than a sed
# pipeline). Neither is a runtime dependency: goose applies migrations from the embedded set and the
# binary the officer installs contains no generator.
migration:
ifndef NAME
	$(error NAME is required: make migration NAME=add_bid_hold)
endif
	@env -u DKP_REPO_ROOT NAME='$(NAME)' bash scripts/new-migration.sh

## seed: seed a dev guild — make seed PROFILE=perf (small lands in P2, demo in P3)
# Real work as of issue #190, for the `perf` profile. `small` and `demo` are not stubs here any
# more: `dkp seed --profile small` exits non-zero naming the phase that implements it, which is a
# better answer than a Makefile that prints a note and exits 0.
#
# It compiles the binary and runs THAT, rather than `go run`: the seed writes to a real database
# through the real commit path, and running the artefact is the only version of this whose result an
# operator could reproduce. It uses go_build_bin rather than depending on `build` — see that macro
# for why a seed must not stage the SPA. RAIDS is the depth knob: `make seed RAIDS=200` is a few
# seconds where the full profile is a couple of minutes.
#
# DKP_DB_PATH defaults to the same ./data/dev.db `make dev` uses, and the migration runs first
# because seeding an unmigrated database fails on a table that does not exist yet.
seed:
	@$(go_build_bin)
	@DKP_DB_PATH=$${DKP_DB_PATH:-./data/dev.db}; export DKP_DB_PATH; \
	mkdir -p "$$(dirname "$$DKP_DB_PATH")"; \
	$(BUILD_DIR)/$(BIN) migrate >/dev/null; \
	$(BUILD_DIR)/$(BIN) seed --profile $${PROFILE:-perf} $${RAIDS:+--raids $$RAIDS}

## docker: build the container image
# buildx, not `docker build`: it is what CI's build/image job invokes, and it is what the
# Dockerfile's cross-compilation ARGs (TARGETOS/TARGETARCH, supplied per platform) expect.
# `--load` puts the result in the local image store so `make smoke-local` and `make image-size` can
# find it. PLATFORM is optional: unset for a native host build (CI's `build / image`), set to
# linux/arm64 by `verify-image-arm64` below, which is what nightly-verify.yml's `image / arm64-cross`
# job runs (issue #108). Go cross-compiles, so no QEMU is ever needed.
#
# BUILDX_CACHE_FROM / BUILDX_CACHE_TO are the layer cache, and this recipe is what reads them (issue
# #119). They are NOT variables buildx honours — the names are this repository's convention, shared
# with scripts/release-image.sh, which assembles the same two flags in its own array — so before this
# they were set by ci.yml's `build / image` job, documented in a five-line comment, and consumed by
# nothing: the PR image build had no layer cache at all while a comment said it had a careful one.
# Both are unset for a local `make docker`, and `$${VAR:+...}` then expands to nothing, so a laptop
# build is exactly what it was. ci.yml's block sets the policy and explains it; keep the two in step.
docker:
	docker buildx build $${PLATFORM:+--platform $$PLATFORM} \
	  $${BUILDX_CACHE_FROM:+--cache-from $$BUILDX_CACHE_FROM} \
	  $${BUILDX_CACHE_TO:+--cache-to $$BUILDX_CACHE_TO} \
	  --load -f deploy/Dockerfile -t $(IMAGE):$(VERSION) $(DOCKER_BUILD_ARGS) .

## verify-image-arm64: cross-build the arm64 image and prove it really is arm64
# Called by nightly-verify.yml's `image / arm64-cross`. Between #101 deleting the merge-queue job
# that never ran and this target, nothing built linux/arm64 until release.yml's per-arch matrix — so
# arm64 breakage surfaced while cutting a release (issue #108).
#
# Two steps, and the second is not decoration. `make docker` failing is the loud half: a GOARCH build
# tag, a cgo dependency from a bump, a base-image assumption. The quiet half is an image buildx has
# labelled arm64 from --platform whose binary is not — nothing in CI boots an arm64 image (that needs
# the QEMU this project bans), so the bytes are what has to be checked. VERSION and IMAGE are
# exported above, so the script measures exactly the ref this recipe built.
verify-image-arm64:
	@PLATFORM=linux/arm64 $(MAKE) docker
	@bash scripts/verify-image-arch.sh linux/arm64

# go_build_bin — the Go half of `build`, as a macro because `seed` needs it and must NOT have the
# other half.
#
# scripts/build-web.sh stages web/dist over internal/ui/dist for go:embed, which DELETES the
# committed placeholder assets and leaves the working tree dirty. That is correct for `make build`,
# whose output is the shipped binary with the real SPA in it, and wrong for `make seed`, which
# writes ledger rows and serves no HTTP: a developer seeding a dev database should not come back to
# a modified internal/ui/dist. So `seed` compiles the binary and skips the SPA entirely — the
# committed placeholder is already embeddable, so `go build` succeeds without it.
define go_build_bin
CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN) ./cmd/dkp
endef

## build: compile the binary to ./bin
build:
	@bash scripts/build-web.sh
	@$(go_build_bin)

## verify-commands: assert AGENTS.md's command table matches this Makefile
verify-commands:
	@env -u DKP_REPO_ROOT bash scripts/verify-commands.sh

## labels-sync: plan the .github/labels.yml -> repo labels sync (ARGS=--apply to write)
# Deliberately NOT a row in AGENTS.md's canonical command table, and NOT part of `check`: it is a
# maintainer operation against repository settings that needs an authenticated `gh`, run on the day
# a label is added and never again. The per-PR half — that no issue form declares a label the
# manifest does not carry — is test/repo/labels_test.go, which needs no network.
#
# Bare, it prints the plan and writes nothing. `make labels-sync ARGS=--apply` writes.
labels-sync:
	@bash scripts/sync-labels.sh $(ARGS)

## verify-generated: fail if generated files drift from their sources
# Content in, content out: hash the generated trees, regenerate, hash again, compare.
#
# NOT `git diff --exit-code`, which is the obvious implementation and is wrong in both directions.
# It reports a laptop's work in progress as drift, which is noise; and it reports a file that is
# staged-but-uncommitted as drift, which means the gate cannot be run at all in the change that
# first creates a generated tree. Hashing answers the question actually being asked — "does
# regenerating change anything?" — and answers it identically on a laptop mid-change and in a clean
# CI checkout.
#
# `find` includes the tree names, so a file that gen DELETES is caught as well as one it rewrites.
#
# db/schema.hcl is in the list even though it is hand-authored schema truth, because FOUR REGIONS of
# it are not: scripts/gen-enums.sh rewrites the ledger_batch enum CHECKs from internal/ledger/kinds,
# audit_log's actor_kind and outcome CHECKs from internal/audit/kinds, account's kind and
# system_key CHECKs from internal/account/kinds, and decay_run's kind and state CHECKs from
# internal/decay/kinds, each between its own GENERATED markers. Listing the
# file is what makes a hand-edit of any region fail here with "run make gen" instead of surviving
# until the CHECK and the Go catalogue disagree in production.
GENERATED_PATHS := db/migrations-sqlite internal/store/sqlitegen openapi clients web/src/api \
                   db/schema.hcl docs/reference
#
# CHAINED WITH `&&`, NOT `;`. A recipe is one shell invocation and make judges it by the LAST
# command's status, so a `;`-separated version reported success whenever `make gen` failed without
# writing anything: the two digests were equal because nothing had run, and the final printf exited
# 0. A generator that dies on a missing tool — which every gen script does deliberately, rather than
# soft-skipping — is exactly that case, and `codegen-drift` is a required job that would have gone
# green having generated nothing. TestVerifyGenerated_FailingGenerator_FailsTheTarget holds this.
verify-generated:
	@before=$$($(MAKE) --no-print-directory generated-digest) && \
	$(MAKE) --no-print-directory gen && \
	after=$$($(MAKE) --no-print-directory generated-digest) && \
	if [ "$$before" != "$$after" ]; then \
		printf '\033[31m  generated files are stale — run '"'"'make gen'"'"' and commit\033[0m\n'; \
		git --no-pager diff --stat -- $(GENERATED_PATHS) 2>/dev/null || true; \
		exit 1; \
	fi && \
	printf '  \033[32mgenerated files match their sources\033[0m\n'

# A stable digest of every generated file's path and contents. Sorted, so it does not depend on
# directory iteration order.
#
# sha256sum on Ubuntu, shasum on macOS. CI is Ubuntu and the laptops are macOS, so picking either
# one would make this target work in exactly half the places it runs — and the half that broke would
# break `verify-generated`, which is a required CI job.
generated-digest:
	@sum=$$(command -v sha256sum || echo 'shasum -a 256'); \
	for p in $(GENERATED_PATHS); do \
		[ -e "$$p" ] && find "$$p" -type f -print0 | sort -z | xargs -0 $$sum 2>/dev/null; \
	done | $$sum | awk '{ print $$1 }'

## check: everything CI runs (budget ~60s)
# test-coverage-floor is here because it is a REQUIRED CI job, and a required job that `make check`
# does not run makes this target's promise false. It re-runs two packages with coverage on, which
# costs about two seconds; `make test` above has already run them without it.
#
# budget-bundle is here for the same reason, and it was absent for the whole of Phase 0 (issue #166):
# it is in ci-required's `needs:` list, and the sentence above applied to it word for word. It is the
# only required job `make check` can run and did not — `osv-scan` and `govulncheck` are the two others
# missing from this list, and they stay missing because they query api.osv.dev and `make check` must
# work on a laptop with no network. A Vite build and a gzip measurement need neither.
#
# It is LAST because it is the only target here that builds the SPA, and that build is what makes the
# number real rather than a measurement of the committed placeholder. See the target for why.
#
# test-property is NOT listed, and its absence is not an omission: the properties are ordinary Go
# tests in those two packages, so `make test` has already executed all of them at the per-PR count.
# The separate target and the separate CI job exist so that a property failure names its own category
# in the checks list and so the nightly lane can re-run just those tests at 20,000 checks.
#
# mod-tidy-check is here because CI runs it (in `lint / go`) and because the property it asserts is
# invisible until somebody else's PR carries the cleanup. It is offline against a warm module cache,
# which is the bar `check` is held to. See the target for why a tidy that FAILS is reported as a
# failed tidy rather than as an untidy tree.
#
# NONE OF THE ABOVE IS REMEMBERED ANY MORE (issue #183). Every sentence in this comment is one
# person noticing, by hand, that a required job was missing from this list — #166 was `budget-bundle`,
# found months after the hole opened because somebody happened to need a bundle measurement. So the
# list is now DERIVED and asserted: test/repo/check_closure_test.go reads ci-required's `needs:`,
# extracts the `make <target>` each blocking job runs, resolves this target's transitive
# prerequisites, and fails on a required target that is in neither the closure nor its reviewed
# exemption table — and on a blocking job that invokes no `make` target at all without a row of its
# own, which is how `security / osv` would otherwise have sat outside the model while the gate
# reported a complete sweep. Those tables — not this comment — are where an omission is argued.
#
# The four that joined here when the gate first ran, with the wall clock measured before deciding
# rather than after (warm cache, laptop): verify-generated 5 s, verify-spec 0.5 s, docs-build +
# docs-links 0.3 s. `make gen` producing a diff is the single most common "green locally, red in CI"
# outcome in this repository, and `check` said nothing about it for the whole of Phase 0.
#
# verify-generated and verify-spec sit after `vet` because both need a compiler and are cheap; the
# docs pair sits before budget-bundle because budget-bundle must stay LAST for the reason given
# above. What stayed out — `build`, which would delete the tracked internal/ui/dist placeholders on
# every run, and the CI lanes `make test` already covers — is a row in that table with its reason.
#
# ONE LINE, however long it gets: test/repo reads this rule's prerequisites as text, and a
# backslash-continued list parses as a prerequisite called `\` plus a second line nothing reads.
check: verify-commands mod-tidy-check lint vet verify-generated verify-spec test test-coverage-floor docs-build docs-links budget-bundle
	@printf '\033[32m  make check complete\033[0m\n'

## check-fast: the inner loop — the laws, the linters and the type checkers, NO test suite (~25s)
# For the edit-compile-lint cycle, and for that only. `make check` above is still the gate: run it
# before claiming a task is done, and the pre-push hook runs the formatting half of it whatever you
# type here (issue #159).
#
# What it deliberately does NOT include, since a fast target nobody trusts is worse than no fast
# target: the whole test suite, the coverage floor, the licence gate (a `go list` over the module
# graph) and eslint (a pnpm install away). What it DOES include is everything that answers "is this
# tree even coherent" — lint-repo is the architectural laws, one compiled Go binary over the tree and
# still the cheapest gate in the repository; lint-go is gofumpt plus golangci-lint; vet is build +
# go vet + staticcheck + tsc.
#
# Ordered cheapest-first on purpose: a law violation should be the first thing you read, not the
# thing you find after waiting for tsc.
#
# lint-actions and lint-shell joined the list with issues #121 and #122: together they are under a
# second, they need no network, and the files they read — the workflows and the shell gates — are
# exactly the ones whose breakage otherwise surfaces as a CI job failing for a reason that has
# nothing to do with the change.
check-fast: lint-repo lint-actions lint-shell lint-go vet
	@printf '\033[32m  make check-fast complete\033[0m — run \033[36mmake check\033[0m before you call it done\n'

## status: which targets are still stubbed, and the roadmap phase that fills each in
# Derived from the `notyet` call sites — never hand-maintained. A target that starts doing real
# work drops off this list on its own, with nobody remembering to update anything.
status:
	@printf 'Dragon Kill Party — targets not yet implemented\n\n'
	@awk '/^[a-zA-Z][a-zA-Z0-9_-]*:/ { t = $$0; sub(/:.*/, "", t) } \
	      /\$$\(call notyet,/ { p = $$0; sub(/^.*call notyet,/, "", p); sub(/,.*$$/, "", p); n++; \
	        printf "  \033[33m%-24s\033[0m %s\n", t, p } \
	      END { printf "\n  %d stubbed target(s); every other target does real work.\n", n }' \
	    $(MAKEFILE_LIST)
	@printf '  Planned vs implemented: ROADMAP.md and docs/development/first-ten-prs.md\n'

## clean: remove build output
# .cache/ is the per-checkout golangci-lint cache (issue #117). It is build output in every sense
# that matters here — derived, reproducible, and never committed — so `make clean` takes it.
clean:
	@rm -rf $(BUILD_DIR) web/dist coverage.out .cache

# ---------------------------------------------------------------------------
# CI-called targets. Deliberately absent from the AGENTS.md canonical table:
# agents do not run these by hand, CI does. verify-commands asserts that every
# table row resolves to a target, not that every target has a row.
# ---------------------------------------------------------------------------

# `env -u DKP_REPO_ROOT` is not decoration. The gate scripts honour that variable so their negative
# fixtures can run against a tainted tree in t.TempDir() — but an existing-but-empty directory makes
# every gate skip vacuously and still print "repo gates passed". Stripping it here means the only
# way to point the gates somewhere else is to invoke the script directly, which is what the tests
# do and what a CI job has no reason to do.
lint-repo:
	@env -u DKP_REPO_ROOT bash scripts/repo-gates.sh

# Atlas, the one pinned tool that cannot be `go install`ed. Called by `make setup` above AND by
# .github/actions/setup-toolchain — through this target rather than the script directly, so that
# GOTOOLS_BIN is computed in exactly one place. The action re-deriving an install directory is the
# drift this indirection removes: CI would install to one path and put a different one on PATH.
#
# `env -u DKP_REPO_ROOT` for the same reason as lint-repo: the script honours that variable so a
# test can point it at a fabricated Makefile/atlas.sha256 pair in t.TempDir() and assert that a pin
# with no checksum row fails before the network is touched. A value leaking in from a developer's
# shell would otherwise make `make setup` read a version out of some other tree.
install-atlas:
	@env -u DKP_REPO_ROOT bash scripts/install-atlas.sh '$(GOTOOLS_BIN)'

# shellcheck, the second pinned tool that cannot be `go install`ed (it is Haskell). Same shape as
# install-atlas above, same reasons, same `env -u DKP_REPO_ROOT`: the script honours that variable so
# a test can point it at a fabricated Makefile/shellcheck.sha256 pair in t.TempDir() and assert that
# a pin with no checksum row fails before the network is touched.
install-shellcheck:
	@env -u DKP_REPO_ROOT bash scripts/install-shellcheck.sh '$(GOTOOLS_BIN)'

# The GitHub Actions workflow gate (issue #121) and the shell gate (issue #122).
#
# Both are `lint` prerequisites and both are their own path-filtered CI job — `lint / actions` and
# `lint / shell` — so a failure names its own category in the checks list rather than arriving inside
# a lint job about Go. `env -u DKP_REPO_ROOT` for the reason lint-repo gives: the scripts honour that
# variable so their negative fixtures can run against a tainted tree in t.TempDir(), and a value
# leaking in from a developer's shell would lint somebody else's directory while reporting success.
lint-actions:
	@env -u DKP_REPO_ROOT bash scripts/lint-actions.sh

lint-shell:
	@env -u DKP_REPO_ROOT bash scripts/lint-shell.sh

# The architectural laws read against TYPE information, over a tree that BUILDS (issue #172).
#
# ADVISORY: scripts/typed-laws.sh prints findings and exits 0 in its default MODE=advise. See that
# script's header for why it is advisory by construction rather than by `continue-on-error`, and why
# a BROKEN invocation still hard-fails in both modes. This is the posture `make lint-migrations`
# landed in under issue #131 and left under #136, and the path is the same one: advisory first while
# the analyzers are unproven against this tree, promoted on evidence rather than on schedule. Issue
# #241 carries what has to be true before that happens here.
#
# ADDITIVE, never a replacement. Every rule in internal/repogate stays exactly where ADR-0018 put it
# and `make lint-repo` stays the merge-blocking gate: it needs no build, so it reads a deliberately
# tainted tree in t.TempDir() and a repository mid-sequence, which is the property that makes it the
# gate rather than the second opinion. What this pass adds is the three classes a syntax rule cannot
# have — a dot-imported `. "time"`, a type alias reaching *sql.DB, an untyped `0.15` in the point
# path. TestTypedLaws_AreAdditive in test/repo says so in code.
#
# It is a `lint` prerequisite, and therefore in `make check`, because it costs one `go list -export`
# over a warm build cache (~0.7 s here) and the person who can act on a law they just broke is the
# one still holding the file. Deliberately NOT in `check-fast`: that lane runs no build.
#
# `env -u DKP_REPO_ROOT` for the reason lint-repo gives: the script honours that variable so its
# negative fixtures can run against a fabricated module in t.TempDir(), and a value leaking in from a
# developer's shell would analyse somebody else's tree while reporting success.
lint-laws:
	@env -u DKP_REPO_ROOT bash scripts/typed-laws.sh

# go.mod and go.sum must ALREADY be tidy (issue #149).
#
# Nothing asserted this, so tidiness was whoever last happened to run `go mod tidy` — and the cleanup
# always arrived attached to an unrelated change. Adding one direct requirement in #126 pruned ~100
# stale `/go.mod` hash lines from go.sum (ClickHouse, docker, gin, antlr — goose's optional-driver
# graph, none of which this project builds) inside a PR about HTML parsing, where they were pure
# noise a reviewer had to read past and decide was safe. The other direction matters more: a PR that
# adds a dependency WITHOUT tidying leaves the graph and the lock disagreeing and nothing says so.
#
# THE TWO FAILURES ARE REPORTED SEPARATELY, and that is the whole care in this recipe. `go mod tidy`
# itself can fail — a module the cache does not hold and no network to fetch it, a broken import — and
# reporting that as "your go.mod is untidy" would send somebody to run the command that just failed.
# So its status is checked first, its output is shown, and only then are the files compared.
#
# The originals are restored WHATEVER happens, including on the failure path: this target must not be
# able to leave a developer's tree rewritten. `cp` rather than `git stash` because it has to work on
# a tree with staged and unstaged edits, which is the normal state of the thing it runs inside.
# The backups live in a mktemp directory rather than beside their originals: a `go.mod.bak` in the
# repository root is a file every gate in test/repo would have to learn to ignore, and one that an
# interrupted run leaves behind for `git status` to report.
mod-tidy-check:
	@tmp=$$(mktemp -d); \
	trap 'cp "$$tmp/go.mod" go.mod; cp "$$tmp/go.sum" go.sum; rm -rf "$$tmp"' EXIT INT TERM; \
	cp go.mod "$$tmp/go.mod"; cp go.sum "$$tmp/go.sum"; \
	if ! out=$$($(GO) mod tidy 2>&1); then \
		printf '\033[31m  go mod tidy FAILED\033[0m — this is not a tidiness failure, and running\n'; \
		printf '  `go mod tidy` yourself will not fix it. What it said:\n%s\n' "$$out"; \
		exit 1; \
	fi; \
	if cmp -s go.mod "$$tmp/go.mod" && cmp -s go.sum "$$tmp/go.sum"; then \
		printf '  \033[32mgo.mod and go.sum are tidy\033[0m\n'; \
		exit 0; \
	fi; \
	printf '\033[31m  go.mod or go.sum is not tidy\033[0m — run `go mod tidy` and commit the diff:\n'; \
	diff -u "$$tmp/go.mod" go.mod || true; \
	diff -u "$$tmp/go.sum" go.sum | head -40 || true; \
	exit 1

# The dependency licence gate. Same `env -u` reasoning as lint-repo above: its negative fixtures
# point DKP_REPO_ROOT at a fabricated module tree in t.TempDir(), and a value leaking in from a
# developer's shell would make `make check` inspect the wrong graph while printing that it passed.
#
# In `lint` rather than a job of its own because it is one `go list` plus a classifier, and a licence
# violation should stop a laptop before it stops CI. It is the cheapest half of the supply-chain
# gates; govulncheck below is the expensive half.
# GOFLAGS is stripped alongside DKP_REPO_ROOT: it can carry -mod=vendor or -tags, either of which
# changes which modules `go list` resolves. A developer's environment must not decide which
# dependency graph the licence gate inspects.
#
# Go rather than shell since #130: the classifier is pure text matching, and in internal/licence its
# patterns are unit-tested directly instead of only through a subprocess. It is BUILT and run rather
# than `go run`, per ADR-0022 — see the go_gate macro at the top of this file. The compile lands in
# the build cache either way, so the invocation costs nothing over the `go list` calls the gate must
# make anyway, and a LIC001 finding is no longer followed by `exit status 1`.
licence-gate:
	@$(call go_gate,./internal/licence/cmd/licence,gate,-u GOFLAGS)

# Hard-fails when golangci-lint is missing. Exiting 0 would report a green `make check` that linted
# nothing, which is the defect this target exists to prevent.
# Resolved and invoked inside ONE shell block, via a variable, on purpose.
#
# A recipe line that is a single simple command is exec'd by make directly, without a shell, using
# the PATH make started with — so the `export PATH` above does not apply to it and a bare
# `golangci-lint run` fails with "make: golangci-lint: No such file or directory" even though the
# line above just found it. Keeping the lookup and the call in one `$$(...)`-using block forces the
# shell and makes the export effective. Do not "tidy" this into a bare command.
#
# GOLANGCI_LINT_CACHE IS SCOPED TO THIS CHECKOUT (issue #117), and it is a correctness fix rather
# than a tidy-up. Unset, golangci-lint uses ONE per-user cache — ~/Library/Caches/golangci-lint on
# macOS — so an analysis result cached by any checkout is reachable from every other one, and the
# cached entry still carries the ABSOLUTE PATH of the tree that produced it. Delete that tree, which
# this project's agent worktrees do continuously, and the generated-file-filter pass cannot open the
# file it is asked about. What it prints is a warning with the whole issue struct inlined:
#
#   level=warning msg="[runner] Can't process results by generated_file_filter processor: ...
#   FromLinter:\"forbidigo\", Text:\"use of `float64` forbidden because \\\"float32/float64 are
#   banned in internal/ledger and internal/strategy (canonical §1) ...\"
#   Filename:\"/Users/…/dragonkillparty-8/internal/core/centipoints.go\" ... no such file"
#
# — a stale-cache warning that quotes the ledger's float ban and names internal/core/centipoints.go,
# in a run that ends `0 issues.` on a clean tree. An environment fault wearing a content fault's
# clothes, which test/repo/python_floor_test.go already names as the most expensive shape a gate
# failure has. The natural next move is to hunt for a §1 violation that does not exist.
#
# $(CURDIR), so two worktrees of this repository cannot serve each other a cached path. CI keeps its
# hits: every Go job there runs in a fresh checkout whose cache is restored per job by the key in
# .github/actions/setup-toolchain, and this directory is under the workspace either way. `?=` so a
# deliberate GOLANGCI_LINT_CACHE from the environment still wins.
GOLANGCI_LINT_CACHE ?= $(CURDIR)/.cache/golangci-lint
export GOLANGCI_LINT_CACHE

lint-go:
	@bin=$$(command -v golangci-lint 2>/dev/null || echo '$(GOTOOLS_BIN)/golangci-lint'); \
	[ -x "$$bin" ] || { \
		printf '\033[31m  golangci-lint not installed — run make setup\033[0m\n'; exit 1; }; \
	"$$bin" run

# `lint` scopes eslint to src/** (the negative fixtures are in eslint.config.js's `ignores`, so a
# bare `eslint .` stays green). `lint:fixtures` is the negative gate: it runs eslint --no-ignore over
# web/test-fixtures/lint/ and FAILS if a deliberate law-4 violation is NOT caught — the property that
# keeps the AST-aware half of law 4 from silently going blind. Both run in CI's `lint / web` job.
#
# web-deps for the same reason `vet` has it: eslint and every plugin it loads come out of
# node_modules, so a merge that adds one would fail here with a module-resolution error against a
# tree the reader did not change. Make runs the install once for the whole `make check`.
lint-web: web-deps
	@if [ -f web/package.json ]; then \
		cd web && pnpm run lint && pnpm run lint:fixtures; \
	else $(call notyet,Phase 0 PR 6,web/ is not scaffolded yet); fi

# Reachable-vulnerability analysis. Same one-shell-block idiom and the same hard-fail-when-missing
# rule as lint-go: a govulncheck that exits 0 because the binary is absent is worse than no gate,
# because CI would report the scan as green.
#
# Deliberately NOT in `lint` or `check`, and the reason is network, not time — it runs in about
# four seconds. It fetches the vulnerability database from vuln.go.dev, and `make check` is expected
# to work on a laptop with no connectivity; a check that fails on a train teaches people to skip it.
# CI runs it as its own required job; run it by hand before proposing a dependency.
govulncheck:
	@bin=$$(command -v govulncheck 2>/dev/null || echo '$(GOTOOLS_BIN)/govulncheck'); \
	[ -x "$$bin" ] || { \
		printf '\033[31m  govulncheck not installed — run make setup\033[0m\n'; exit 1; }; \
	"$$bin" ./...

# Known vulnerabilities across BOTH dependency graphs, from the OSV database (issue #132).
#
# The two supply-chain scanners are complementary and neither replaces the other:
#
#   govulncheck    Go only, REACHABILITY-aware. Call-graph analysis, so it reports a vulnerability
#                  only when this module's code can actually reach it. High signal, narrow scope.
#   osv-scan       Go AND npm, reachability-BLIND. Reports every advisory affecting a pinned
#                  version. Lower signal on the Go side — which is exactly why govulncheck stays —
#                  and the ONLY coverage the ~275 package npm graph in web/pnpm-lock.yaml has.
#
# The npm graph is the gap this closes. `security / licences` already reads that graph for LICENCES;
# nothing read it for VULNERABILITIES until now, and the first scan found three (issues #133, #134,
# #135 — all transitive devDependencies, all waived with an expiry in osv-scanner.toml).
#
# BOTH lockfiles are named explicitly rather than letting `-r .` walk the tree. A recursive walk
# quietly scans whatever it happens to find, so a lockfile that moved or a manifest that stopped
# being generated reads as "clean" instead of "not scanned"; naming them means osv-scanner itself
# errors when one is missing. TestOSV_ScanTargets_AreTheSameInCIAndTheMakefile asserts this list and
# ci.yml's agree — two copies of a scan target is how one of them silently stops covering anything.
#
# Same hard-fail-when-missing rule as govulncheck and lint-go: a scanner that exits 0 because the
# binary is absent reports a clean tree it never read. NOT in `lint` or `check`, for govulncheck's
# reason exactly — it queries api.osv.dev, and `make check` must work on a laptop with no network.
# CI runs it as its own required job; run it by hand before proposing a dependency.
osv-scan:
	@bin=$$(command -v osv-scanner 2>/dev/null || echo '$(GOTOOLS_BIN)/osv-scanner'); \
	[ -x "$$bin" ] || { \
		printf '\033[31m  osv-scanner not installed\033[0m — `make setup` does not install it.\n'; \
		printf '  CI runs it as a pinned container action; install $(OSV_SCANNER_VERSION) locally with:\n'; \
		printf '    go install github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION)\n'; \
		printf '  (needs a Go patch release at or above the one osv-scanner pins, which is why\n'; \
		printf '   `make setup` leaves it out — see OSV_SCANNER_VERSION above.)\n'; \
		exit 1; }; \
	"$$bin" scan source --config=osv-scanner.toml --lockfile=go.mod --lockfile=web/pnpm-lock.yaml

# Atlas's own migration analyzers over db/migrations-sqlite/ — destructive changes, data-dependent
# changes and backward-incompatible changes (issue #131). A GATE since issue #136: a diagnostic
# fails, here and in CI, because `make lint` is what `make check` runs and `test / migrations` runs
# this same target. See the script's header for the evidence the promotion waited on, for the
# `-- atlas:nolint` waiver, and for why a BROKEN invocation hard-fails in either mode.
#
# MODE=enforce is stated HERE as well as defaulted in the script, so which mode CI runs is legible
# from the target a reader is already looking at. It also means `make check` on a laptop and
# `test / migrations` reach the same verdict about the same branch — a gate that only fires in CI
# costs a push, a round trip and a contributor who did exactly what AGENTS.md told them (#166, #183).
# To read diagnostics without being blocked by them, run the script directly: MODE=advise bash
# scripts/migrate-lint.sh.
#
# ADDITIVE, never a replacement. MIG001 (DDL in a Down block), MIG002 (backtick identifiers), MIG003
# (SHIPPED.lock), TestMigrate_FreshInstall_MatchesFingerprint and the populated-upgrade suite all
# stay: Atlas lint knows nothing about forward-only migrations, a frozen-shipped-migration file, or
# append-only triggers surviving a table rebuild. TestMigrationGates_AtlasLint_IsAdditive says so in
# code.
#
# `env -u DKP_REPO_ROOT` for the same reason as lint-repo and licence-gate: the script honours that
# variable so test/repo can point it at a fabricated migration directory in t.TempDir(), and a value
# leaking in from a developer's shell would make `make check` analyse some other tree while printing
# that it passed.
#
# DKP_ATLAS names the PINNED atlas by path (issue #254). PATH is appended above so "a deliberately
# chosen system tool still wins", which is right for every tool here except this one: `atlas migrate
# lint` is Pro-only from v0.38 in Atlas's OFFICIAL build, so a system atlas does not merely differ
# from the pin, it makes this target impossible to run — and Atlas's own error banner is what talks
# contributors into installing it. GOTOOLS_BIN is still derived in exactly one place; this passes
# that value on rather than re-deriving it, the same contract install-atlas.sh takes it under. The
# script falls back to PATH when the pinned copy is absent, and refuses a non-community atlas either
# way, so this line is the preference and the check in the script is the guarantee.
lint-migrations:
	@env -u DKP_REPO_ROOT DKP_ATLAS='$(GOTOOLS_BIN)/atlas' MODE=enforce bash scripts/migrate-lint.sh

# The template-database clone cost, printed as a p50 in the CI log. This is the measurement behind
# item V4 of docs/development/verify-before-phase-0.md — "integration tests are nearly free" is the
# thesis the whole test pyramid inverts on, and it is either true at a fraction of a millisecond or
# the pyramid needs redrawing.
#
# -benchtime=1x -count=200 gives 200 INDEPENDENT samples rather than one mean over 200 iterations,
# which is what a median needs. The median is taken in awk rather than in Go on purpose: computing
# it in Go would need time.Now, which CLOCK001 bans outside internal/clock, and the testing package
# is already timing this correctly.
#
# `set -o pipefail` is not decoration. Without it the recipe's status is the last awk's, so a panic,
# a data race or a goleak failure AFTER the first sample has printed leaves the step green while
# reporting a healthy p50 — a benchmark that reports a number for a run that crashed.
bench-clone:
	@set -o pipefail; $(GO) test -run '^$$' -bench '^BenchmarkNewDB_Clone$$' -benchtime=1x -count=200 \
		./internal/store \
		| awk '/^BenchmarkNewDB_Clone/ { print $$3 }' \
		| sort -n \
		| awk '{ ns[NR] = $$1 } \
		       END { if (NR == 0) { print "  no benchmark samples — did BenchmarkNewDB_Clone run?"; exit 1 } \
		             printf "  BenchmarkNewDB_Clone  p50 %.3f ms  (p95 %.3f ms, n=%d)\n", \
		                    ns[int((NR + 1) / 2)] / 1e6, ns[int(NR * 0.95)] / 1e6, NR }'

# Currently an alias for lint-repo, and knowingly so: PIN001 in repo-gates.sh is the only pin check
# that exists. It asserts every `uses:` carries a 40-character SHA — the SHAPE of a pin.
#
# What it does NOT do is verify that a digest is the commit its trailing `# v7.0.1` comment claims.
# A wrong-but-well-formed digest passes. Closing that needs authenticated, rate-limited GitHub API
# calls from a lint job, which makes the gate non-hermetic and is its own design question; the
# header of .github/workflows/ci.yml says so rather than promising it. Kept as a separate target so
# that work has somewhere to land without changing what CI calls.
verify-action-pins:
	@env -u DKP_REPO_ROOT bash scripts/repo-gates.sh

docs-build:
	@if [ -f docs/package.json ]; then cd docs && pnpm run build; \
	else $(call notyet,Phase 0 PR 11,the Astro/Starlight site is not scaffolded yet); fi

docs-links:
	@python3 scripts/check-links.py

# mockup-site: build the publishable UI-mockup site into _site/ (.github/workflows/pages.yml).
# Deliberately absent from the AGENTS.md canonical table — it is a docs artefact, not part of the
# inner loop. See docs/design/mockups/README.md.
#
# Go, not shell, since issue #126. The job is entirely "parse HTML, rewrite it, then assert
# properties of the result", and the scripts/dc-publish.py + scripts/build-mockup-site.sh pair did
# that with a hand-written tag scanner, quote-state tracking and four greps. internal/mockup does it
# with golang.org/x/net/html; test/repo/mockup_gates_test.go drives MOCK001-004 against negative
# fixtures, which no version of the shell gate was ever able to do.
mockup-site:
	@$(GO) run ./internal/mockup/sitegen

# verify-spec: assert the properties of openapi/openapi.json that regenerating it cannot establish.
#
# This stub used to say "vacuum lint". It is not vacuum, and the reason is worth recording rather
# than quietly changing: the properties .github/workflows/ci.yml's spec-drift job specifies —
# operationId casing and stability, x-dkp-permission resolving against the authz catalogue, money as
# unquoted integer centipoints — are DKP rules that no general-purpose OpenAPI linter knows. vacuum's
# own job (an operation must carry a summary, a description and an example, per
# docs/design/07-documentation-system.md §"Gates") is documentation quality, and it belongs with
# vale and lychee in Phase 0 PR 11 where the docs site and its gates land. Adding it here would have
# meant a third pinned tool that did not answer the question the job asks.
#
# GO, NOT PYTHON, since issue #127. This was `python3 scripts/verify-spec.py`, and issue #83 is what
# that cost: an interpreter below the floor made the script raise at import — before it read a byte of
# the spec — so `make check` reported the SPEC GATE failing on a tree whose spec was fine, pointing at
# openapi/openapi.json, the one file nobody may hand-edit. `go` is pinned by go.mod and GOTOOLCHAIN is
# local, so the gate cannot now fail for a reason that has nothing to do with its subject. It is built
# and run rather than `go run` (ADR-0022): the package imports only the standard library, so the
# compile is well under a second and needs nothing in `make setup` either way.
#
# `env -u DKP_REPO_ROOT` for the reason given above lint-repo. DKP_SPEC_BASE_REF is stripped for the
# same class of reason: an empty value disables the operationId-rename check, and that switch exists
# only so the negative fixtures in test/repo can run against a tree with no git history. A value
# leaking in from a developer's shell would turn a merge-blocking gate green.
verify-spec:
	@$(call go_gate,./internal/specgate/verifyspec,,-u DKP_SPEC_BASE_REF)

## eval-example-endpoint: LOCAL agent-eval of the two worked-example documents
# Hands a fresh agent session nothing but the repo plus internal/api/EXAMPLE_ENDPOINT.md and
# db/RECIPES.md, and one instruction (add GET /api/v1/guild/settings). Passes when the resulting tree
# has `make check` and the spec-drift gate green.
#
# LOCAL-ONLY, ON PURPOSE (decision record §U5). There is deliberately NO nightly workflow and NO CI
# secret: which runner, whose API key and what budget are unsettled, and the nightly lane lands with
# the release train in Phase 0 PR 7 rather than shipping a workflow that needs a key nobody agreed to.
# It needs an agent CLI on PATH; set DKP_EVAL_RUNNER to yours. See the script header for the contract.
eval-example-endpoint:
	@env -u DKP_REPO_ROOT bash scripts/eval-example-endpoint.sh

# The documentation snippet-compile gate runs inside `make test` — it is
# TestDocs_ExampleEndpointSnippets_Compile in internal/api, which extracts every fenced block from the
# two worked-example documents and rebuilds it (Go blocks `go build`, SQL blocks pass sqlc, HCL blocks
# pass `atlas schema inspect`). It has no target of its own because it is an ordinary Go test that
# `make test` already runs; it skips under -short so `make test-unit` does not pay it.

api-breaking:
	@$(call notyet,Phase 2,oasdiff against main's openapi.json)

api-changelog-comment:
	@$(call notyet,Phase 2,sticky PR comment summarising the spec diff)

# The bundle budget BUILDS the thing it measures, and that is the whole target (issue #166).
#
# scripts/budget-bundle.sh falls back to internal/ui/dist when web/dist is absent, and what lives
# there in a clean checkout is the committed placeholder — a few hundred bytes. So the CI job and any
# laptop invocation were measuring the placeholder and reporting a pass with 99% headroom: a required
# gate whose subject was not the bundle. The measurement that closed #133/#134/#135 had to be taken by
# hand as `make build && make budget-bundle` for exactly that reason.
#
# DKP_WEB_STAGE=0 because the budget reads web/dist and nothing else: full staging would delete the
# tracked internal/ui/dist placeholders and leave `make check` with a dirty tree every run. web-deps
# is a prerequisite rather than an install inside the recipe, so `make check` still pays for one
# install across lint, vet and this.
budget-bundle: web-deps
	@DKP_WEB_STAGE=0 bash scripts/build-web.sh
	@bash scripts/budget-bundle.sh

verify-postgres:
	@$(call notyet,post-1.0,nightly Postgres schema convergence)

# verify-ledger — seed a guild-scale ledger and replay it (issue #198). Called by nightly-verify.yml's
# `replay / seed.Perf`, which is the job ROADMAP Phase 1 item 9 names.
#
# NOT a row in AGENTS.md's command table, like verify-postgres above it and verify-fonts below: the
# hand-runnable form is `dkp verify-ledger` against an operator's own database, and this target is the
# CI orchestration around it — build, migrate, seed, replay, in a directory it created itself.
#
# THE SEED IS THE POINT. A replay of an empty database is clean, instantly, and proves nothing; what
# this asserts is that half a million entries written through ledger.Service.Commit — the invariant
# engine, the seq allocator, the hash chain and the synchronous balance_snapshot upsert — still
# recompute to themselves and still reproduce every cached balance exactly. That is the property
# .claude/rules/seed-profiles.md says the Perf profile exists to make checkable.
#
# RAIDS sizes it, the same one-knob shape `make seed` and DKP_PERF_RAIDS use: the full 3,400-raid
# profile by default (~90 s to seed, ~4 s to replay, ~250 MB), and `make verify-ledger RAIDS=200` for
# a quick local run. There is no second code path at either size.
#
# It writes into a fresh mktemp directory rather than ./data, because seeding is not undoable: the
# generator refuses a pool that already has batches, so pointing this at a developer's dev database
# would fail on the second run and, worse, would be pointing a test at a file somebody was using.
# The directory is removed on the way out, including on failure.
#
# go_build_bin rather than `build`, for the reason `seed` gives: staging the SPA deletes the tracked
# internal/ui/dist placeholders and leaves the working tree dirty, and nothing here serves HTTP.
#
# `set -euo pipefail` before anything else, because every step here is a precondition for the next
# one and the verdict is the LAST command's exit code: without -e a failed migrate would run the
# seed anyway, and without pipefail a failed seed would be masked by the `tail` it is piped into —
# either way the replay would run against a database that is not what this target claims to have
# built. The trap still fires on the way out, so the scratch directory goes whichever way it ends.
verify-ledger:
	@$(go_build_bin)
	@set -euo pipefail; \
	dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
	export DKP_DB_PATH="$$dir/verify.db"; \
	$(BUILD_DIR)/$(BIN) migrate >/dev/null; \
	$(BUILD_DIR)/$(BIN) seed --profile perf $${RAIDS:+--raids $$RAIDS} | tail -n 1; \
	$(BUILD_DIR)/$(BIN) verify-ledger --quiet

test-golden:
	@$(call notyet,Phase 4,parser golden files)

test-authz:
	@$(call notyet,Phase 2,the authorization matrix)

# test-property and test-coverage-floor used to live down here as stubs. They do real work now and
# have moved above the divider with the rest of the hand-runnable targets, because an agent that has
# just touched internal/ledger or internal/strategy should run both before claiming a task is done.

# The migration suite: fresh-install fingerprint, STRICT and no-guild_id assertions, the
# snapshot/auto-restore path, the downgrade refusal, and the upgrade of a POPULATED ledger across a
# table rebuild. Not -short: every one of these applies real migrations to a real database, which is
# the only way any of them mean anything.
#
# Cacheable (issue #155): the suite drives the migrator in-process and reads the migration set
# through go:embed, which is compiled in — a changed migration changes the build id and misses the
# cache. Nothing here reaches its subject through a subprocess.
test-migrations:
	@$(GO) test $(TEST_CACHE_FLAGS) ./test/migrations/...

# test-e2e used to live here as a stub. It does real work now and has moved above the divider with
# the rest of the hand-runnable targets, for the same reason test-property and test-coverage-floor
# did — and because docs/design/04-testing.md says that when E2E earns a local target, the AGENTS.md
# row ships in the same PR.

# Phase 8, not Phase 0 PR 3, and the correction is deliberate.
#
# This target upgrades the PREVIOUS RELEASE's reference database to HEAD. No reference database
# exists: `release-refdb` below is what publishes ghcr.io/prokopto-dev/dkp-refdb:<version>, and it
# is a Phase 8 stub. Phase 0 PR 3 built the migrator, the snapshot and the auto-restore — it could
# not build an upgrade test against an artefact no release has ever produced, and saying otherwise
# here would be the same defect that #5 and #6 were cleanups of.
#
# What PR 3 DID land is `test-migrations` above, which proves the forward path and the restore path
# against a real database. The N-1 ladder is a different guarantee and needs a published refdb.
#
# NO CI JOB CALLS THIS. Its only caller was mq/upgrade-from-latest-release, deleted with the rest of
# the dead merge-queue configuration (issue #101) — it never ran, because merge_group never fires on
# a repository with no queue. Issue #109 decides where it runs once there is a refdb to pull.
test-upgrade:
	@$(call notyet,Phase 8,needs release-refdb to publish a reference database first)

test-upgrade-ladder:
	@$(call notyet,Phase 8,every published minor upgraded to HEAD)

# The upgrade ladder's matrix source. TWO consumers, and the difference is issue #255:
#
#   stdout          a human running this locally, and any script that pipes it
#   $GITHUB_OUTPUT  `versions`, which nightly-verify.yml's `upgrade-ladder-enumerate` job declares
#                   as an output and `upgrade-ladder` expands with fromJSON
#
# Writing only stdout left `versions` UNSET, so the matrix expanded `fromJSON('')` — an expression
# error, which GitHub reports against the RUN rather than against a job. Every nightly since the
# ladder was wired concluded `failure` with eighteen green jobs, no red check-run, and no
# `upgrade-ladder` entry at all: nothing for a maintainer to click. Emit BOTH, and emit valid JSON
# even when the answer is "nothing to ladder" — `[]` before the first release, which is the state
# this repository is in until Phase 8 publishes one.
#
# The guarded append is scripts/release-manifest.sh's shape, for its reason: the producer owns the
# CI contract, so a caller cannot forget it, and the write is skipped outside Actions rather than
# failing on an unset variable. Phase 8 replaces the `[]` with the real enumeration and KEEPS both
# halves; test/repo/upgrade_ladder_matrix_test.go fails if it does not.
upgrade-ladder-enumerate:
	@versions='[]'; \
	echo "$$versions"; \
	if [ -n "$${GITHUB_OUTPUT:-}" ]; then echo "versions=$$versions" >>"$$GITHUB_OUTPUT"; fi

soak-jobs:
	@$(call notyet,Phase 1,10k-job River soak)

smoke-local:
	@bash scripts/smoke-local.sh

nightly-report:
	@$(call notyet,Phase 8,nightly summary issue)

fixture-build:
	@$(call notyet,Phase 0 parallel lane,run the real EQdkp PHP installers in Docker)

fixture-seed:
	@$(call notyet,Phase 0 parallel lane,seed synthetic guild data into a fixture)

fixture-capture:
	@$(call notyet,Phase 0 parallel lane,snapshot the MariaDB data directory)

fixture-verify:
	@$(call notyet,Phase 0 parallel lane,assert a fixture matches its manifest)

fixture-manifest:
	@$(call notyet,Phase 0 parallel lane,write the fixture manifest)

fixture-publish:
	@$(call notyet,Phase 0 parallel lane,push fixtures to GHCR as OCI artifacts)

fixture-gate:
	@$(call notyet,Phase 5,require a fixture for every importer change)

release-version:
	@$(call notyet,Phase 8,derive the version from the tag)

release-notes:
	@$(call notyet,Phase 8,generate notes from oasdiff + migrations + BREAKING trailers)

release-image:
	@bash scripts/release-image.sh

release-manifest:
	@bash scripts/release-manifest.sh

release-sign:
	@bash scripts/release-sign.sh

release-sbom:
	@bash scripts/release-sbom.sh

release-smoke:
	@bash scripts/release-smoke.sh

release-promote:
	@bash scripts/release-promote.sh

# db/migrations-sqlite/SHIPPED.lock — the append-only record of which migrations have already run on
# a user's database. Two halves, and they ask different questions on purpose:
#
#   shipped-lock-seal      appends a row for every migration not yet listed. Run when preparing a
#                          release, so the manifest is sealed IN the Release PR and reviewed by the
#                          human who merges it. CI cannot write it: nothing pushes to main, so the
#                          release job verifies rather than seals.
#   release-shipped-lock   the release-path gate, called by release.yml's `prepare` job before any
#                          image, binary or tag exists. Every listed hash must still match AND every
#                          migration present at the tag must be listed — at a tag, everything present
#                          ships, so an unsealed manifest is a hole in the record and fails here.
#
# The per-PR half is MIG003 in scripts/repo-gates.sh, which runs the same command WITHOUT
# --complete: a migration added on a feature branch has not shipped and must not be listed yet.
#
# `env -u DKP_REPO_ROOT` for the reason given above lint-repo: the command honours that variable so
# its negative fixtures can run against a fabricated tree in t.TempDir(), and a value leaking in
# from a developer's shell would otherwise verify some other directory's manifest while printing
# that it passed.
#
# Both go through go_gate rather than `go run` (ADR-0022, issue #180). `release-shipped-lock` is the
# caller that decision's first argument is about: it runs in release.yml's `prepare` job, before any
# image, binary, attestation or moving tag exists, and it is the last point at which a missing row in
# db/migrations-sqlite/SHIPPED.lock is free to fix. "The manifest is incomplete" and "the job
# mistyped the command" arriving identically in a release log is the cost the macro removes.
shipped-lock-seal:
	@$(call go_gate,./internal/migrate/shippedlock,seal)

release-shipped-lock:
	@$(call go_gate,./internal/migrate/shippedlock,verify --complete)

# Regenerate THIRD_PARTY_NOTICES.txt from the runtime dependency graph. NOTICE promises this file and
# .goreleaser.yaml attaches it to every release archive; TestThirdPartyNotices_* asserts it covers the
# graph. Stdlib + the module cache only — no network, no extra tool. Run it after any go.mod change.
third-party-notices:
	@bash scripts/third-party-notices.sh

# Re-cut the vendored Inter faces as a Latin subset, and prove the committed bytes are what the
# recipe produces. The faces under web/src/assets/fonts/ are DERIVED, so the derivation is pinned —
# input hashes, flags, fonttools and brotli versions — and byte-reproducible; see
# web/src/assets/fonts/README.md for the evidence. Both targets need network (a 33 MB upstream
# archive and two wheels), which is why verify-fonts runs nightly rather than in PR CI, and why
# neither is in `check`: test/repo/web_fonts_test.go carries the offline half of the gate.
# DKP_INTER_ZIP=/path/to/Inter-4.1.zip reuses an already-downloaded archive.
subset-fonts:
	@bash scripts/subset-fonts.sh

verify-fonts:
	@bash scripts/subset-fonts.sh --verify

# Advisory measurement of the compressed image against the 30 MB budget (docs/design section 7). The
# ci.yml build/image step runs this target advisory-by-construction — MODE=advise prints the number,
# warns on a breach, and exits 0 (NOT continue-on-error, which ci.yml bans). The release path
# (scripts/release-image.sh) runs the SAME script with MODE=enforce as a hard gate. Prints the number
# either way.
image-size:
	@bash scripts/image-size.sh

release-promote-rc:
	@$(call notyet,Phase 8,promote a release candidate)

release-refdb:
	@$(call notyet,Phase 8,publish the reference database for the upgrade ladder)

release-failure-issue:
	@$(call notyet,Phase 8,file an issue when a release job fails)
