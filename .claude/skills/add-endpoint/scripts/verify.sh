#!/usr/bin/env bash
#
# verify.sh — the definition of done for the add-endpoint skill.
#
# Runs the four gates an endpoint change must satisfy, plus the changelog check that CODEOWNERS
# turns into a review control:
#
#   1. architectural tests   — security, permission, operationId, idempotency, envelope, hidden ops
#   2. generated-artefact drift — openapi.json, sqlitegen, both SDKs
#   3. oasdiff breaking      — the committed spec on the base ref vs this working tree
#   4. PAT parity            — no capability the browser has and a scoped PAT does not
#   5. api-changelog         — a spec change with no changelog line
#
# A gate that CANNOT RUN is a FAILURE, not a skip. A silent skip is how an endpoint ships without a
# permission key. Every unrunnable gate prints what is missing and how to get it.
#
# Usage:  .claude/skills/add-endpoint/scripts/verify.sh [--base <ref>]
# Env:    DKP_BASE_REF   base ref for oasdiff and the changelog check (default: origin/main)
#
# Exit:   0  every gate ran and passed
#         1  a gate ran and failed
#         2  a gate could not run (missing Makefile target, missing tool, missing base ref)

set -euo pipefail

BASE_REF="${DKP_BASE_REF:-origin/main}"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --base) BASE_REF="${2:?--base needs a ref}"; shift 2 ;;
        -h|--help) sed -n '3,24p' "$0"; exit 0 ;;
        *) printf 'verify.sh: unknown argument %s\n' "$1" >&2; exit 2 ;;
    esac
done

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$REPO_ROOT" ]]; then
    printf 'verify.sh: not inside a git repository\n' >&2
    exit 2
fi
cd "$REPO_ROOT"

SPEC="openapi/openapi.json"
CHANGELOG="docs/api-changelog.md"

TMPDIR_V="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_V"' EXIT

if [[ -t 1 ]]; then
    BOLD=$'\033[1m'; RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; OFF=$'\033[0m'
else
    BOLD=''; RED=''; GRN=''; YEL=''; OFF=''
fi

FAILED=()
UNRUNNABLE=()

step()      { printf '\n%s▸ %s%s\n' "$BOLD" "$1" "$OFF"; }
pass()      { printf '  %sPASS%s  %s\n' "$GRN" "$OFF" "$1"; }
fail()      { printf '  %sFAIL%s  %s\n' "$RED" "$OFF" "$1"; FAILED+=("$1"); }
cannot()    { printf '  %sCANNOT RUN%s  %s\n         %s\n' "$YEL" "$OFF" "$1" "$2"; UNRUNNABLE+=("$1"); }

# have_target <name> — true when the Makefile declares <name> as a real target.
have_target() { [[ -f Makefile ]] && grep -qE "^${1}:" Makefile; }
have_tool()   { command -v "$1" >/dev/null 2>&1; }
have_path()   { [[ -e "$1" ]]; }

printf '%sadd-endpoint verify%s  repo=%s  base=%s\n' "$BOLD" "$OFF" "$REPO_ROOT" "$BASE_REF"

# ---------------------------------------------------------------------------
# Gate 1 — architectural tests over the Huma registry.
# ---------------------------------------------------------------------------
step 'Gate 1/5  architectural tests (security · permission · operationId · idempotency · envelope)'

if ! have_tool go; then
    cannot 'architectural tests' 'go is not on PATH. Run: make setup'
elif ! have_path internal/api/arch_test.go; then
    # `go test -run TestArch` exits 0 when nothing matches, so a missing arch_test.go would read as
    # a PASS rather than as a gate that never ran. This branch existed until Phase 0 PR 4 to say
    # "arch_test.go lands in PR 4"; it now guards against its deletion.
    fail 'architectural tests: internal/api/arch_test.go is missing, so this gate would pass without running'
elif ! go test -count=1 -run 'TestArch' ./internal/api/... ; then
    fail 'architectural tests'
elif ! go test -count=1 -list 'TestArch' ./internal/api/... | grep -q '^TestArch'; then
    fail 'architectural tests: no TestArch* test exists, so `go test -run TestArch` passed vacuously'
else
    pass 'architectural tests'
fi

# ---------------------------------------------------------------------------
# Gate 2 — generated artefacts match their sources.
# ---------------------------------------------------------------------------
step 'Gate 2/5  generated-artefact drift (openapi.json · sqlitegen · clients/ts · clients/python)'

if ! have_target verify-generated; then
    cannot 'generated-artefact drift' \
        'no verify-generated target in the Makefile. Add it (and its AGENTS.md row) before relying
         on this gate; it is `make gen` followed by `git diff --exit-code`.'
elif ! have_path "$SPEC"; then
    # The spec has been committed since Phase 0 PR 4, so reaching this branch means it was deleted
    # rather than never written — which makes verify-generated inspect one fewer tree in silence.
    fail "generated-artefact drift: $SPEC is missing. It is a committed generated artefact — run \`make gen\`."
elif make verify-generated; then
    if git diff --quiet -- "$SPEC" internal/store/sqlitegen clients web/src/api 2>/dev/null; then
        pass 'generated artefacts are in sync'
    else
        fail 'generated artefacts drifted — run `make gen` and COMMIT the diff (never hand-edit)'
    fi
else
    fail 'make verify-generated'
fi

# ---------------------------------------------------------------------------
# Gate 3 — oasdiff against the base ref.
# ---------------------------------------------------------------------------
step "Gate 3/5  oasdiff breaking-change check vs ${BASE_REF}"

if ! have_tool oasdiff; then
    cannot 'oasdiff' 'oasdiff is not on PATH. Run: make setup'
elif ! have_path "$SPEC"; then
    fail "oasdiff: $SPEC is missing. It has been committed since Phase 0 PR 4 — run \`make gen\`."
elif ! git rev-parse --verify --quiet "${BASE_REF}" >/dev/null; then
    cannot 'oasdiff' "base ref ${BASE_REF} not found. Run: git fetch origin main"
elif ! git show "${BASE_REF}:${SPEC}" > "${TMPDIR_V}/base.json" 2>/dev/null; then
    cannot 'oasdiff' "${SPEC} does not exist on ${BASE_REF}; nothing to diff against."
else
    # Human-readable delta first — this is what a reviewer reads.
    oasdiff changelog "${TMPDIR_V}/base.json" "$SPEC" || true
    if oasdiff breaking "${TMPDIR_V}/base.json" "$SPEC" --fail-on ERR; then
        pass 'no breaking API change'
    else
        fail "breaking API change vs ${BASE_REF} — needs the !breaking-api label, a line in ${CHANGELOG}, and a human decision"
    fi
fi

# ---------------------------------------------------------------------------
# Gate 4 — PAT parity. If the browser can do it, a scoped token must be able to.
# ---------------------------------------------------------------------------
step 'Gate 4/5  PAT-parity suite'

if ! have_tool go; then
    cannot 'PAT parity' 'go is not on PATH. Run: make setup'
elif ! have_path test/integration; then
    cannot 'PAT parity' 'test/integration does not exist yet (hand-written parity cases land in Phase 2).'
elif ! compgen -G 'test/integration/*_test.go' >/dev/null; then
    cannot 'PAT parity' 'no integration tests yet. Parity cases land in Phase 2; the recorder in Phase 3.'
elif go test -count=1 -tags=integration -run 'TestPATParity' ./test/integration/... ; then
    pass 'PAT parity'
else
    fail 'PAT parity — the SPA can do something a scoped PAT cannot'
fi

# ---------------------------------------------------------------------------
# Gate 5 — a spec change carries a changelog line.
# ---------------------------------------------------------------------------
step "Gate 5/5  ${CHANGELOG} line for a spec change"

if ! git rev-parse --verify --quiet "${BASE_REF}" >/dev/null; then
    cannot 'api-changelog' "base ref ${BASE_REF} not found. Run: git fetch origin main"
elif ! have_path "$SPEC"; then
    cannot 'api-changelog' "$SPEC does not exist yet."
elif git diff --quiet "${BASE_REF}" -- "$SPEC"; then
    pass 'spec unchanged vs base; no changelog line required'
elif git diff --quiet "${BASE_REF}" -- "$CHANGELOG"; then
    fail "${SPEC} changed but ${CHANGELOG} did not — add one line describing the change"
else
    pass 'spec change carries a changelog line'
fi

# ---------------------------------------------------------------------------
step 'Summary'

if ((${#UNRUNNABLE[@]})); then
    printf '  %s%d gate(s) could not run:%s\n' "$RED" "${#UNRUNNABLE[@]}" "$OFF"
    printf '    - %s\n' "${UNRUNNABLE[@]}"
    printf '  A gate that cannot run has NOT passed. Do not report this endpoint as verified.\n'
fi
if ((${#FAILED[@]})); then
    printf '  %s%d gate(s) failed:%s\n' "$RED" "${#FAILED[@]}" "$OFF"
    printf '    - %s\n' "${FAILED[@]}"
fi

if ((${#FAILED[@]})); then exit 1; fi
if ((${#UNRUNNABLE[@]})); then exit 2; fi

printf '  %sall gates passed%s — now run: make check\n' "$GRN" "$OFF"
