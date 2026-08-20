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
// Two halves. Catalogue() and Scopes() are the SOURCE: pure Go, no database, read as text by
// SPEC005. Reconcile() is the projection: it writes the catalogue into the permission table on the
// boot path, stamps orphaned_at on a key the running binary no longer ships, never deletes a row, and
// refuses to boot when a registered route names a key the catalogue does not have.
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
//   - The BUILT-IN ROLE SEED (guest … owner, bot_readonly, bot_raid — 01-domain-model.md §5.1) and
//     the admin.owner holder. A role nobody can be assigned to is a row with no effect: assignment
//     needs app_user and service_account, which are Phase 2 deliverable 1. Issue #264.
//   - authz.Check and the MIDDLEWARE. There is no Principal to check yet.
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
