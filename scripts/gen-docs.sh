#!/usr/bin/env bash
# The reference-page half of `make gen`: docs/reference/permissions.md and docs/reference/scopes.md,
# emitted from the Go catalogue in internal/authz.
#
# docs/README.md's generated-pages table names both files and their source, and the ROADMAP's phase-2
# exit criterion states it as a requirement: "docs/reference/permissions.md is generated, not
# written." Canonical §6 says it from the other end — one catalogue generates the permission table
# seed, the OpenAPI metadata, the PAT scope enum and this page.
#
# RUNS LAST, and unlike gen-enums.sh it may. That one is first and must compile without generated
# code, because a tree whose sqlc output does not build has to be able to repair itself. This
# generator imports internal/authz, which reaches internal/store/sqlitegen through the boot
# reconciliation, so it has to run AFTER sqlc — and it can, because nothing it writes is an input to
# any other generator.

set -euo pipefail

# Same DKP_REPO_ROOT contract as the other gen scripts; `make gen` strips it with `env -u`.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

die() {
    printf '\033[31m  %s\033[0m\n' "$*" >&2
    exit 1
}

# A hard requirement, never soft-skipped, for the reason gen-db.sh gives: a `make gen` that exits 0
# because a generator was missing reports a clean tree it never inspected.
command -v go >/dev/null 2>&1 || die "go is not installed — see make setup"

out=docs/reference

go run ./internal/authz/docgen "$out" \
    || die "docgen failed — the reference pages were not written"

# A smoke test for "the command produced something", not a content check; `make verify-generated` is
# the real gate. A zero-byte page would otherwise be committed and rendered as an empty reference.
for page in permissions scopes; do
    [ -s "$out/$page.md" ] || die "docgen produced an empty $out/$page.md"
done

printf '  \033[32m%s regenerated\033[0m — from internal/authz/catalogue.go\n' "$out/{permissions,scopes}.md"
