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
IMAGE          ?= ghcr.io/dragonkillparty/dkp
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

# notyet <phase> <what> — a target that is declared but not yet implemented.
# No leading '@': call sites add it, so this also works inside shell if/else blocks.
define notyet
printf '\033[33m  not yet implemented\033[0m  %s\n  lands in: %s\n' "$(2)" "$(1)"
endef

.PHONY: help setup dev gen test-unit test test-importer lint vet migration seed docker check \
        build clean fmt verify-generated verify-commands \
        lint-repo lint-go lint-web licence-gate govulncheck bench-clone verify-action-pins \
        install-atlas generated-digest \
        docs-build docs-links verify-spec \
        api-breaking api-changelog-comment budget-bundle verify-postgres test-golden \
        test-property test-authz test-migrations test-e2e test-upgrade test-upgrade-ladder \
        upgrade-ladder-enumerate soak-jobs smoke-local nightly-report status \
        fixture-build fixture-seed fixture-capture fixture-verify fixture-manifest \
        fixture-publish fixture-gate release-version release-notes release-image \
        release-manifest release-sign release-sbom release-smoke release-promote \
        release-promote-rc release-refdb release-failure-issue \
        eval-example-endpoint third-party-notices image-size

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

## dev: run the dev servers — Go on :8080, Vite on :5173
dev:
	@command -v pnpm >/dev/null 2>&1 || { \
		printf '\033[31m  pnpm is not installed\033[0m — Node + pnpm are needed for the SPA. See make setup.\n'; exit 1; }
	@[ -d web/node_modules ] || { printf '  installing web dependencies\n'; (cd web && pnpm install --frozen-lockfile --ignore-scripts); }
	@printf '  Go API on :8080, Vite on :5173 — Ctrl-C stops both\n'
	@trap 'kill 0' EXIT INT TERM; \
		( DKP_DB_PATH=$${DKP_DB_PATH:-./data/dev.db} $(GO) run ./cmd/dkp serve --addr :8080 ) & \
		( cd web && pnpm run dev ) & \
		wait

## gen: regenerate ALL generated code — migrations, sqlc, openapi, clients
# Today: atlas.sum, the schema/migration sync assertion, sqlc, `dkp openapi ->
# openapi/openapi.json`, and openapi-typescript -> web/src/api/schema.d.ts. openapi-python-client and
# the SDKs land with the clients/ tree in a later phase; each arrives as another line here.
#
# `env -u DKP_REPO_ROOT` for the reason given above lint-repo: the script honours that variable so
# its negative fixtures can run against a tree in t.TempDir(), and a value leaking in from a
# developer's shell would regenerate somebody else's tree while reporting success here.
gen:
	@env -u DKP_REPO_ROOT bash scripts/gen-db.sh
	@env -u DKP_REPO_ROOT bash scripts/gen-openapi.sh
	@env -u DKP_REPO_ROOT bash scripts/gen-client.sh

## test-unit: fast unit tests only (budget < 5s)
test-unit:
	@$(GO) test -short -shuffle=on -count=1 $(PKG)

## test: integration tests against a real SQLite database (budget ~30s)
test:
	@$(GO) test -race -shuffle=on -count=1 $(PKG)

## test-importer: EQdkp Plus importer suite — needs Docker (budget ~120s)
test-importer:
	@$(call notyet,Phase 5,runs against real EQdkp 2.0.5/2.1.5/2.2.27/2.3.39 fixtures plus the hostile fixture)

## lint: the repo grep gates, the dependency licence gate, golangci-lint and eslint
lint: lint-repo licence-gate lint-go lint-web

## vet: build + go vet + staticcheck + tsc
# Runs build + vet, then tsc over the SPA. staticcheck is folded into golangci-lint, so it runs
# under `make lint`. AGENTS.md documents the composition.
vet:
	@$(GO) build $(PKG) && $(GO) vet $(PKG)
	@if [ -f web/package.json ]; then \
		[ -d web/node_modules ] || (cd web && pnpm install --frozen-lockfile --ignore-scripts); \
		(cd web && pnpm run typecheck); \
	fi

## fmt: gofumpt + goimports over the tree
fmt:
	@bin=$$(command -v gofumpt 2>/dev/null || echo '$(GOTOOLS_BIN)/gofumpt'); \
	if [ -x "$$bin" ]; then "$$bin" -l -w .; \
	else printf '\033[33m  gofumpt not installed — run make setup\033[0m\n'; fi

## migration: create a migration — make migration NAME=add_bid_hold
# The `ifndef` stays even though the script also checks NAME: it makes `make migration` with no
# argument fail at parse time with the usage line, before any tool runs.
migration:
ifndef NAME
	$(error NAME is required: make migration NAME=add_bid_hold)
endif
	@env -u DKP_REPO_ROOT NAME='$(NAME)' bash scripts/new-migration.sh

## seed: seed a dev guild — make seed PROFILE=demo (or small, perf)
seed:
	@$(call notyet,Phase 1 (perf) / Phase 2 (small) / Phase 3 (demo),dkp seed --profile $${PROFILE:-demo})

## docker: build the container image
# buildx, not `docker build`: it is what CI's build/image and mq/image-arm64 jobs invoke, and it is
# what the Dockerfile's cross-compilation ARGs (TARGETOS/TARGETARCH, supplied per platform) expect.
# `--load` puts the result in the local image store so `make smoke-local` and `make image-size` can
# find it. PLATFORM is optional: unset for a native host build (build/image), set to linux/arm64 for
# the merge-queue cross-compile check (mq/image-arm64). Go cross-compiles, so no QEMU is ever needed.
docker:
	docker buildx build $${PLATFORM:+--platform $$PLATFORM} --load -f deploy/Dockerfile -t $(IMAGE):$(VERSION) $(DOCKER_BUILD_ARGS) .

## build: compile the binary to ./bin
build:
	@bash scripts/build-web.sh
	@CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN) ./cmd/dkp

## verify-commands: assert AGENTS.md's command table matches this Makefile
verify-commands:
	@env -u DKP_REPO_ROOT bash scripts/verify-commands.sh

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
GENERATED_PATHS := db/migrations-sqlite internal/store/sqlitegen openapi clients web/src/api
verify-generated:
	@before=$$($(MAKE) --no-print-directory generated-digest); \
	$(MAKE) --no-print-directory gen; \
	after=$$($(MAKE) --no-print-directory generated-digest); \
	if [ "$$before" != "$$after" ]; then \
		printf '\033[31m  generated files are stale — run '"'"'make gen'"'"' and commit\033[0m\n'; \
		git --no-pager diff --stat -- $(GENERATED_PATHS) 2>/dev/null || true; \
		exit 1; \
	fi; \
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
check: verify-commands lint vet test
	@printf '\033[32m  make check complete\033[0m\n'

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
clean:
	@rm -rf $(BUILD_DIR) web/dist coverage.out

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

# The dependency licence gate. Same `env -u` reasoning as lint-repo above: its negative fixtures
# point DKP_REPO_ROOT at a fabricated module tree in t.TempDir(), and a value leaking in from a
# developer's shell would make `make check` inspect the wrong graph while printing that it passed.
#
# In `lint` rather than a job of its own because it is one `go list` plus shell, and a licence
# violation should stop a laptop before it stops CI. It is the cheapest half of the supply-chain
# gates; govulncheck below is the expensive half.
# GOFLAGS is stripped alongside DKP_REPO_ROOT: it can carry -mod=vendor or -tags, either of which
# changes which modules `go list` resolves. A developer's environment must not decide which
# dependency graph the licence gate inspects.
licence-gate:
	@env -u DKP_REPO_ROOT -u GOFLAGS bash scripts/licence-gate.sh

# Hard-fails when golangci-lint is missing. Exiting 0 would report a green `make check` that linted
# nothing, which is the defect this target exists to prevent.
# Resolved and invoked inside ONE shell block, via a variable, on purpose.
#
# A recipe line that is a single simple command is exec'd by make directly, without a shell, using
# the PATH make started with — so the `export PATH` above does not apply to it and a bare
# `golangci-lint run` fails with "make: golangci-lint: No such file or directory" even though the
# line above just found it. Keeping the lookup and the call in one `$$(...)`-using block forces the
# shell and makes the export effective. Do not "tidy" this into a bare command.
lint-go:
	@bin=$$(command -v golangci-lint 2>/dev/null || echo '$(GOTOOLS_BIN)/golangci-lint'); \
	[ -x "$$bin" ] || { \
		printf '\033[31m  golangci-lint not installed — run make setup\033[0m\n'; exit 1; }; \
	"$$bin" run

# `lint` scopes eslint to src/** (the negative fixtures are in eslint.config.js's `ignores`, so a
# bare `eslint .` stays green). `lint:fixtures` is the negative gate: it runs eslint --no-ignore over
# web/test-fixtures/lint/ and FAILS if a deliberate law-4 violation is NOT caught — the property that
# keeps the AST-aware half of law 4 from silently going blind. Both run in CI's `lint / web` job.
lint-web:
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
mockup-site:
	@bash scripts/build-mockup-site.sh

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
# `env -u DKP_REPO_ROOT` for the reason given above lint-repo. DKP_SPEC_BASE_REF is stripped for the
# same class of reason: an empty value disables the operationId-rename check, and that switch exists
# only so the negative fixtures in test/repo can run against a tree with no git history. A value
# leaking in from a developer's shell would turn a merge-blocking gate green.
verify-spec:
	@env -u DKP_REPO_ROOT -u DKP_SPEC_BASE_REF python3 scripts/verify-spec.py

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

budget-bundle:
	@bash scripts/budget-bundle.sh

verify-postgres:
	@$(call notyet,post-1.0,nightly Postgres schema convergence)

test-golden:
	@$(call notyet,Phase 4,parser golden files)

test-property:
	@$(call notyet,Phase 1,rapid property suite)

test-authz:
	@$(call notyet,Phase 2,the authorization matrix)

# The migration suite: fresh-install fingerprint, STRICT and no-guild_id assertions, the
# snapshot/auto-restore path and the downgrade refusal. Not -short: every one of these applies real
# migrations to a real database, which is the only way any of them mean anything.
test-migrations:
	@$(GO) test -count=1 ./test/migrations/...

test-e2e:
	@$(call notyet,Phase 3,Playwright against the built binary)

# Phase 8, not Phase 0 PR 3, and the correction is deliberate.
#
# This target upgrades the PREVIOUS RELEASE's reference database to HEAD. No reference database
# exists: `release-refdb` below is what publishes ghcr.io/<org>/dkp-refdb:<version>, and it is a
# Phase 8 stub. Phase 0 PR 3 built the migrator, the snapshot and the auto-restore — it could not
# build an upgrade test against an artefact no release has ever produced, and saying otherwise here
# would be the same defect that #5 and #6 were cleanups of.
#
# What PR 3 DID land is `test-migrations` above, which proves the forward path and the restore path
# against a real database. The N-1 ladder is a different guarantee and needs a published refdb.
test-upgrade:
	@$(call notyet,Phase 8,needs release-refdb to publish a reference database first)

test-upgrade-ladder:
	@$(call notyet,Phase 8,every published minor upgraded to HEAD)

upgrade-ladder-enumerate:
	@echo '[]'

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

# Regenerate THIRD_PARTY_NOTICES.txt from the runtime dependency graph. NOTICE promises this file and
# .goreleaser.yaml attaches it to every release archive; TestThirdPartyNotices_* asserts it covers the
# graph. Stdlib + the module cache only — no network, no extra tool. Run it after any go.mod change.
third-party-notices:
	@bash scripts/third-party-notices.sh

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
