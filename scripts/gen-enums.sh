#!/usr/bin/env bash
# The enum half of `make gen`: db/schema.hcl's generated CHECK constraints, emitted from the Go
# catalogues in internal/ledger/kinds (ledger_batch.kind, ledger_batch.source), internal/audit/kinds
# (audit_log.actor_kind, audit_log.outcome), internal/account/kinds (account.kind,
# account.system_key) and internal/decay/kinds (decay_run.kind, decay_run.state).
#
# RUNS FIRST, AND COMPILES ONLY LEAF PACKAGES. Every catalogue and internal/schemaenum, which holds
# the rendering they share, import nothing but the standard library, deliberately: this step
# precedes sqlc, so if it reached generated code a tree whose sqlc output did not build could not
# run `make gen` to repair it.
#
# Canonical §5 requires the enum catalogue to be a Go const block that `make gen` writes into the
# CHECK and the OpenAPI schema, with a test asserting the copies agree. Before this step the
# fourteen ledger_batch kinds and six sources were literals in db/schema.hcl and nowhere else, so a
# kind added in Go was a legal write that failed at the database — the failure mode canonical §5
# exists to remove, and the one internal/authz/catalogue.go already removes for permission keys. The
# six audit_log actor kinds were worse: a literal in the CHECK AND a second list in
# internal/ledger/commit.go, which is the same failure with a second way to reach it (#40). The
# account vocabularies were worse again: account.system_key was written THREE times — a const block
# in internal/strategy, the CHECK, and the seed rows of 000003_ledger.sql — and generated from none
# of them (#51), while account.kind and audit_log.outcome had no Go list at all (#53).
#
# RUNS BEFORE gen-db.sh, and the order is load-bearing: gen-db.sh asserts db/schema.hcl and the
# committed migrations describe the same schema, so it has to see the schema this step just wrote.
# Reversed, a catalogue change would be reported as clean on the run that introduced it and as a
# mystery on the next one.
#
# It rewrites db/schema.hcl only between each catalogue's GENERATED markers; everything else in that
# file is hand-authored schema truth. Changing a VALUE is therefore a two-command change —
# `make gen` here, then `make migration NAME=<snake_case>` to author the SQL — because this script
# deliberately does not write migrations (scripts/gen-db.sh's header explains why no `gen` step may).

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

# The generator writes through a temp file and a rename, and says nothing on success. It is a
# generator, not a gate: the drift assertions are TestLedgerKinds_CheckMatchesCatalogue,
# TestAuditKinds_CheckMatchesCatalogue, TestAccountKinds_CheckMatchesCatalogue,
# TestDecayKinds_CheckMatchesCatalogue and `make verify-generated`.
go run ./internal/ledger/enumgen db/schema.hcl \
    || die "enumgen failed — db/schema.hcl was not rewritten"

printf '  \033[32mdb/schema.hcl enum CHECKs regenerated\033[0m — from internal/ledger/kinds, internal/audit/kinds, internal/account/kinds, internal/decay/kinds\n'
