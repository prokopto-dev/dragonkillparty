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

# The `go` directive in go.mod is the single source of the minimum toolchain. `make setup` asserts
# the installed Go satisfies it; it cannot install Go itself.
GO_REQUIRED    := $(shell awk '/^go /{print $$2; exit}' go.mod)

# Pinned dev tools. Never @latest — an unpinned tool makes CI and the laptop disagree silently.
# renovate: datasource=go depName=mvdan.cc/gofumpt
GOFUMPT_VERSION        ?= v0.11.0
# renovate: datasource=go depName=golang.org/x/tools
GOIMPORTS_VERSION      ?= v0.48.0
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION  ?= v2.12.2

# notyet <phase> <what> — a target that is declared but not yet implemented.
# No leading '@': call sites add it, so this also works inside shell if/else blocks.
define notyet
printf '\033[33m  not yet implemented\033[0m  %s\n  lands in: %s\n' "$(2)" "$(1)"
endef

.PHONY: help setup dev gen test-unit test test-importer lint vet migration seed docker check \
        build clean fmt verify-generated verify-commands \
        lint-repo lint-go lint-web verify-action-pins docs-build docs-links verify-spec \
        api-breaking api-changelog-comment budget-bundle verify-postgres test-golden \
        test-property test-authz test-migrations test-e2e test-upgrade test-upgrade-ladder \
        upgrade-ladder-enumerate soak-jobs smoke-local nightly-report status \
        fixture-build fixture-seed fixture-capture fixture-verify fixture-manifest \
        fixture-publish fixture-gate release-version release-notes release-image \
        release-manifest release-sign release-sbom release-smoke release-promote \
        release-promote-rc release-refdb release-failure-issue

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
	@bin=$$($(GO) env GOBIN); [ -n "$$bin" ] || bin="$$($(GO) env GOPATH)/bin"; \
	printf '\033[32m  toolchain installed\033[0m into %s — make sure it is on your PATH\n' "$$bin"
	@printf '  not installed yet, and deliberately so — nothing needs them at this phase:\n'
	@printf '    sqlc, atlas, goose   lands in: Phase 0 PR 3 (schema, migrations, generated queries)\n'
	@printf '    oasdiff              lands in: Phase 2 (spec breaking-change gate)\n'
	@printf '    vale, lychee         lands in: Phase 0 PR 11 (the docs site and its gates)\n'
	@printf '    pnpm + web deps      lands in: Phase 0 PR 6 (the SPA)\n'

## dev: run the dev servers — Go on :8080, Vite on :5173
dev:
	@$(call notyet,Phase 0 PR 6,needs cmd/dkp and web/ to exist)

## gen: regenerate ALL generated code — migrations, sqlc, openapi, clients
gen:
	@$(call notyet,Phase 0 PR 3-6,atlas migrate diff -> sqlc generate -> dkp openapi -> openapi-typescript + openapi-python-client)

## test-unit: fast unit tests only (budget < 5s)
test-unit:
	@$(GO) test -short -shuffle=on -count=1 $(PKG)

## test: integration tests against a real SQLite database (budget ~30s)
test:
	@$(GO) test -race -shuffle=on -count=1 $(PKG)

## test-importer: EQdkp Plus importer suite — needs Docker (budget ~120s)
test-importer:
	@$(call notyet,Phase 5,runs against real EQdkp 2.0.5/2.1.5/2.2.27/2.3.39 fixtures plus the hostile fixture)

## lint: the repo grep gates, golangci-lint and eslint
lint: lint-repo lint-go lint-web

## vet: build + go vet + staticcheck + tsc
# Today this runs build + vet only. staticcheck is folded into golangci-lint, so it runs under
# `make lint`; tsc lands with the SPA in Phase 0 PR 6. AGENTS.md documents the eventual composition.
vet:
	@$(GO) build $(PKG) && $(GO) vet $(PKG)

## fmt: gofumpt + goimports over the tree
fmt:
	@command -v gofumpt >/dev/null 2>&1 && gofumpt -l -w . || \
		printf '\033[33m  gofumpt not installed — run make setup\033[0m\n'

## migration: create a migration — make migration NAME=add_bid_hold
migration:
ifndef NAME
	$(error NAME is required: make migration NAME=add_bid_hold)
endif
	@$(call notyet,Phase 0 PR 3,atlas migrate diff $(NAME) --dev-url "sqlite://file?mode=memory")

## seed: seed a dev guild — make seed PROFILE=demo (or small, perf)
seed:
	@$(call notyet,Phase 1 (perf) / Phase 2 (small) / Phase 3 (demo),dkp seed --profile $${PROFILE:-demo})

## docker: build the container image
docker:
	@if [ -f deploy/Dockerfile ]; then \
		docker build -f deploy/Dockerfile -t $(IMAGE):$(VERSION) \
			--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) . ; \
	else \
		$(call notyet,Phase 0 PR 7,deploy/Dockerfile does not exist yet); \
	fi

## build: compile the binary to ./bin
build:
	@CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN) ./cmd/dkp

## verify-commands: assert AGENTS.md's command table matches this Makefile
verify-commands:
	@env -u DKP_REPO_ROOT bash scripts/verify-commands.sh

## verify-generated: fail if generated files drift from their sources
verify-generated:
	@$(call notyet,Phase 0 PR 3,make gen then git diff --exit-code)

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

# Hard-fails when golangci-lint is missing. Exiting 0 would report a green `make check` that linted
# nothing, which is the defect this target exists to prevent.
lint-go:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		printf '\033[31m  golangci-lint not installed — run make setup\033[0m\n'; exit 1; }
	@golangci-lint run

lint-web:
	@if [ -f web/package.json ]; then cd web && pnpm run lint; \
	else $(call notyet,Phase 0 PR 6,web/ is not scaffolded yet); fi

verify-action-pins:
	@env -u DKP_REPO_ROOT bash scripts/repo-gates.sh

docs-build:
	@if [ -f docs/package.json ]; then cd docs && pnpm run build; \
	else $(call notyet,Phase 0 PR 11,the Astro/Starlight site is not scaffolded yet); fi

docs-links:
	@python3 scripts/check-links.py

verify-spec:
	@$(call notyet,Phase 0 PR 4,vacuum lint over openapi/openapi.json)

api-breaking:
	@$(call notyet,Phase 2,oasdiff against main's openapi.json)

api-changelog-comment:
	@$(call notyet,Phase 2,sticky PR comment summarising the spec diff)

budget-bundle:
	@$(call notyet,Phase 0 PR 6,250 KB gzipped initial-route budget)

verify-postgres:
	@$(call notyet,post-1.0,nightly Postgres schema convergence)

test-golden:
	@$(call notyet,Phase 4,parser golden files)

test-property:
	@$(call notyet,Phase 1,rapid property suite)

test-authz:
	@$(call notyet,Phase 2,the authorization matrix)

test-migrations:
	@$(call notyet,Phase 0 PR 3,fresh-install and upgrade fingerprints)

test-e2e:
	@$(call notyet,Phase 3,Playwright against the built binary)

test-upgrade:
	@$(call notyet,Phase 0 PR 3,N-1 to N upgrade against the reference database)

test-upgrade-ladder:
	@$(call notyet,Phase 8,every published minor upgraded to HEAD)

upgrade-ladder-enumerate:
	@echo '[]'

soak-jobs:
	@$(call notyet,Phase 1,10k-job River soak)

smoke-local:
	@$(call notyet,Phase 0 PR 7,boot the built image and hit /readyz)

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
	@$(call notyet,Phase 0 PR 7,multi-arch build and push)

release-manifest:
	@$(call notyet,Phase 0 PR 7,imagetools manifest join)

release-sign:
	@$(call notyet,Phase 0 PR 7,cosign keyless)

release-sbom:
	@$(call notyet,Phase 0 PR 7,SBOM attestation)

release-smoke:
	@$(call notyet,Phase 0 PR 7,pull the immutable digest on amd64 and arm64)

release-promote:
	@$(call notyet,Phase 0 PR 7,advance the moving tags only after smoke passes)

release-promote-rc:
	@$(call notyet,Phase 8,promote a release candidate)

release-refdb:
	@$(call notyet,Phase 8,publish the reference database for the upgrade ladder)

release-failure-issue:
	@$(call notyet,Phase 8,file an issue when a release job fails)
