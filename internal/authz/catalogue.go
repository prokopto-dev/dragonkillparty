package authz

// Permission is one row of the permission catalogue, and one row of the permission table.
//
// SEVEN FIELDS, which is the permission table's eight columns minus orphaned_at. Phase 0 PR 5 shipped
// four — Key, Category, Label, Description — and deferred the three policy fields because nothing
// derived from them and no test could validate one against a consumer that did not exist
// (docs/development/phase-0-pr5-decisions.md §Q1). Phase 2 Wave 0b is that consumer: Reconcile writes
// every field below into the permission table at boot, so a flag that is wrong here is now a wrong row
// in a database rather than an unread constant.
//
// orphaned_at is deliberately NOT a field. It is a property of a DB row after a downgrade — the
// timestamp at which a key that a newer binary had stopped shipping was noticed — so it exists only in
// the table, is written only by Reconcile, and has nothing to say in code.
type Permission struct {
	// Key is the <resource>.<action> permission key, dot-separated and lowercase (canonical §6, §16).
	// It is written as a WHOLE QUOTED LITERAL below, never composed from parts — SPEC005 does a quoted
	// substring match against this file's text. See doc.go.
	Key string

	// Category groups the key for the authorization matrix and the reference page. It is a display
	// grouping, not a security boundary — the capability floor is a set of keys, not a category.
	Category string

	// Label is the short human name shown in the role editor and the matrix header.
	Label string

	// Description is one sentence an officer reads when deciding whether a role should hold this key.
	Description string

	// RequiresStepUp is true for the keys canonical §6's capability floor names: the operations that
	// alter authentication, authorization or bulk-export state, which are session-and-step-up only and
	// carry no PAT scope at all. It is the column the middleware reads
	// (docs/design/01-domain-model.md §5), so it is stored on the row rather than recomputed per
	// request.
	//
	// It is NOT a second definition of the floor. CapabilityFloor() is the definition;
	// TestCatalogue_RequiresStepUp_IsExactlyTheCapabilityFloor compares the two in both directions, so
	// a key flagged here and absent there — or the reverse — is a red test.
	RequiresStepUp bool

	// IsDangerous marks a key the role editor confirms twice before granting, and the matrix renders
	// with a warning. It is an affordance, not a control: nothing about authorization changes.
	//
	// EXACTLY ONE KEY CARRIES IT, and that is not an oversight — docs/design/01-domain-model.md:455
	// specifies bid.reveal_early and no document specifies a second. Guessing the rest would put an
	// invented policy in a database column that survives every later correction, so the flag ships with
	// the one documented case and the rest are decided with the role editor that renders them
	// (issue #265).
	IsDangerous bool

	// SortOrder is the display order of the key in the role editor and the authorization matrix,
	// 1-based. It is derived from this catalogue's position — canonical §6's order, which groups by
	// category — rather than hand-written per row: fifty-eight hand-maintained integers are fifty-eight
	// chances for two keys to claim one slot, and the order is already declared by the list itself.
	SortOrder int64
}

// Scope is one PAT scope: <family>:<verb>, colon-separated (canonical §6).
//
// Scopes are coarser than permissions on purpose — a scope narrows a token, a permission narrows a
// role, and effective capability is the intersection (canonical §6, "The capability floor"). There
// is no admin:* scope and no all-powerful token; the operations that alter authentication,
// authorization or bulk-export state are session-only and carry no scope at all.
type Scope struct {
	// Key is the family:verb scope string, written as a whole quoted literal for the same reason
	// Permission.Key is.
	Key string

	// Label is the short human name shown when a service account mints a token.
	Label string

	// Description is one sentence describing what a token bearing this scope may do.
	Description string
}

// Category names. Constants rather than inline strings so a typo is a compile error and the matrix
// groups stay consistent between the catalogue and any later consumer that reads Category.
const (
	categoryRoster    = "roster"
	categoryRaids     = "raids"
	categoryLoot      = "loot"
	categoryPoints    = "points"
	categoryBidding   = "bidding"
	categoryBank      = "bank"
	categoryCalendar  = "calendar"
	categoryDraft     = "draft"
	categorySwaps     = "swaps"
	categoryRecruit   = "recruitment"
	categoryPolicy    = "policy"
	categoryContent   = "content"
	categoryImport    = "import"
	categoryWebhooks  = "webhooks"
	categoryTokens    = "tokens"
	categoryAdmin     = "administration"
	categorySensitive = "sensitive"
)

// Catalogue returns every permission key in canonical §6's order.
//
// A FUNCTION returning a FRESH LITERAL, never a package-level var. .claude/rules/go-idioms.md bans
// package-level mutable state — a var slice is one append away from an intermittent failure under
// -shuffle=on and t.Parallel() — and api.HiddenOperationAllowlist() and api.AllCodes() are the same
// shape for the same reason. The order matches the fenced list at
// docs/design/00-canonical-conventions.md §6 row by row, because
// TestCatalogue_Permissions_MatchCanonicalConventions compares element by element and in order.
//
// Every Key below is a whole quoted string literal. Do not refactor these into Resource+Action:
// SPEC005 greps this file for `"<key>"` and a composed key fails the gate. See doc.go.
//
// SortOrder is stamped from the position in that list rather than written per row: the order is
// already declared once, by the list, and a second hand-maintained copy of it as fifty-eight integers
// is fifty-eight chances for two keys to claim one slot. Inserting a key therefore renumbers the keys
// after it, which is correct and costs nothing — sort_order is display order, Reconcile rewrites it on
// every boot, and nothing joins on it.
func Catalogue() []Permission {
	permissions := []Permission{
		// Roster and people.
		{Key: "roster.read", Category: categoryRoster, Label: "Read roster", Description: "View the guild roster, members and characters."},
		{Key: "roster.write", Category: categoryRoster, Label: "Edit roster", Description: "Add, edit and archive members and characters."},
		{Key: "person.merge", Category: categoryRoster, Label: "Merge people", Description: "Merge two person records into one, re-parenting their characters."},
		{Key: "character.claim.approve", Category: categoryRoster, Label: "Approve character claims", Description: "Approve or reject a member's claim to a character."},
		{Key: "character.key.verify", Category: categoryRoster, Label: "Verify zone keys", Description: "Move a self-reported zone key to verified, which gates raid eligibility."},

		// Raids.
		{Key: "raid.read", Category: categoryRaids, Label: "Read raids", Description: "View raids, attendance and ticks."},
		{Key: "raid.create", Category: categoryRaids, Label: "Create raids", Description: "Open a new raid."},
		{Key: "raid.update", Category: categoryRaids, Label: "Update raids", Description: "Edit a raid's details before it is finalized."},
		{Key: "raid.finalize", Category: categoryRaids, Label: "Finalize raids", Description: "Finalize a raid, freezing its attendance and ticks."},
		{Key: "raid.tick.create", Category: categoryRaids, Label: "Create ticks", Description: "Award an attendance tick during a raid."},
		{Key: "raid.tick.delete", Category: categoryRaids, Label: "Delete ticks", Description: "Remove an attendance tick from a raid."},
		{Key: "raid.custody.manage", Category: categoryRaids, Label: "Manage custody", Description: "Transfer custody of an in-progress raid between officers."},

		// Loot and items.
		{Key: "item.read", Category: categoryLoot, Label: "Read items", Description: "View the item catalogue and award history."},
		{Key: "item.award", Category: categoryLoot, Label: "Award items", Description: "Record an item award to a character."},
		{Key: "item.alias.manage", Category: categoryLoot, Label: "Manage item aliases", Description: "Add and edit the aliases the parser resolves item names through."},
		{Key: "item.priority.manage", Category: categoryLoot, Label: "Manage item priority", Description: "Edit the loot-priority lists that resolution reads."},

		// Points and the ledger.
		{Key: "dkp.read", Category: categoryPoints, Label: "Read points", Description: "View standings, balances and the ledger."},
		{Key: "dkp.adjust", Category: categoryPoints, Label: "Adjust points", Description: "Create a manual point adjustment."},
		{Key: "dkp.decay.run", Category: categoryPoints, Label: "Run decay", Description: "Run a decay pass over the point pools."},
		{Key: "ledger.reverse", Category: categoryPoints, Label: "Reverse a batch", Description: "Write a reversal batch that undoes a prior ledger batch."},

		// Bidding.
		{Key: "bid.read", Category: categoryBidding, Label: "Read bids", Description: "View bid sessions and, after reveal, the bids in them."},
		{Key: "bid.manage", Category: categoryBidding, Label: "Manage bids", Description: "Open, extend, close, resolve and settle bid sessions."},
		// The one is_dangerous key, and the only one any document specifies
		// (docs/design/01-domain-model.md:455): the UI requires an extra confirmation and every use
		// writes an audit row naming the session, because revealing early is how a sealed auction
		// stops being sealed.
		{Key: "bid.reveal_early", Category: categoryBidding, Label: "Reveal bids early", Description: "Reveal a sealed bid session before its scheduled reveal.", IsDangerous: true},

		// Guild bank.
		{Key: "bank.read", Category: categoryBank, Label: "Read bank", Description: "View the guild bank's contents and requests."},
		{Key: "bank.request", Category: categoryBank, Label: "Request from bank", Description: "Ask the guild bank for an item."},
		{Key: "bank.fulfil", Category: categoryBank, Label: "Fulfil bank requests", Description: "Hand over a requested item and close the delivery handshake."},
		{Key: "bank.manage", Category: categoryBank, Label: "Manage bank", Description: "Add, remove and reconcile the guild bank's inventory."},

		// Calendar and signups.
		{Key: "calendar.read", Category: categoryCalendar, Label: "Read calendar", Description: "View the raid calendar and events."},
		{Key: "calendar.write", Category: categoryCalendar, Label: "Edit calendar", Description: "Create and edit calendar events."},
		{Key: "signup.manage", Category: categoryCalendar, Label: "Manage signups", Description: "Manage the signup list for an event."},

		// Draft and voting.
		{Key: "draft.read", Category: categoryDraft, Label: "Read drafts", Description: "View draft weeks and ballots."},
		{Key: "draft.vote", Category: categoryDraft, Label: "Vote in a draft", Description: "Submit a ranked ballot in an open draft."},
		{Key: "draft.manage", Category: categoryDraft, Label: "Manage drafts", Description: "Open, close and publish draft weeks and quakes."},

		// Main swaps.
		{Key: "swap.request", Category: categorySwaps, Label: "Request a swap", Description: "Ask for a main-character swap quote."},
		{Key: "swap.approve", Category: categorySwaps, Label: "Approve a swap", Description: "Approve or deny a requested main swap."},
		{Key: "swap.policy.manage", Category: categorySwaps, Label: "Manage swap policy", Description: "Edit the pricing and rules that govern main swaps."},

		// Recruitment.
		{Key: "recruit.read", Category: categoryRecruit, Label: "Read applications", Description: "View recruitment applications."},
		{Key: "recruit.comment", Category: categoryRecruit, Label: "Comment on applications", Description: "Leave signed feedback on a recruitment application."},
		{Key: "recruit.decide", Category: categoryRecruit, Label: "Decide on applications", Description: "Accept or reject a recruitment application."},
		{Key: "vouch.manage", Category: categoryRecruit, Label: "Manage vouches", Description: "Record and settle vouches for applicants."},

		// Policies.
		{Key: "policy.read", Category: categoryPolicy, Label: "Read policies", Description: "View guild policies and their acceptance state."},
		{Key: "policy.write", Category: categoryPolicy, Label: "Edit policies", Description: "Author and revise guild policies."},

		// Content and CMS.
		{Key: "cms.read", Category: categoryContent, Label: "Read content", Description: "View articles, media and portal blocks."},
		{Key: "cms.write", Category: categoryContent, Label: "Edit content", Description: "Author and edit articles, media and portal blocks."},
		{Key: "cms.moderate", Category: categoryContent, Label: "Moderate content", Description: "Moderate comments and untrusted rich text."},

		// Import.
		{Key: "import.run", Category: categoryImport, Label: "Run an import", Description: "Run an EQdkp import in dry-run, staging the data without committing it."},
		{Key: "import.commit", Category: categoryImport, Label: "Commit an import", Description: "Commit a staged import into the live database.", RequiresStepUp: true},

		// Webhooks.
		{Key: "webhook.manage", Category: categoryWebhooks, Label: "Manage webhooks", Description: "Create, edit and delete outbound webhooks."},

		// Tokens.
		{Key: "token.mint", Category: categoryTokens, Label: "Mint tokens", Description: "Mint a personal access token or a service-account token.", RequiresStepUp: true},
		{Key: "token.revoke", Category: categoryTokens, Label: "Revoke tokens", Description: "Revoke a personal access token or a service-account token.", RequiresStepUp: true},

		// Administration.
		{Key: "admin.settings", Category: categoryAdmin, Label: "Manage settings", Description: "Change instance configuration that does not affect the security posture, such as the guild name, timezone and point label."},
		{Key: "admin.security.manage", Category: categoryAdmin, Label: "Manage security settings", Description: "Read and change security-affecting configuration — identity-provider credentials, MFA and session policy, the outbound allowlist — and feed tokens.", RequiresStepUp: true},
		{Key: "admin.roles.manage", Category: categoryAdmin, Label: "Manage roles", Description: "Edit roles and role assignments.", RequiresStepUp: true},
		{Key: "admin.backup", Category: categoryAdmin, Label: "Manage backups", Description: "Download and restore database backups.", RequiresStepUp: true},
		{Key: "admin.owner", Category: categoryAdmin, Label: "Owner", Description: "The owner capability: an ordinary permission row, not a hardcoded superuser.", RequiresStepUp: true},

		// Sensitive reads.
		{Key: "person.pii.read", Category: categorySensitive, Label: "Read PII", Description: "Read personally-identifying information in bulk, such as email addresses.", RequiresStepUp: true},
		{Key: "audit.read", Category: categorySensitive, Label: "Read the audit log", Description: "Read the audit log.", RequiresStepUp: true},
		// ops.read is SESSION-ONLY BY OMISSION, not session-plus-step-up. It sits in the sensitive
		// category beside person.pii.read and audit.read, both of which ARE in CapabilityFloor(), and
		// its absence from that list is the intended answer rather than an oversight — Category is a
		// display grouping, not a security boundary (see Permission.Category above).
		//
		// The floor is defined by what a compromise costs, not by what feels sensitive (canonical §6,
		// "The capability floor"): it is the operations that alter authentication, authorization or
		// bulk-export state. ops.read is none of those. It reads job queues, doctor checks and the last
		// ledger-verify result (docs/design/02-api-design.md) — operational status, carrying neither PII
		// nor a security-affecting read. Adding it to the floor would put a re-authentication prompt in
		// front of an officer refreshing a job list, which is the same conflation the admin.settings
		// paragraph in canonical §6 exists to prevent.
		//
		// It is session-only for the ordinary reason: no PAT scope family covers the operational
		// surface, so an /ops operation declares {"session": {}} with no pat alternative, and declares
		// NEITHER x-dkp-scopes NOR x-dkp-pat-forbidden — the "session-only by omission" case of the
		// three-case scope rule (.claude/rules/api-endpoints.md; registerGuild's updateGuild is the
		// worked example). Declaring x-dkp-pat-forbidden here would assert the floor membership this
		// comment denies, and TestArch_ScopeCoverage_MatchesSecurity derives that set from
		// CapabilityFloor(), so it would go red.
		{Key: "ops.read", Category: categorySensitive, Label: "Read operational status", Description: "Read operational status and diagnostics."},
	}

	// 1-based, so a zero SortOrder in a permission row is distinguishable from "the first key" — the
	// column defaults to 0, and a row that kept the default is a row Reconcile never wrote.
	for i := range permissions {
		permissions[i].SortOrder = int64(i) + 1
	}

	return permissions
}

// CapabilityFloor returns the permission keys that are session-and-step-up only and carry no PAT
// scope at all — the operations that alter authentication, authorization or bulk-export state.
//
// Canonical §6 enumerates this set as normative, superseding the three different lists that
// 03-security.md, auth-and-scopes.md and api-contract-guardian.md each carried before Phase 0 PR 5.
// The arch test TestArch_ScopeCoverage_MatchesSecurity derives the x-dkp-pat-forbidden set from THIS
// function rather than from a hand-maintained copy, so the three sources cannot drift apart again
// (decision record §16). A fresh literal, for the same reason Catalogue() is.
//
// The order and membership below match the fenced capability-floor block in canonical §6 exactly:
// TestCapabilityFloor_MatchesCanonicalConventions compares the two element by element and in both
// directions. Editing this list without editing §6 (or the reverse) is a red test, because the arch
// test's x-dkp-pat-forbidden expectation follows this function and would otherwise change silently.
//
// Every key here is also a key in Catalogue(); TestCapabilityFloor_KeysAreInCatalogue asserts it, so
// a floor entry that is not a real permission is a red test rather than an unenforceable rule.
func CapabilityFloor() []string {
	return []string{
		"token.mint",
		"token.revoke",
		"admin.security.manage",
		"admin.roles.manage",
		"admin.backup",
		"admin.owner",
		"person.pii.read",
		"audit.read",
		"import.commit",
	}
}

// Scopes returns every PAT scope in canonical §6's order.
//
// A fresh literal, for the same reason Catalogue() is. Scopes are the coarser token-narrowing
// vocabulary; the order matches the fenced PAT-scopes block in canonical §6 so
// TestCatalogue_Scopes_MatchCanonicalConventions can compare element by element.
//
// The three self-scoped capabilities — bids:place, bank:request and draft:vote — authorise acting
// for the authenticated member's OWN accounts only. A scope check alone cannot express "own accounts
// only", so canonical §6 requires each to carry an authorization-matrix case denying it on another
// member's account; that case lands with the matrix in Phase 2.
func Scopes() []Scope {
	return []Scope{
		{Key: "roster:read", Label: "Read roster", Description: "Read the guild roster."},
		{Key: "roster:write", Label: "Edit roster", Description: "Edit the guild roster."},
		{Key: "raids:read", Label: "Read raids", Description: "Read raids and attendance."},
		{Key: "raids:write", Label: "Write raids", Description: "Create and update raids and ticks."},
		{Key: "dkp:read", Label: "Read points", Description: "Read standings and the ledger."},
		{Key: "dkp:adjust", Label: "Adjust points", Description: "Create point adjustments."},
		{Key: "loot:read", Label: "Read loot", Description: "Read the item catalogue and awards."},
		{Key: "loot:award", Label: "Award loot", Description: "Record item awards."},
		{Key: "bids:read", Label: "Read bids", Description: "Read bid sessions."},
		{Key: "bids:place", Label: "Place bids", Description: "Place and retract bids in an open session, for the token owner's own accounts only."},
		{Key: "bids:manage", Label: "Manage bids", Description: "Open, extend, close, resolve and settle bid sessions."},
		{Key: "logs:ingest", Label: "Ingest logs", Description: "Submit parsed P99 log artefacts."},
		{Key: "calendar:read", Label: "Read calendar", Description: "Read the raid calendar."},
		{Key: "calendar:write", Label: "Write calendar", Description: "Create and edit calendar events."},
		{Key: "cms:read", Label: "Read content", Description: "Read articles, media and policies."},
		{Key: "cms:write", Label: "Write content", Description: "Author articles, media and policies."},
		{Key: "events:subscribe", Label: "Subscribe to events", Description: "Subscribe to the realtime event stream."},
		{Key: "webhooks:manage", Label: "Manage webhooks", Description: "Create, edit and delete webhooks."},
		{Key: "bank:read", Label: "Read bank", Description: "Read the guild bank."},
		{Key: "bank:request", Label: "Request from bank", Description: "Request an item from the bank, for the token owner's own accounts only."},
		{Key: "bank:manage", Label: "Manage bank", Description: "Manage the guild bank's inventory and requests."},
		{Key: "draft:read", Label: "Read drafts", Description: "Read draft weeks and ballots."},
		{Key: "draft:vote", Label: "Vote in drafts", Description: "Submit a ranked ballot, for the token owner's own account only."},
		{Key: "recruit:read", Label: "Read recruitment", Description: "Read recruitment applications."},
		{Key: "recruit:manage", Label: "Manage recruitment", Description: "Comment on and decide recruitment applications."},
		{Key: "swap:read", Label: "Read swaps", Description: "Read and quote main swaps, without committing one."},
		{Key: "swap:manage", Label: "Manage swaps", Description: "Approve, deny and configure main swaps."},
	}
}
