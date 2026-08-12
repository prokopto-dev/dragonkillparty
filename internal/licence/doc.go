// Package licence is the dependency licence gate and the runtime-graph enumeration it shares with
// the third-party notices generator.
//
// The project is Apache-2.0 and its entire relationship with EQdkp Plus (AGPL-3.0) rests on the
// tree staying uncontaminated — see docs/design/00-canonical-conventions.md §15. The gate fails on
// any copyleft or non-open-source licence in the RUNTIME module graph.
//
// Rule ids, in the same shape as scripts/repo-gates.sh:
//
//	LIC001  a runtime dependency is under a denied licence
//	LIC002  a runtime dependency's licence could not be identified, or is not on the allowlist
//	LIC003  a runtime dependency declares embedded third-party code under a denied licence
//
// The gate has no vacuous-pass path. Unlike repo-gates.sh, whose rules gate trees that do not exist
// yet and therefore skip, every input here exists from the moment there is a go.mod: a missing
// toolchain, a missing go.mod, a `go list` failure and a module graph that resolves to nothing are
// all errors, never silent passes.
//
// # Two rules govern classification, and both exist because the obvious implementation is wrong
//
// 1. EVERY pattern is evaluated. There is no early return on the first match.
//
// A classifier that stops at the first licence it recognises cannot see a restriction layered on
// top of a permissive grant, because the grant matches first. That is not a hypothetical shape: the
// Commons Clause is Apache-2.0 plus "the License does not grant to you ... the right to Sell the
// Software", and the JSON licence is verbatim MIT plus "shall be used for Good, not Evil". Both
// would be reported as their permissive base and waved through. Collecting every match and letting
// any denial win is what makes the gate see the rider. [Classify] returns every id that matched,
// for exactly that reason, and no caller may stop early.
//
// 2. The denylist is applied to text with MPL-2.0's §1.12 cross-reference removed.
//
// MPL-2.0's own text names the GNU licences:
//
//	"Secondary License" means either the GNU General Public License, Version 2.0, the GNU
//	Lesser General Public License, Version 2.1, the GNU Affero General Public License, ...
//
// So a denylist applied to raw MPL-2.0 text fires on github.com/hashicorp/golang-lru/v2, which is
// already in this repo's graph. The fix is to delete that one sentence, NOT to identify MPL first
// and stop looking — doing that lets a genuine GPL module through by naming the MPL in a preamble,
// which is exactly the shape a module takes when it vendors a component and concatenates notices.
// If a future MPL variant escapes the strip, the gate fails closed on MPL. That is the safe
// direction, and TestLicenceGate_AllowedLicence_Passes/mpl2 is what would tell us.
//
// # Why this is Go and not shell
//
// The classifier is pure text matching over `go list` output, so its logic is directly testable
// (issue #130): classify_test.go drives every pattern against the licence bodies themselves,
// without a subprocess. The black-box fixture suite in test/repo/licence_gate_test.go still runs
// the whole gate end to end against fabricated module trees, unchanged — the two layers answer
// different questions and neither replaces the other.
package licence
