#!/usr/bin/env bash
# The spec half of `make gen`: openapi/openapi.json, emitted from the Go handler types.
#
# The document is DERIVED, never authored. Huma builds it from the exact structs the handlers are
# compiled against (docs/design/02-api-design.md §9), so the committed file cannot describe a server
# nobody runs — which is the failure mode spec-first codegen has and this direction does not.
#
# This script therefore has no inputs beyond the Go source, and exactly one output. It does not lint
# the result (`make verify-spec` does) and it does not generate clients (Phase 0 PR 6 adds
# openapi-typescript and openapi-python-client as further lines in the `gen` recipe).

set -euo pipefail

# Same DKP_REPO_ROOT contract as the gate scripts; `make gen` strips it with `env -u`.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() {
    printf '\033[31m  %s\033[0m\n' "$*" >&2
    exit 1
}

# A hard requirement, never soft-skipped, for the reason gen-db.sh gives: a `make gen` that exits 0
# because a generator was missing reports a clean tree it never inspected.
command -v go >/dev/null 2>&1 || die "go is not installed — see make setup"

out=openapi/openapi.json
mkdir -p "$(dirname "$out")"

# Emit to a temporary file and move it into place only on success.
#
# `go run ./cmd/dkp openapi > "$out"` would be the obvious one-liner and is wrong: the shell
# truncates the target before the command runs, so a compile error or a panic leaves a ZERO-BYTE
# committed spec. That file is the input to the drift gate, both SDK generators and the docs
# renderer, and an empty one fails all of them with errors that point anywhere but here.
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

go run ./cmd/dkp openapi >"$tmp" || die "dkp openapi failed — the spec was not written"

# A well-formed document is at least an object with an openapi version in it. This is a smoke test
# for "the command produced something", not a schema check; `make verify-spec` is the real gate.
[ -s "$tmp" ] || die "dkp openapi produced an empty document"
grep -q '"openapi"' "$tmp" || die "dkp openapi produced output with no \"openapi\" key"

mv "$tmp" "$out"
trap - EXIT

# mktemp creates 0600. git stores only the executable bit, so this does not affect what is
# committed — but leaving a generated, committed file readable by nobody but the developer who ran
# `make gen` is a surprise waiting in a container build or a shared checkout.
chmod 644 "$out"

printf '  \033[32mopenapi/openapi.json regenerated\033[0m — from the Go handler types\n'
