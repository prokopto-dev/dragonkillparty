# Dragon Kill Party — the distributed image.
#
# LOCATION. docs/development/first-ten-prs.md §PR 7 lists this file at the repository root, but the
# committed Makefile `docker` recipe already reads `deploy/Dockerfile` (Makefile:199-203) and CI's
# `build / image` job calls `make docker`. Per AGENTS.md a conflict between a plan and shipped code
# is resolved in favour of the shipped code and recorded rather than quietly picked: the Makefile is
# what CI executes, and moving the file to the root would leave `make docker` stuck on its `notyet`
# stub forever. The plan's files-touched blocks have been corrected this way before (see the PR 3
# and PR 4 corrections in that same document). This is that correction for PR 7.
#
# The build is deliberately structured so the FINAL stage is `FROM scratch` with a handful of COPY
# layers and nothing else — no shell, no package manager, no libc. modernc.org/sqlite is pure Go and
# CGO stays off, so the binary is statically linked and needs no runtime beyond what is copied in:
# the CA bundle (for outbound HTTPS — the update check and future webhooks) and the zoneinfo
# database (canonical time handling; the guild timezone setting is meaningless without it).
#
# Multi-arch is produced by CROSS-COMPILATION, never QEMU — see .github/workflows/release.yml's
# header for why. TARGETOS/TARGETARCH are supplied by buildx per platform; on a plain `docker build`
# they default to the host, which is what a local `make docker` wants.

# --- web stage: build the SPA -----------------------------------------------------------------
# The binary embeds the SPA (internal/ui), and //go:embed cannot reach out of its own directory, so
# the Vite output has to be staged into internal/ui/dist BEFORE `go build` runs. This stage exists
# because for the whole of Phase 0 it did not: the build stage ran `go build` directly and every
# image shipped internal/ui's committed PLACEHOLDER, serving "web UI not yet built into this binary"
# to anyone who opened it (issue #55).
#
# $BUILDPLATFORM, never the target: JavaScript is architecture-independent, so the SPA is built once
# on the native runner and the same bytes are embedded into every architecture's binary. Running
# node under emulation for arm64 would be QEMU by another name.
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436 AS web

# corepack activates the pnpm version pinned by web/package.json's `packageManager` field, so the
# image, `make setup` and CI all run one pnpm. The prompt is disabled because corepack asks before
# downloading a version it has not seen, and a build has nobody to answer it.
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0

WORKDIR /src

# Manifest, lockfile and .npmrc first, then the tree: a change to a .tsx does not re-resolve the
# dependency graph. --frozen-lockfile because the lockfile is the contract, --ignore-scripts because
# no dependency lifecycle script runs during a build (the same posture CI and build-web.sh install
# with). web/.npmrc carries `ignore-scripts=true` so this layer has that posture by DEFAULT and not
# only because the flag is spelled out — the flag stays, so the two cannot silently disagree.
COPY web/package.json web/pnpm-lock.yaml web/.npmrc ./web/
RUN corepack enable && cd web && pnpm install --frozen-lockfile --ignore-scripts

# The SPA sources and nothing else: a Go-only commit must not invalidate this layer and re-run Vite.
# The SAME script `make build` runs, not a second transcription of it — it builds the SPA, refuses to
# stage a source map, and copies web/dist into internal/ui/dist. An image whose web build drifted
# from the local one would be a difference nobody could see until a guild reported it.
COPY web/ ./web/
COPY scripts/build-web.sh ./scripts/
RUN bash scripts/build-web.sh

# --- build stage: compile the static binary --------------------------------------------------
# Pinned by digest as well as tag: docker:pinDigests in renovate.json5 maintains it, and an
# unpinned base is the supply-chain equivalent of an unpinned action.
#
# THE DIGEST IS ALSO A SECURITY FLOOR, which is why it moves with go.mod's `go` directive rather than
# only when Renovate notices. This one is `golang:1.26.6-bookworm`: the five reachable stdlib
# advisories in 1.26.5 (GO-2026-6218, -6090, -6089, -5972, -5026) are fixed there, and a builder image
# older than go.mod's floor would either fail the build or quietly download the fixed toolchain into
# an image that still claims the vulnerable one.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build

# The cross-compilation targets, injected by buildx (or defaulted to the host by plain docker build).
ARG TARGETOS=linux
ARG TARGETARCH

# Build stamps, mirrored from the Makefile's -ldflags so an image built here reports the same
# version, commit and date a `make build` binary would. Passed as build args by `make docker`.
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

WORKDIR /src

# Module graph first, then the tree: a source-only change does not re-download modules.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

# The built SPA, staged where //go:embed can reach it.
#
# The `rm -rf` is load-bearing and not tidiness: COPY MERGES into an existing directory, and the
# context carries internal/ui's committed placeholders. Copying over them would replace index.html
# but leave assets/app-placeholder.js behind, and the embed's `all:` directive would ship it.
RUN rm -rf internal/ui/dist
COPY --from=web /src/internal/ui/dist ./internal/ui/dist

# Then prove it. A placeholder embed compiles, links and boots perfectly well — that is precisely why
# issue #55 survived a whole phase — so the only thing that catches it is an assertion between
# staging and compiling. This fails the build rather than pushing an image that serves the
# placeholder, and it is the only SPA check that can cover the cross-compiled arm64 image, which no
# runner boots without the QEMU this project bans. scripts/smoke-spa.sh is the runtime counterpart.
RUN bash scripts/verify-spa-dist.sh internal/ui/dist

# CGO_ENABLED=0 and -trimpath give a static, reproducible binary. GOOS/GOARCH come from buildx so a
# single amd64 runner emits every architecture at native speed. The -ldflags match Makefile:33.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/dkp ./cmd/dkp

# --- final stage: FROM scratch ---------------------------------------------------------------
# Nothing but the binary and the two data files it needs. No shell means no shell-injection surface
# and no `docker exec … sh` — `docker exec dkp /usr/local/bin/dkp doctor` still works, because exec
# runs a binary and not a login shell, and docs/getting-started/install-docker.md documents the rest
# of the ways to look inside a distroless image.
#
# There is NO `-debug` variant. This comment promised one until issue #184, as did two tables in
# docs/design/06-cicd-and-release.md and one in install-docker.md, and nothing has ever built it:
# scripts/release-image.sh builds exactly one image per architecture, from this one target. Shipping
# a shell-bearing variant of a deliberately distroless image is a security-posture decision
# (docs/design/03-security.md argues for the distroless base), so the promise came out rather than
# the image going in. TestImage_DebugVariant_IsNeitherBuiltNorPromised holds the answer.
FROM scratch

# CA certificates for outbound TLS and the zoneinfo database for timezone handling. Copied from the
# build stage, which is Debian-based, so no extra base image is pulled just for these two files.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo

# The binary.
COPY --from=build /out/dkp /usr/local/bin/dkp

# 65532 is the conventional "nonroot" uid (distroless uses it). Running as a non-root, non-zero uid
# means a container breakout starts with no privileges, and it is a numeric uid rather than a name
# because scratch has no /etc/passwd to resolve a name against.
USER 65532

# /data is the single writable volume: the SQLite database, the pre-migration snapshots and the
# artifacts all live under it. Declaring it here documents the mount an operator must persist and
# lets the runtime create an anonymous volume if they forget.
VOLUME /data

# The default database path lives on the volume, matching docs and DKP_DATA_DIR's container default.
ENV DKP_DB_PATH=/data/dkp.db

EXPOSE 8080

# The HEALTHCHECK calls `dkp healthcheck`, NOT `dkp doctor` (canonical §13). It is a loopback GET
# /healthz that touches no database, so it stays green through a migration while /readyz reports
# not-ready — a container mid-migration is healthy (do not kill it) but not ready (do not route to
# it). A database-touching healthcheck lets Docker kill the container mid-migration.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/dkp", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/dkp"]
CMD ["serve"]
