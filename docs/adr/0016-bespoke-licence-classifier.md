# ADR-0016 — A bespoke licence classifier, not go-licenses or trivy

**Status:** accepted · **Date:** 2026-08-12 · **Deciders:** owner

## Context and problem statement

The AGPL firewall ([ADR-0010](0010-agpl-clean-room-firewall.md)) rests on no copyleft or
source-available licence reaching the runtime module graph. The gate that enforces it was 556 lines
of bash, and its logic could only be observed through a subprocess — whole licence families had no
test at all. Moving it to Go re-opened the question every reviewer asks: why is this hand-written
when `google/go-licenses` and `trivy` exist? The answer has been given in three issue threads and
needs to stop being re-derived.

## Considered options

| Option | For | Against |
|---|---|---|
| A — `google/go-licenses` as the gate | Maintained by Google; `licenseclassifier` scores similarity against a real corpus and handles module layouts we hand-roll | It answers "which licence is this text most like", so a **rider** returns its permissive base: Commons Clause reports Apache-2.0, the JSON licence reports MIT. Its posture is a deny-list of flagged types, not an allowlist that defaults to STOP |
| B — `trivy` licence scanning | One tool for vulnerabilities and licences, already common in CI | Same similarity model, plus its categories (`FORBIDDEN`, `RESTRICTED`, …) encode *its* policy, not ours. BUSL's `Change License: Apache-2.0` parameter reads permissive to it. A container-scale dependency for a question that is 200 lines of matching |
| C — Keep the bespoke matcher in bash | Already worked; no dependency | Untestable except as a subprocess at ~250 ms a case, so most patterns were never executed by a test; and the platform enumeration was hand-copied into `scripts/third-party-notices.sh` under a comment asking the reader to keep the two in step |
| D — Bespoke classifier in Go, standard library only | Directly unit-testable; keeps the posture the firewall needs; no dependency to propose | We maintain a pattern list that an off-the-shelf tool nominally maintains for us |

## Decision outcome

**Chosen: D.** The gate's job is not "identify this licence". It is "prove nothing here restricts
what this project must be free to do", and those differ precisely where it matters. A restriction
layered onto a permissive grant is the shape that defeats a similarity classifier, and it is not
hypothetical: Commons Clause is Apache-2.0 plus "no Sell", the JSON licence is verbatim MIT plus
"for Good, not Evil", and BUSL names a permissive licence in its own Change License parameter.

The posture, in `internal/licence`:

- **Every pattern is evaluated**, and any denial wins — no early return on the first match.
- **MPL-2.0 §1.12 is stripped before the denylist**, as an exact literal, because that sentence names
  the GNU licences and would otherwise fail every MPL module.
- **The allowlist is explicit and defaults to STOP.** An unrecognised licence is LIC002, not a pass.
- **The graph is unioned across the release platforms**, so a dependency behind `//go:build windows`
  is not missed.

**Enforced by:** `make licence-gate` in `make lint` and the required `security / licences` CI job;
`TestHits_CollectEveryMatchWithinATable_NotJustTheFirst`,
`TestClassify_LayeredRider_IsDeniedWithoutHidingItsPermissiveBase`,
`TestAllowlistAndClassifier_DoNotDrift` and `TestClassify_MPL2_IsNotDeniedByItsOwnCrossReference` for
the posture; and the fixtures in `test/repo/licence_gate_test.go`, which drive the whole gate against
fabricated GPL module trees.

**Boundary.** `go-licenses`, `trivy` and `osv-scanner` may be added as *additional* signals. None may
replace this gate. The tool is repo tooling and not product: `TestLicence_IsNotLinkedIntoTheBinary`
asserts `cmd/dkp`'s package graph never reaches it, which is what keeps "one shipped binary" true
while a second `main` package exists for the gate.

### Consequences

- Good, because the classifier's logic is now unit-tested directly — 103 cases in 0.2 s, including
  the families (EPL, CDDL, FSL, PolyForm, CC BY-ND, ISC, CC0, Zlib) no fixture ever reached.
- Good, because the licence gate and `scripts/third-party-notices.sh` resolve the module graph
  through one function, so the two cannot disagree about which platforms ship.
- Good, because adding a licence to the allowlist stays a human decision with a name on it.
- **Bad, because the pattern list is ours to maintain.** A licence nobody wrote a pattern for stops
  the build as LIC002, and the cost is paid by whoever is adding a dependency at the time.
- **Bad, because regex matching is weaker than a scored classifier.** A reworded, truncated or
  translated LICENSE that `licenseclassifier` would match may be unidentifiable here — fail-closed,
  but red on a legitimate dependency, and a gate that goes red wrongly is a gate someone disables.
- **Bad, because we duplicate effort that may become unnecessary.** If upstream classifiers grow
  rider detection, nothing schedules a review of this decision.
- **Bad, because a second `main` package deviates from "cmd/dkp is the only binary"**, and every
  future reader of the repo map has to be told why.

### Reversal cost

A day. The gate is one package with no callers in product code; swapping it for a tool means
rewriting `make licence-gate` and accepting the posture that tool enforces — which is the decision
above, not the code.
