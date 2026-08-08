#!/usr/bin/env bash
# The dependency licence gate.
#
# Fails on any copyleft or non-open-source licence in the RUNTIME module graph. The project is
# Apache-2.0 and its entire relationship with EQdkp Plus (AGPL-3.0) rests on the tree staying
# uncontaminated — see docs/design/00-canonical-conventions.md §15.
#
# Rule ids, in the same shape as scripts/repo-gates.sh:
#   LIC001  a runtime dependency is under a denied licence
#   LIC002  a runtime dependency's licence could not be identified, or is not on the allowlist
#   LIC003  a runtime dependency declares embedded third-party code under a denied licence
#
# This gate has no vacuous-pass path. Unlike repo-gates.sh, whose rules gate trees that do not
# exist yet and therefore skip, every input here exists from the moment there is a go.mod: a
# missing toolchain, a missing go.mod, a `go list` failure and a module graph that resolves to
# nothing are all errors, never silent passes.

set -euo pipefail

# DKP_REPO_ROOT lets a test point the gate at a tree other than this checkout — the same mechanism
# repo-gates.sh uses, for the same reason: the gate must be tested rather than trusted, and a
# fixture with a GPL dependency cannot live inside the repo without failing the project's own CI.
cd "${DKP_REPO_ROOT:-$(dirname "$0")/..}"

fail=0
# violation <id> <description> <hits-as-one-newline-separated-string>
violation() {
    printf '\033[31mFAIL\033[0m [%s] %s\n' "$1" "$2"
    printf '%s\n' "$3" | sed 's/^/  /'
    fail=1
}
die() { printf '\033[31mFAIL\033[0m %s\n' "$1"; exit 1; }

# --- Licence classification --------------------------------------------------------------------
#
# Two rules govern this section, and both exist because the obvious implementation is wrong.
#
# 1. EVERY pattern is evaluated. There is no early return on the first match.
#
#    A classifier that stops at the first licence it recognises cannot see a restriction layered on
#    top of a permissive grant, because the grant matches first. That is not a hypothetical shape:
#    the Commons Clause is Apache-2.0 plus "the License does not grant to you ... the right to Sell
#    the Software", and the JSON licence is verbatim MIT plus "shall be used for Good, not Evil".
#    Both would be reported as their permissive base and waved through. Collecting every match and
#    letting any denial win is what makes the gate see the rider.
#
# 2. The denylist is applied to text with MPL-2.0's §1.12 cross-reference removed.
#
#    MPL-2.0's own text names the GNU licences:
#
#      "Secondary License" means either the GNU General Public License, Version 2.0, the GNU
#      Lesser General Public License, Version 2.1, the GNU Affero General Public License, ...
#
#    So a denylist applied to raw MPL-2.0 text fires on github.com/hashicorp/golang-lru/v2, which is
#    already in this repo's graph. The fix is to delete that one sentence, NOT to identify MPL first
#    and stop looking — doing that lets a genuine GPL module through by naming the MPL in a preamble,
#    which is exactly the shape a module takes when it vendors a component and concatenates notices.
#    If a future MPL variant escapes the strip, the gate fails closed on MPL. That is the safe
#    direction, and TestLicenceGate_AllowedLicence_Passes/mpl2 is what would tell us.

# normalise <file> — the file's text as one line, with MPL-2.0's Secondary License definition
# removed. Newlines collapse to spaces because every phrase matched below is long enough to be
# wrapped across lines in a real licence file.
#
# The removal is an EXACT literal, not a span. A bounded-wildcard form such as
# `s/"Secondary License"[^"]*licen[cs]es\.//` is greedy: in a file that names the Secondary License
# early and carries a real GPL grant later, it deletes everything between the two — the GNU title
# and the operative grant with it — and the denylist then sees nothing. Verified; it collapses such
# a file to its first sentence. Matching the canonical MPL-2.0 §1.12 sentence verbatim cannot
# over-reach. If a future MPL variant rewords it the strip stops matching and the gate fails closed
# on MPL, which is the safe direction, and TestLicenceGate_AllowedLicence_Passes/mpl2 says so.
MPL_SECONDARY_LICENSE_DEFINITION='"Secondary License" means either the GNU General Public License, Version 2\.0, the GNU Lesser General Public License, Version 2\.1, the GNU Affero General Public License, Version 3\.0, or any later versions of those licenses\.'

#
# LC_ALL=C makes this byte-wise. Every pattern below is ASCII, and under a UTF-8 locale BSD sed
# rejects an invalid multibyte sequence outright — so a dependency shipping a Latin-1 LICENSE (an
# ordinary thing for older European projects) would abort the whole script from inside the module
# loop with a bare "RE error: illegal byte sequence", no rule id, no module name, and every
# remaining module unexamined. It fails closed, but on a developer laptop only: the ubuntu runner
# would be green, which is the worst possible split.
normalise() {
    LC_ALL=C tr '\n' ' ' <"$1" |
        LC_ALL=C tr -s '[:space:]' ' ' |
        LC_ALL=C sed -E "s/$MPL_SECONDARY_LICENSE_DEFINITION//g"
}

# deny_hits <text> — echoes the ids of every denied licence family the text matches.
deny_hits() {
    local t="$1" h=""

    # Source-available and non-open-source. Matched by title: unlike the GNU names, these never
    # appear as a cross-reference inside a permissive licence. BUSL is the reason this block runs
    # over the raw title rather than relying on the permissive patterns not matching — BUSL's own
    # "Change License" parameter names the MPL (HashiCorp) or Apache-2.0 (most others), so it reads
    # as permissive to anything that looks for a grant.
    grep -qiE 'Business Source License' <<<"$t" && h="$h BUSL"
    grep -qiE 'Server Side Public License' <<<"$t" && h="$h SSPL"
    grep -qiE 'Elastic License' <<<"$t" && h="$h Elastic"
    grep -qiE 'Functional Source License' <<<"$t" && h="$h FSL"
    grep -qiE 'PolyForm [A-Za-z-]+ License' <<<"$t" && h="$h PolyForm"

    # Copyleft. Title, SPDX identifier and operative grant are all matched: a short notice file may
    # carry only one of the three.
    grep -qiE 'GNU AFFERO GENERAL PUBLIC LICENSE|\bAGPL-[0-9]|\bAGPLv[0-9]' <<<"$t" && h="$h AGPL"
    grep -qiE 'GNU LESSER GENERAL PUBLIC LICENSE|GNU LIBRARY GENERAL PUBLIC LICENSE|\bLGPL-[0-9]|\bLGPLv[0-9]' <<<"$t" && h="$h LGPL"
    grep -qiE 'GNU GENERAL PUBLIC LICENSE|\bGPL-[0-9]|\bGPLv[0-9]' <<<"$t" && h="$h GPL"
    grep -qiE 'Eclipse Public License|\bEPL-[0-9]' <<<"$t" && h="$h EPL"
    # CDDL needs a version or the word License adjacent. A bare token match fires on ordinary prose
    # in a NOTICE file, and LIC003 applies this list to NOTICE files.
    grep -qiE 'Common Development and Distribution License|\bCDDL[- ]?[0-9]|\bCDDL License' <<<"$t" && h="$h CDDL"

    # Creative Commons variants that are not CC0. NonCommercial and NoDerivatives are unusable;
    # ShareAlike is copyleft. None belongs on source we redistribute under Apache-2.0.
    #
    # Matched only as part of a licence name. A bare \bNonCommercial\b fires on any NOTICE that uses
    # the word in a sentence, and a gate that goes red on prose gets deleted rather than fixed.
    grep -qiE 'Attribution-NonCommercial|\bCC[ -]BY-NC' <<<"$t" && h="$h CC-BY-NC"
    grep -qiE 'Attribution-ShareAlike|\bCC[ -]BY-SA' <<<"$t" && h="$h CC-BY-SA"
    grep -qiE 'Attribution-NoDerivatives|\bCC[ -]BY-ND' <<<"$t" && h="$h CC-BY-ND"

    # Restrictions layered onto an otherwise permissive grant. These are the riders rule 1 exists
    # to catch, and each is matched on its operative phrase rather than on a licence name.
    grep -qiE 'Commons Clause|right to Sell the Software' <<<"$t" && h="$h CommonsClause"
    grep -qiE 'shall be used for Good, not Evil' <<<"$t" && h="$h JSON-Evil"
    grep -qiE 'All advertising materials mentioning features or use of this software' <<<"$t" && h="$h BSD-4-Clause"

    printf '%s' "$h"
}

# allow_hits <text> — echoes the ids of every allowed licence the text matches.
allow_hits() {
    local t="$1" h=""

    grep -qiE 'Apache License.{0,80}Version 2\.0|\bApache-2\.0\b' <<<"$t" && h="$h Apache-2.0"
    grep -qiE 'Mozilla Public License,? [Vv]ersion 2\.0|\bMPL-2\.0\b' <<<"$t" && h="$h MPL-2.0"
    grep -qiE 'Permission is hereby granted, free of charge|\bMIT License\b' <<<"$t" && h="$h MIT"
    grep -qiE 'Permission to use, copy, modify, and/or distribute this software' <<<"$t" && h="$h ISC"
    grep -qiE 'Redistribution and use in source and binary forms' <<<"$t" && h="$h BSD"
    grep -qiE 'CC0 1\.0 Universal|\bCC0-1\.0\b' <<<"$t" && h="$h CC0-1.0"
    grep -qiE 'free and unencumbered software released into the public domain' <<<"$t" && h="$h Unlicense"
    grep -qiE 'This software is provided .as-is., without any express or implied warranty' <<<"$t" && h="$h Zlib"

    printf '%s' "$h"
}

# allowed <id> — an explicit allowlist, deliberately not "anything not denied".
#
# The default must be STOP, not permit. Otherwise adding a pattern to allow_hits for a licence
# nobody decided about — which is exactly what the LIC002 message invites the next engineer to do —
# silently admits it. Adding an id here is a licence decision and needs a human.
allowed() {
    case "$1" in
        Apache-2.0 | MIT | ISC | BSD | MPL-2.0 | CC0-1.0 | Unlicense | Zlib) return 0 ;;
        *) return 1 ;;
    esac
}

echo "licence gate"

command -v go >/dev/null 2>&1 || die "Go is not installed or not on PATH — run make setup"
[ -f go.mod ] || die "no go.mod in $(pwd) — the licence gate has nothing to resolve"

# --- The runtime module set -------------------------------------------------------------------
#
# `./...` WITHOUT -test is the whole point of this query.
#
#   * With -test, or via `go list -m all`, the set includes modules reachable only from a
#     dependency's own test binary. github.com/hashicorp/golang-lru/v2 (MPL-2.0) is in this repo's
#     `go list -m all` today purely because modernc.org/libc's tests import it — it is not linked
#     into dkp, and failing on it would be failing on code we do not ship.
#   * Scoping to ./cmd/dkp instead would be worse: cmd/dkp does not import internal/store yet, so
#     the binary's graph is cobra + pflag and the entire modernc.org/sqlite tree — the dependency
#     that prompted this gate — would go unexamined.
#
# The set is UNIONED across the release platforms, because `go list` resolves build constraints for
# one GOOS/GOARCH at a time. On this repo a linux-only query returns 11 modules and the union
# returns 14: mousetrap, go-isatty and go-strftime are absent on linux and present in the darwin and
# windows binaries. A gate that runs only on the ubuntu CI runner would never examine them, and a
# GPL dependency behind `//go:build windows` would ship unnoticed.
#
# {{.Module.Dir}} gives the on-disk directory directly. Reconstructing $GOMODCACHE paths by hand
# would break on `replace` directives, and it is exactly the `replace` case that lets the negative
# fixtures run offline against a fabricated GPL module.
# The release matrix is {linux,darwin,windows} x {amd64,arm64} (ROADMAP.md), and all three operating
# systems appear here. One GOARCH per GOOS is enough: build constraints that add a MODULE are
# overwhelmingly GOOS-gated — syscall shims, terminal handling, filesystem notification — while
# GOARCH differences live inside a module, in its assembly and per-arch files. Measured on this repo,
# linux/amd64, linux/arm64 and linux/arm resolve identical module sets, as do darwin/amd64 and
# darwin/arm64, and these three reproduce the full six-platform union exactly. If a dependency is
# ever gated on GOARCH alone, add that platform here.
PLATFORMS="linux/amd64 darwin/arm64 windows/amd64"

# The platform queries are independent, so they run concurrently. Serially this is the slowest part of
# `make check`; the negative fixtures pay it once per fixture and it dominated the unit-test budget.
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

n=0
for platform in $PLATFORMS; do
    n=$((n + 1))
    (
        # GOWORK=off is load-bearing, not tidiness. Under a go.work file every workspace member
        # reports an EMPTY Module.Version — the same signal the main module gives — so a
        # third-party module brought in with `use ./thing` would be skipped as if it were
        # first-party, and the gate would print its success banner having examined nothing.
        # .Module.Main is the correct main-module test and is used below for the same reason.
        # Main is emitted rather than filtered out here: the main module's presence is what makes an
        # empty result mean "go list resolved nothing" rather than "this module has no runtime
        # dependencies", which is a legitimate state and was this repo's own until sqlite landed.
        GOWORK=off GOOS="${platform%/*}" GOARCH="${platform#*/}" \
            go list -deps -f '{{if .Module}}{{.Module.Main}}|{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}' \
            ./... >"$work/out.$n" 2>"$work/err.$n"
        printf '%s' "$?" >"$work/rc.$n"
    ) &
done
wait

n=0
modules=""
for platform in $PLATFORMS; do
    n=$((n + 1))

    # A missing rc file means the subshell died without reporting. Defaulted rather than read
    # bare, so that case produces this gate's own error instead of a stray `cat:` on stderr.
    rc=$(cat "$work/rc.$n" 2>/dev/null || true)
    [ -n "$rc" ] || rc="killed before reporting"

    [ "$rc" = "0" ] || die "go list failed for $platform (status: $rc):
$(sed 's/^/  /' "$work/err.$n" 2>/dev/null)"

    # `go list ./...` EXITS ZERO when nothing matches, warning only on stderr. Folding stderr into
    # the result would make that warning look like output, and the gate would report success having
    # examined nothing.
    if grep -q 'matched no packages' "$work/err.$n"; then
        die "go list matched no packages in $(pwd) for $platform — the gate examined nothing"
    fi

    modules="$modules$(cat "$work/out.$n")"$'\n'
done

modules=$(printf '%s\n' "$modules" | grep -v '^$' | sort -u || true)

# The main module always appears in its own dependency list, so an empty result means `go list`
# resolved nothing rather than "this module has no dependencies".
[ -n "$modules" ] || die "go list resolved no modules at all in $(pwd) — the graph could not be read"

summary=""
count=0

while IFS='|' read -r main path version dir; do
    # This repository is Apache-2.0 by definition and is not a dependency of itself.
    #
    # Identified by the Main flag, NOT by an empty version. Under a go.work file every workspace
    # member also reports an empty version, so the old test would skip a third-party module brought
    # in with `use ./thing` as though it were first-party — and the gate would print its success
    # banner having examined nothing. GOWORK=off above closes that too; this is the second lock.
    [ "$main" != "true" ] || continue
    count=$((count + 1))

    # Under -mod=vendor the module cache is not consulted and Dir is empty, but the licence is
    # sitting in the vendor tree.
    if [ -z "$dir" ] && [ -d "vendor/$path" ]; then
        dir="vendor/$path"
    fi

    if [ -z "$dir" ] || [ ! -d "$dir" ]; then
        violation LIC002 "runtime dependency is not unpacked on disk — run go mod download" \
            "$path@$version"
        continue
    fi

    # Module root only. Every licence-shaped file is collected, then split in two: the PRIMARY
    # grant, which is fully classified, and everything else, which LIC003 deny-scans below.
    #
    # The primary set is the exact conventional names — LICENSE, LICENSE.md, COPYING and so on. A
    # suffixed name such as LICENSE-GO or LICENSE-3RD-PARTY.md is auxiliary by definition: it is
    # either a second grant or a bibliography, and in neither case does it define what the module
    # itself is licensed under.
    if ! all_files=$(find "$dir" -maxdepth 1 -type f \
        \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' \
        -o -iname 'UNLICENSE*' -o -iname 'COPYRIGHT*' -o -iname 'NOTICE*' \) 2>/dev/null | sort); then
        violation LIC002 "could not read the module directory" "$path@$version  ($dir)"
        continue
    fi

    # A primary grant is <NAME> or <NAME>.<ext> — never <NAME>-something.
    PRIMARY_RE='/(LICENSE|LICENCE|COPYING|UNLICENSE)(\.[A-Za-z0-9]+)?$'
    licence_files=$(printf '%s\n' "$all_files" | grep -iE "$PRIMARY_RE" || true)
    auxiliary_files=$(printf '%s\n' "$all_files" | grep -ivE "$PRIMARY_RE" || true)

    # LIC003 — a denied licence in an AUXILIARY licence or notice file.
    #
    # A module's primary LICENSE is its grant. Everything else beside it — LICENSE-GPL,
    # LICENSE-3RD-PARTY.md, NOTICE, COPYRIGHT — is either a second grant in a dual-licensed module
    # or a bibliography of embedded code. Both matter, and neither is classifiable the same way:
    #
    #   * Reading them as grants would fire on every licence a bibliography names, and would fail
    #     modernc.org/memory today, which ships LICENSE-LOGO containing nothing but a URL to a
    #     Wikimedia image page. A gate that goes red on that gets deleted.
    #   * Ignoring them lets a module ship a permissive LICENSE beside its real copyleft grant in
    #     LICENSE-GPL — the standard dual-licence layout — and pass. modernc.org/memory really does
    #     ship LICENSE, LICENSE-GO, LICENSE-MMAP-GO and LICENSE-LOGO; a glob requiring a literal dot
    #     after LICENSE read exactly one of the four.
    #
    # So the denylist alone is applied here, never the allowlist. The question for an auxiliary file
    # is "does this module carry something we cannot ship", not "what is this module licensed under".
    # BSD-4-Clause is dropped from the auxiliary scan. Its advertising clause is an attribution
    # obligation, not a bar on shipping, and NOTICE files routinely reproduce the OpenSSL text that
    # contains it. Firing LIC003 on that would make the gate red on a legitimate dependency, and
    # there is no waiver mechanism yet for a maintainer to record a reviewed exception — so the
    # only available response would be to delete the gate. It still fires as LIC001 when it is the
    # module's own grant, which is where the decision actually belongs.
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        found=$(deny_hits "$(normalise "$f")" | sed 's/ BSD-4-Clause//')
        if [ -n "${found// /}" ]; then
            violation LIC003 \
                "denied licence ($(echo "$found" | tr -s ' ')) in a licence or notice file beside the module's own grant" \
                "$path@$version  $(basename "$f")"
        fi
    done <<<"$auxiliary_files"

    if [ -z "$licence_files" ]; then
        violation LIC002 "runtime dependency ships no licence file" "$path@$version  ($dir)"
        continue
    fi

    # EVERY licence file must clear, not merely the first one that happens to be recognisable.
    #
    # A permissive LICENSE beside a copyleft COPYING is a dual-licensed module and the copyleft half
    # still binds. A permissive LICENSE beside a file this gate cannot classify is a module nobody
    # has actually vetted. Taking the first allowed licence as the verdict would wave both through
    # and leave the fail-closed guarantee holding only for modules that ship exactly one licence
    # file — which is not a guarantee, it is a coincidence.
    denied_ids=""
    allowed_ids=""
    unidentified=""
    names=""

    while IFS= read -r f; do
        [ -n "$f" ] || continue
        base=$(basename "$f")
        names="$names $base"

        text=$(normalise "$f")
        d=$(deny_hits "$text")
        a=$(allow_hits "$text")

        [ -n "$d" ] && denied_ids="$denied_ids$d"
        if [ -n "$a" ]; then
            allowed_ids="$allowed_ids$a"
        elif [ -z "$d" ]; then
            unidentified="$unidentified $base"
        fi
    done <<<"$licence_files"

    if [ -n "$denied_ids" ]; then
        violation LIC001 "runtime dependency under a denied licence ($(echo "$denied_ids" | tr -s ' '))" \
            "$path@$version $names"
        continue
    fi

    if [ -n "$unidentified" ]; then
        violation LIC002 "licence file this gate cannot identify — add it to the denylist in scripts/licence-gate.sh, or take it to a human" \
            "$path@$version  unidentified:$unidentified"
        continue
    fi

    # EVERY recognised licence must be on the allowlist, not merely one of them.
    #
    # Stopping at the first id that clears would mean "at least one recognised licence is allowed",
    # which is the opposite of how denied_ids and unidentified are treated above. It also disarms
    # the guard this allowlist exists to be: someone who teaches allow_hits a licence nobody decided
    # about would get a silent pass on any module whose text also matches the MIT or BSD pattern —
    # and those patterns match a single sentence, so most modules match them.
    verdict=""
    unvetted=""
    for id in $allowed_ids; do
        if allowed "$id"; then
            [ -n "$verdict" ] || verdict="$id"
        else
            unvetted="$unvetted $id"
        fi
    done

    if [ -n "$unvetted" ]; then
        violation LIC002 "licence recognised but not on the allowlist ($(echo "$unvetted" | tr -s ' ')) — a human decides" \
            "$path@$version $names"
        continue
    fi

    if [ -z "$verdict" ]; then
        violation LIC002 "no allowed licence identified" "$path@$version $names"
        continue
    fi

    summary="$summary$verdict"$'\n'
done <<<"$modules"

# count == 0 is NOT an error. A module whose only external dependencies are test-only has an empty
# runtime graph, which is a true statement about it — and was this repository's own state until
# modernc.org/sqlite landed. The vacuous-pass path this gate has to avoid is `go list` resolving
# nothing, which is caught above by the "matched no packages" and empty-module-list checks.

if [ "$fail" -eq 0 ]; then
    printf '%s' "$summary" | sort | uniq -c | while read -r n id; do
        printf '  %-12s %s\n' "$id" "$n"
    done
    printf '  \033[32m%d runtime dependencies, all under allowed licences\033[0m\n' "$count"
fi

# --- The JavaScript dependency set ------------------------------------------------------------
#
# The SPA (web/) landed in Phase 0 PR 6 and dragged in ~200 npm packages. The Go gate above cannot
# see them — a different package manager, a different licence-declaration convention — so a copyleft
# or source-available JS dependency would contaminate the tree unnoticed. This section closes that,
# using the SAME closed allowlist the Go half uses, PLUS two extra permissive licences that appear
# in the current lock and were reviewed: Python-2.0 (argparse) and CC-BY-4.0 (caniuse-lite's data).
#
# Scope: the WHOLE dependency graph, not just `--prod`. The Go gate scopes to the runtime graph
# because a Go test-only import is genuinely not linked into dkp. npm has no such boundary that
# `pnpm licenses list` exposes reliably per-package, and a devDependency's licence still ships in the
# lockfile and runs on every contributor's machine and in CI. Failing closed over all ~200 is the
# safe direction; a narrower scope would be a hole nobody could see. `--prod` alone reports only the
# 15 runtime packages (all MIT today), which would leave the other ~190 exactly as unchecked as
# before this section existed.
#
# `pnpm licenses list --json` emits an object keyed by SPDX id, each value an array of packages. An
# SPDX EXPRESSION (a compound key like "(MIT OR CC0-1.0)") is its own key and is allowed only when
# EVERY token in it is on the allowlist. For an AND that is exactly right — both grants bind. For an
# OR the recipient could pick just one branch, so requiring all is STRICTER than SPDX demands; that is
# a deliberate fail-closed choice, not a correctness claim. It can only over-deny (forcing a human to
# review, e.g. an "(MIT OR GPL-3.0)" dep), never admit a copyleft branch — a GPL inside an OR is
# denied, which is the security-critical direction. "(MIT OR CC0-1.0)" passes because BOTH branches
# are permissive, so the strict rule and the lenient rule agree on the one such key in the lock today.

# js_allowed <spdx-id> — the JS allowlist. The Go set, plus the two reviewed additions. As in the Go
# half, the default is DENY: an id not named here is a human decision, never a silent pass.
js_allowed() {
    case "$1" in
        Apache-2.0 | MIT | ISC | MPL-2.0 | CC0-1.0 | Unlicense | Zlib) return 0 ;;
        BSD-2-Clause | BSD-3-Clause | 0BSD | BSD) return 0 ;;
        Python-2.0 | CC-BY-4.0) return 0 ;;
        *) return 1 ;;
    esac
}

# js_expr_allowed <spdx-expression> — true iff every alternative in an OR-expression is allowed.
# Only the OR form is handled: an AND ("A AND B") requires BOTH, so it is allowed iff both are, which
# is the same all-must-pass loop. Parentheses and the words are stripped and each token checked; a
# WITH exception (e.g. "GPL-2.0 WITH Classpath-exception") collapses to its base, which is denied —
# the safe direction, since no such licence is expected here.
js_expr_allowed() {
    local expr tok
    expr=$(printf '%s' "$1" | tr '()' '  ')
    for tok in $expr; do
        case "$tok" in
            OR | AND | WITH) continue ;;
        esac
        js_allowed "$tok" || return 1
    done

    return 0
}

# Whether this section can run at all. It needs BOTH pnpm (to resolve the graph) and node (to
# classify the JSON), and it must DEGRADE — not die — when they are absent.
#
# The Go check above is toolchain-mandatory: `go` is required for the whole build, so a missing Go
# is a real error. Node/pnpm are not. They are installed for the web-facing CI jobs (`node: "true"`)
# but NOT for `test / unit` or `test / integration`, whose runners have only Go. Those two jobs run
# test/repo/licence_gate_test.go, which shells out to this script against the real tree — so a hard
# `die` here fails a job that was never meant to resolve the JS graph, on a box that cannot.
#
# So when the JS toolchain is absent this section prints a `note:` and is skipped with the Go check's
# verdict preserved; the Go half ALWAYS runs. The authoritative JS enforcement lives in the
# `security / licences` CI job, which installs node+pnpm (see .github/workflows/ci.yml) — that job is
# where a bad JS licence fails the build, and `scripts/**` is in its path filter so this gate cannot
# skip it. When the toolchain IS present (dev boxes, that job) the full JS graph is enumerated and
# the gate fails closed exactly as before.
if [ -f web/package.json ] && ! { command -v pnpm >/dev/null 2>&1 && command -v node >/dev/null 2>&1; }; then
    echo
    echo "licence gate — JavaScript (web/)"
    echo "  note: skipping JS dependency licences — pnpm not installed (enforced by the security/licences CI job)"
elif [ -f web/package.json ]; then
    echo
    echo "licence gate — JavaScript (web/)"

    # The generator is not needed, but the graph must be resolved on disk for `pnpm licenses list` to
    # read each package's licence. Same frozen/no-scripts posture as the build and CI.
    [ -d web/node_modules ] || (cd web && pnpm install --frozen-lockfile --ignore-scripts >/dev/null)

    # `pnpm licenses list --json` over the whole graph. `|| true` because pnpm exits non-zero when it
    # finds licences it considers problematic, and we do our own classification below rather than
    # trusting its verdict — but an EMPTY result must still be caught as a failure, not a vacuous pass.
    js_json=$(cd web && pnpm licenses list --json 2>/dev/null || true)
    [ -n "$js_json" ] || die "pnpm licenses list produced no output — the JS dependency set could not be read"

    # Classify with node (present whenever pnpm is). Emit one `id|count|names` line per licence key,
    # and a leading `TOTAL|<n>` line so an empty graph is distinguishable from a read failure.
    js_report=$(cd web && printf '%s' "$js_json" | node -e '
        let s = ""; process.stdin.on("data", d => s += d).on("end", () => {
            const o = JSON.parse(s);
            let total = 0;
            const lines = [];
            for (const id of Object.keys(o)) {
                const pkgs = o[id] || [];
                total += pkgs.length;
                const names = pkgs.map(p => p.name + "@" + (p.versions || []).join("/")).join(" ");
                lines.push(id + "|" + pkgs.length + "|" + names);
            }
            console.log("TOTAL|" + total);
            for (const l of lines) console.log(l);
        });
    ') || die "could not parse pnpm licenses output"

    js_total=$(printf '%s\n' "$js_report" | awk -F'|' '$1=="TOTAL"{print $2; exit}')
    [ -n "$js_total" ] && [ "$js_total" -gt 0 ] 2>/dev/null || die "the JS dependency graph resolved to nothing — the gate examined no packages"

    js_summary=""
    while IFS='|' read -r id cnt names; do
        [ "$id" = "TOTAL" ] && continue
        # Skip only a wholly blank line (the here-string's trailing newline). A row with an empty id
        # but a package list is a package pnpm could not classify, and it must reach the LIC002 guard
        # below rather than be skipped — failing closed, not silently passing.
        [ -z "$id" ] && [ -z "$cnt" ] && [ -z "$names" ] && continue

        # An empty, "Unknown" or "UNLICENSED" id is a package pnpm could not classify — deny, as
        # LIC002 does for Go. Fail closed: an unidentifiable licence is never a silent pass.
        if [ -z "$id" ] || [ "$id" = "Unknown" ] || [ "$id" = "UNLICENSED" ]; then
            violation LIC002 "JS dependency with no identifiable licence" "$names"
            continue
        fi

        if js_expr_allowed "$id"; then
            js_summary="$js_summary$id|$cnt"$'\n'
        else
            violation LIC001 "JS dependency under a licence not on the allowlist ($id) — a human decides" "$names"
        fi
    done <<<"$js_report"

    if [ "$fail" -eq 0 ]; then
        printf '%s' "$js_summary" | grep -v '^$' | sort | while IFS='|' read -r id cnt; do
            printf '  %-16s %s\n' "$id" "$cnt"
        done
        printf '  \033[32m%d JS dependencies, all under allowed licences\033[0m\n' "$js_total"
    fi
fi

if [ "$fail" -ne 0 ]; then
    printf '\n\033[31mlicence gate failed\033[0m — see the rule ids above.\n'
    printf 'This project is Apache-2.0. A copyleft or source-available dependency contaminates the tree\n'
    printf 'and breaks its relationship with EQdkp Plus. Do not disable this gate (AGENTS.md); drop the\n'
    printf 'dependency, or take it to a human with the licence named.\n'
    exit 1
fi
