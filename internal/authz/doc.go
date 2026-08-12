// Package authz is the single source of the permission catalogue and the PAT scope list.
//
// Canonical conventions §6 is explicit: "There is exactly one source:
// internal/authz/catalogue.go. It generates the permission table seed, the OpenAPI
// x-dkp-permission metadata, the PAT scope enum, the authorization-matrix header, and
// docs/reference/permissions.md." Every one of those is downstream of this package; nothing is
// upstream of it. A hand-written permission list anywhere else is forbidden, because
// role_permission is FK-constrained to permission(key) and a divergent list is a boot failure.
//
// WHAT SHIPS AT PHASE 0 PR 5, AND WHAT DOES NOT.
//
// This package is the Go catalogue and nothing more: Catalogue() returns every permission key with
// its Key, Category, Label and Description, and Scopes() returns every PAT scope. There is no
// permission table, no role_permission, no seed and no boot reconciliation — those arrive with the
// role table that makes the FK meaningful, in ROADMAP Phase 2 deliverable 3, and a migration cannot
// be un-shipped. See docs/development/phase-0-pr5-decisions.md §Q1.
//
// The catalogue carries Key, Category, Label and Description and NO policy fields. RequiresStepUp,
// IsDangerous and SortOrder wait for Phase 2, which builds the middleware that can test them against
// a real consumer. The "declare it whole now" argument holds for keys — both SDKs and the Phase 2
// seed derive from them, and growing the list one key per PR would make every early endpoint PR a
// breaking SDK change — and it does not hold for a policy flag nothing derives from.
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
