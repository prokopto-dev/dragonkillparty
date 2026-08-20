// Package authz is the single source of the permission catalogue and the PAT scope list.
//
// Canonical conventions §6 is explicit: "There is exactly one source:
// internal/authz/catalogue.go. It generates the permission table seed, the OpenAPI
// x-dkp-permission metadata, the PAT scope enum, the authorization-matrix header, and
// docs/reference/permissions.md." Every one of those is downstream of this package; nothing is
// upstream of it. A hand-written permission list anywhere else is forbidden, because
// role_permission is FK-constrained to permission(key) and a divergent list is a boot failure.
//
// WHAT THIS PACKAGE IS, AND WHAT IT IS STILL NOT.
//
// Two halves. Catalogue(), Scopes() and BuiltinRoles() are the SOURCE: pure Go, no database, and the
// first of them is read as TEXT by SPEC005. Reconcile() is the projection: it writes the catalogue
// into the permission table on the boot path, seeds the built-in roles and their grants, stamps
// orphaned_at on a key the running binary no longer ships, never deletes a row, and refuses to boot
// when a registered route names a key the catalogue does not have.
//
// THE PERMISSION TABLE IS RECONCILED AND THE ROLE TABLE IS SEEDED, and the two words are not
// interchangeable. A permission row is code's, so every boot makes it match the catalogue. A role row
// is the GUILD'S once it exists — docs/design/01-domain-model.md §5.1 calls this table "the seed, not
// a second catalogue" — so a built-in role that is already there is left alone, grants included.
// Rewriting them would silently restore a permission an officer deliberately revoked, which is a
// security decision undone by a restart.
//
// Phase 0 PR 5 shipped the first half alone, deliberately: there was no permission table, no
// role_permission and therefore no FK, so a bad key was a red `make verify-spec` rather than a boot
// failure, and a migration cannot be un-shipped (docs/development/phase-0-pr5-decisions.md §Q1).
// Phase 2 Wave 0b (issue #261) ships the second half with the four tables that make the FK
// meaningful — permission, role, role_permission, role_assignment — which is ROADMAP Phase 2
// deliverable 3.
//
// Still NOT here, and each has a reason rather than an omission:
//
//   - The admin.owner HOLDER, and the first-run bootstrap that creates it. The owner ROLE is seeded
//     here; who holds it cannot be, because a role_assignment names an app_user or a service_account
//     and both are Phase 2 deliverable 1. Issue #264.
//   - authz.Check. Phase 2 Wave 0d (#273) built the Principal and the middleware that resolves one,
//     so the missing half is now the CHECK itself: the permission lookup, the role-permission
//     intersection with a token's scopes, and the capability floor. Wave 0e.
//   - The GENERATED OUTPUTS canonical §6 lists — the OpenAPI x-dkp-permission metadata, the PAT scope
//     enum, the authorization matrix and docs/reference/permissions.md.
//
// The catalogue now carries all three policy fields — RequiresStepUp, IsDangerous and SortOrder —
// because Reconcile is the consumer their absence was waiting for: every one of them is a column of a
// row this package writes. orphaned_at is still not a field, because it is a fact about a database
// after a downgrade rather than about code.
//
// THE WHOLE LIST SHIPS NOW, for the reason internal/api/errors.go gives about the error enum: the
// catalogue is what the PAT scope enum and the Phase 2 table seed derive from, so publishing it
// whole makes every later endpoint PR a copy-not-ask and makes SPEC005 resolve on day one for every
// route in the resource map. TestCatalogue_Permissions_MatchCanonicalConventions keeps it honest by
// comparing Catalogue() against the fenced §6 list element by element, in both directions — the same
// mechanism TestErrors_Enum_MatchesPublishedCatalogue uses for the error enum.
//
// EVERY KEY IS A WHOLE QUOTED LITERAL, AND THIS IS LOAD-BEARING, NOT STYLE. internal/specgate's
// SPEC005 reads this file as TEXT and asserts that each permission appears in it wrapped in double
// quotes — a quoted exact substring, so `raid.tick` does not satisfy a requirement for
// `raid.tick.create`. That the gate moved from Python to Go (#127) changes nothing here: reading the
// source as text rather than parsing it is the deliberate half, because a parser would resolve a
// composed key and lose the property. A composed key
// (Resource + "." + Action, or a Key() method) produces the right runtime value and FAILS the gate,
// because the literal "raid.tick.create" never appears in the source. This was measured, both
// directions (see the decision record §Q1). Do not "tidy" the catalogue into Resource/Action fields.
package authz

// AuthorizationGapNotice is the disclosure every published description of a credential carries until
// the authorization middleware exists.
//
// IT IS A TRIPWIRE, AND IT IS MEANT TO BE DELETED. It is also the SECOND of them: the first,
// Phase0EnforcementNotice, said that nothing was enforced at all, and it was deleted by Phase 2 Wave
// 0d (issue #273) together with TestGuild_Unauthenticated_IsAKnownPhase0Gap, in the change that made
// an unauthenticated `PATCH /api/v1/guild` answer 401. Narrowing a disclosure is not the same as
// removing it, and replacing it rather than deleting it is what keeps the remaining half visible.
//
// WHAT IS TRUE NOW: an operation that declares `Security` requires a live credential. What is still
// NOT true is anything about capability. internal/auth resolves identity; nothing yet checks that the
// principal HOLDS the `x-dkp-permission` the operation declares, that a token's scopes reach it, or
// that the capability floor's session-and-step-up operations refuse a token. Any live credential
// passes every operation — a member's session can `PATCH /api/v1/guild`, and so can a zero-scope
// token.
//
// WHY IT HAS TO BE ON THE PUBLISHED SURFACE, and this is a security review's finding rather than a
// tidiness one: the OpenAPI security schemes and the generated reference pages describe scopes,
// intersection and step-up in detail. A bot author reading that reasonably concludes a zero-scope
// token is refused. It is not. Describing a control well makes its absence HARDER to notice, not
// easier, so the description has to say so itself.
//
// Delete this constant and its uses in the same change that lands authz.Check and the capability
// floor (Wave 0e). Its consumers are internal/api's security schemes and internal/authz/docgen's two
// reference pages; both have tests asserting the notice is present, which is what makes the deletion
// deliberate rather than a silent drift back to over-promising.
const AuthorizationGapNotice = "**AUTHENTICATION IS ENFORCED; AUTHORIZATION IS NOT YET.** An " +
	"operation that names a credential requires one — an anonymous request is refused with `401`. " +
	"What the server does not check yet is CAPABILITY: no permission is verified, scopes are not " +
	"intersected with the service account's role, and the capability floor is documented rather than " +
	"enforced. Until the authorization middleware lands, any live credential passes every operation. " +
	"See \"Known gaps\" in SECURITY.md."
