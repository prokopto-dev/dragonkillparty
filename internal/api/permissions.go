package api

// ExtensionPermission is the OpenAPI extension key naming an operation's permission.
//
// It belongs in huma.Operation.Extensions, never in Metadata. Metadata is tagged `yaml:"-"` in
// Huma and is dropped when the document is marshalled, so an operation that declares its permission
// there produces a spec with no x-dkp-permission — which `make verify-spec` rejects as SPEC004,
// because internal/specgate asserts the property against the committed JSON rather than against the
// in-process registry. internal/specgate.PermissionKey mirrors this constant and
// TestSpecGate_PermissionKey_MatchesAPI asserts the two agree.
const ExtensionPermission = "x-dkp-permission"

// ExtensionScopes is the OpenAPI extension key naming the PAT scopes an operation accepts.
//
// It is declared on every PAT-callable operation, alongside Security and x-dkp-permission
// (docs/development/phase-0-pr5-decisions.md §U4). Four documents required it before PR 4 and no code
// emitted it; PR 5a is where it becomes real. Like x-dkp-permission it goes in Extensions, not
// Metadata — Metadata is tagged `yaml:"-"` and never reaches the document.
//
// The three-case rule TestArch_ScopeCoverage_MatchesSecurity enforces:
//   - an operation whose Security offers a `pat` alternative carries a non-empty x-dkp-scopes, every
//     member resolving in the authz catalogue (getGuild -> ["roster:read"]);
//   - an operation in canonical §6's capability floor is session-only and carries
//     x-dkp-pat-forbidden: true and NO scopes;
//   - an operation that is session-only merely because no scope family covers it declares NEITHER
//     (updateGuild -> admin.settings, which is not in the floor).
//
// Declaring the extension also retires an unverified claim: 02-api-design.md flagged scope arrays on
// non-oauth2 Security as of uncertain legality in OpenAPI 3.1. If x-dkp-scopes is always present as a
// plain extension, that question stops mattering.
const ExtensionScopes = "x-dkp-scopes"

// ExtensionPATForbidden is the OpenAPI extension key marking an operation as forbidden to personal
// access tokens — the capability floor of canonical §6. It is `true` on exactly the operations that
// alter authentication, authorization or bulk-export state, and absent everywhere else. No operation
// in PR 5a sets it: getGuild is PAT-callable and updateGuild is session-only by omission, not by
// being in the floor.
const ExtensionPATForbidden = "x-dkp-pat-forbidden"

// Permission sentinels.
//
// docs/design/02-api-design.md §4.1 defines exactly two values that appear in the permission column
// and are not keys in the generated catalogue in internal/authz:
//
//   - public — no credential required. The operation declares `security: []` EXPLICITLY, which in
//     OpenAPI means "this operation overrides any global requirement and needs no authentication".
//     Omitting `security` entirely means the opposite: inherit the document-level requirement.
//   - self — any authenticated principal, constrained to its own records. Used by /me, /events/*,
//     signups, disputes, claims, votes and notification preferences.
//
// They exist so that "every endpoint declares Security and x-dkp-permission" (AGENTS.md) stays a
// rule with no exceptions rather than a rule with a shrug next to the public endpoints.
const (
	PermissionPublic = "public"
	PermissionSelf   = "self"
)

// SentinelPermissions returns the x-dkp-permission values that are NOT catalogue keys.
//
// `make verify-spec` asserts, as SPEC005, that every other x-dkp-permission value resolves in
// internal/authz/catalogue.go. That file did not exist when this was written and was deliberately not
// created here: .claude/rules/api-endpoints.md calls adding a permission key "a schema change — stop
// and ask", because `role_permission` is FK-constrained to `permission(key)` and a divergent key is a
// boot failure. getMeta is public, so PR 4 needed no key at all.
//
// The consequence is intentional: the first operation that names a real permission — PR 5's
// /api/v1/guild — fails the spec gate until it also creates the catalogue. That is the gate doing
// its job, not a gap.
//
// internal/specgate.SentinelPermissions() mirrors this list, and
// TestSpecGate_SentinelPermissions_MatchAPI asserts the two agree: a value that is a sentinel on one
// side and not on the other is a permission one half of the repository requires a catalogue entry for
// and the other half forbids one for.
func SentinelPermissions() []string {
	return []string{PermissionPublic, PermissionSelf}
}

// HiddenOperationAllowlist returns the only paths permitted to carry `Hidden: true`.
//
// Canonical §7, verbatim: /healthz, /readyz, /metrics, the OAuth callback, and the compat shim.
// Five entries. Note that docs/design/02-api-design.md:149-152 substitutes /feeds/{feed_token}/…
// for the OAuth callback and internal/api/EXAMPLE_ENDPOINT.md:232 says the list has four members;
// canonical is the normative tie-breaker and wins over both, and both are corrected in this PR.
//
// A function returning a fresh literal rather than a package-level var, for the reason
// .claude/rules/go-idioms.md gives: a var slice is package-level mutable state that -shuffle=on and
// t.Parallel() turn into an intermittent failure the day a test appends to it. The property the
// contract actually asks for — that a sixth entry is a deliberate edit a reviewer sees in the diff —
// is preserved either way, because the list is a literal in source.
//
// NOTHING USES THIS AT PR 4, and that is worth stating rather than implying. /healthz and /readyz
// are raw net/http handlers, not Huma operations, so no operation in the registry sets Hidden and
// the arch test finds nothing to reject. The allowlist and its test are installed now because
// canonical §7 is a rule about every operation that will ever exist, and the cheapest moment to
// install a gate is before the thing it gates.
//
// FOUR ENTRIES, NOT FIVE, AND THE MISSING ONE IS DELIBERATE. Canonical §7 permits five things; four
// of them have a path written down somewhere in this repository and one does not. /healthz and
// /readyz are in internal/api; /metrics is canonical §14; the compat shim is
// /api/compat/eqdkp/api.php (docs/design/02-api-design.md:45 and :414). The OAuth callback's path is
// specified NOWHERE — it is named in prose in canonical §7 and in 02-api-design.md:609, and no
// document gives its route.
//
// Guessing it would put a path in a merge-blocking gate that nobody verified, which is the failure
// AGENTS.md's "do not invent a regex and ship it" rule describes. Leaving it out costs nothing today
// and produces exactly the right behaviour later: the PR that adds the OAuth callback with
// Hidden: true fails this gate until it adds the real path here — a deliberate edit a reviewer sees,
// which is what the allowlist is for.
func HiddenOperationAllowlist() []string {
	return []string{
		"/healthz",
		"/readyz",
		"/metrics",
		"/api/compat/eqdkp/api.php",
	}
}
