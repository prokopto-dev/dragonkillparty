# Dragon Kill Party — canonical commands.
#
# CI asserts that every row in the AGENTS.md command table is a real target here. Needing a command
# that is not in that table means adding the target AND the row in the same change.
#
# The reverse does not hold: `help`, `build`, `clean`, `fmt`, `verify-commands` and
# `verify-generated` are plumbing invoked by the canonical targets or by CI, not commands a
# contributor is told to run, so they are deliberately absent from the table.
#
# Nothing is implemented yet. Targets that cannot do real work yet call `notyet` with the roadmap
# phase that fills them in, and exit 0 so `make check` stays green through Phase 0.

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
        build clean fmt verify-generated verify-commands

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
	@if ls **/*.go >/dev/null 2>&1 || compgen -G "*/*.go" >/dev/null; then \
		$(GO) test -short -shuffle=on -count=1 $(PKG); \
	else \
		$(call notyet,Phase 0 PR 2,no Go sources yet); \
	fi

## test: integration tests against a real SQLite database (budget ~30s)
test:
	@if compgen -G "*/*.go" >/dev/null; then \
		$(GO) test -race -shuffle=on -count=1 $(PKG); \
	else \
		$(call notyet,Phase 0 PR 2,no Go sources yet); \
	fi

## test-importer: EQdkp Plus importer suite — needs Docker (budget ~120s)
test-importer:
	@$(call notyet,Phase 5,runs against real EQdkp 2.0.5/2.1.5/2.2.27/2.3.39 fixtures plus the hostile fixture)

## lint: golangci-lint, eslint, and the repo grep gates
lint:
	@bash scripts/repo-gates.sh 2>/dev/null || printf '\033[33m  scripts/repo-gates.sh lands in Phase 0 PR 2\033[0m\n'
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || \
		printf '\033[33m  golangci-lint not installed (make setup) or no sources yet\033[0m\n'

## vet: build + go vet + staticcheck + tsc
vet:
	@if compgen -G "*/*.go" >/dev/null; then $(GO) build $(PKG) && $(GO) vet $(PKG); \
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
	@if compgen -G "cmd/dkp/*.go" >/dev/null; then \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BIN) ./cmd/dkp; \
	else \
		$(call notyet,Phase 0 PR 1,cmd/dkp is empty); \
	fi

## verify-commands: assert AGENTS.md's command table matches this Makefile
verify-commands:
	@bash scripts/verify-commands.sh 2>/dev/null || \
		printf '\033[33m  scripts/verify-commands.sh lands in Phase 0 PR 1\033[0m\n'

## verify-generated: fail if generated files drift from their sources
verify-generated:
	@$(call notyet,Phase 0 PR 3,make gen then git diff --exit-code)

## check: everything CI runs (budget ~60s)
check: verify-commands lint vet test
	@printf '\033[32m  make check complete\033[0m\n'

## clean: remove build output
clean:
	@rm -rf $(BUILD_DIR) web/dist coverage.out
