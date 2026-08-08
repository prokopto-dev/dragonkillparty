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

# --- build stage: compile the static binary --------------------------------------------------
# Pinned by digest as well as tag: docker:pinDigests in renovate.json5 maintains it, and an
# unpinned base is the supply-chain equivalent of an unpinned action.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599 AS build

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

# CGO_ENABLED=0 and -trimpath give a static, reproducible binary. GOOS/GOARCH come from buildx so a
# single amd64 runner emits every architecture at native speed. The -ldflags match Makefile:33.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
      -o /out/dkp ./cmd/dkp

# --- final stage: FROM scratch ---------------------------------------------------------------
# Nothing but the binary and the two data files it needs. No shell means no `docker exec` debugging
# and no shell-injection surface; the `-debug` alpine variant (built by release.yml) exists for the
# officer who needs to look inside.
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
