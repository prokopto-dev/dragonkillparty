# Dragon Kill Party — canonical commands.
#
# CI asserts that every row in the AGENTS.md command table resolves to a real target here. The
# reverse is not asserted: the targets below the divider are called by CI, not by hand, and listing
# them in a file agents read every session would be noise.
#
# Nothing is implemented yet. Targets that cannot do real work yet call `notyet` with the roadmap
# phase that fills them in, and exit 0 so `make check` and CI stay green through Phase 0.

SHELL := /bin/bash
.DEFAULT_GOAL := help

GO             ?= go
PKG            := ./...
BIN            := dkp
BUILD_DIR      := ./bin
IMAGE          ?= ghcr.io/dragonkillparty/dkp
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT         ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS        := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

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
        upgrade-ladder-enumerate soak-jobs smoke-local nightly-report \
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
	@$(call notyet,Phase 0 PR 1,installs gofumpt goimports golangci-lint sqlc atlas goose oasdiff vale lychee and pnpm deps)

## dev: run the dev servers — Go on :8080, Vite on :5173
dev:
	@$(call notyet,Phase 0 PR 6,needs cmd/dkp and web/ to exist)

## gen: regenerate ALL generated code — migrations, sqlc, openapi, clients
gen:
	@$(call notyet,Phase 0 PR 3-6,atlas migrate diff -> sqlc generate -> dkp openapi -> openapi-typescript + openapi-python-client)

## test-unit: fast unit tests only (budget < 5s)
test-unit:
	@if compgen -G "*/*.go" >/dev/null 2>&1; then \
		$(GO) test -short -shuffle=on -count=1 $(PKG); \
	else \
		$(call notyet,Phase 0 PR 2,no Go sources yet); \
	fi

## test: integration tests against a real SQLite database (budget ~30s)
test:
	@if compgen -G "*/*.go" >/dev/null 2>&1; then \
		$(GO) test -race -shuffle=on -count=1 $(PKG); \
	else \
		$(call notyet,Phase 0 PR 2,no Go sources yet); \
	fi

## test-importer: EQdkp Plus importer suite — needs Docker (budget ~120s)
test-importer:
	@$(call notyet,Phase 5,runs against real EQdkp 2.0.5/2.1.5/2.2.27/2.3.39 fixtures plus the hostile fixture)

## lint: the repo grep gates, golangci-lint and eslint
lint: lint-repo lint-go lint-web

## vet: build + go vet + staticcheck + tsc
vet:
	@if compgen -G "*/*.go" >/dev/null 2>&1; then $(GO) build $(PKG) && $(GO) vet $(PKG); \
	else $(call notyet,Phase 0 PR 1,no Go sources yet); fi

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
	@if compgen -G "cmd/dkp/*.go" >/dev/null 2>&1; then \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN) ./cmd/dkp; \
	else \
		$(call notyet,Phase 0 PR 1,cmd/dkp is empty); \
	fi

## verify-commands: assert AGENTS.md's command table matches this Makefile
verify-commands:
	@bash scripts/verify-commands.sh

## verify-generated: fail if generated files drift from their sources
verify-generated:
	@$(call notyet,Phase 0 PR 3,make gen then git diff --exit-code)

## check: everything CI runs (budget ~60s)
check: verify-commands lint vet test
	@printf '\033[32m  make check complete\033[0m\n'

## clean: remove build output
clean:
	@rm -rf $(BUILD_DIR) web/dist coverage.out

# ---------------------------------------------------------------------------
# CI-called targets. Deliberately absent from the AGENTS.md canonical table:
# agents do not run these by hand, CI does. verify-commands asserts that every
# table row resolves to a target, not that every target has a row.
# ---------------------------------------------------------------------------

lint-repo:
	@bash scripts/repo-gates.sh

lint-go:
	@if compgen -G "*/*.go" >/dev/null 2>&1 && command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		$(call notyet,Phase 0 PR 1,no Go sources or golangci-lint not installed); \
	fi

lint-web:
	@if [ -f web/package.json ]; then cd web && pnpm run lint; \
	else $(call notyet,Phase 0 PR 6,web/ is not scaffolded yet); fi

verify-action-pins:
	@bash scripts/repo-gates.sh

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
