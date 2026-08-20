package authz_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/authz"
	rolekinds "github.com/prokopto-dev/dragonkillparty/internal/authz/role/kinds"
)

// The built-in role seed's tests. docs/design/01-domain-model.md §5.1 is the source, and it is a prose
// table rather than a fenced list — "the six `*.read` keys", "`officer` + `roster.write` …" — so it
// cannot be parsed the way canonical §6's fenced blocks are.
//
// What is asserted instead is the STRUCTURE the table states, which is checkable without parsing it:
// each role's grants are its parent's plus exactly the named delta. That catches the mistake a flat
// list of nine grant sets would hide — a key added to `member` silently reaching `owner`, or a key
// meant for `admin` landing on `officer` — and it is the same property a reader checks by eye when
// comparing the function against the table.

// roleByKey returns the built-in role with the given key.
func roleByKey(t *testing.T, key string) authz.Role {
	t.Helper()

	for _, r := range authz.BuiltinRoles() {
		if r.Key == key {
			return r
		}
	}

	require.FailNowf(t, "no such built-in role", "%s is not in BuiltinRoles()", key)

	return authz.Role{}
}

// delta returns the keys in child that are not in parent, and asserts child is a superset of parent.
func delta(t *testing.T, parent, child authz.Role) []string {
	t.Helper()

	have := make(map[string]struct{}, len(child.Permissions))
	for _, p := range child.Permissions {
		have[p] = struct{}{}
	}

	for _, p := range parent.Permissions {
		_, ok := have[p]
		require.Truef(t, ok, "%s does not grant %q, which its parent %s does — the roles in §5.1 are "+
			"cumulative, so a role that drops one of its parent's keys is a break in the ladder",
			child.Key, p, parent.Key)
	}

	in := make(map[string]struct{}, len(parent.Permissions))
	for _, p := range parent.Permissions {
		in[p] = struct{}{}
	}

	var extra []string

	for _, p := range child.Permissions {
		if _, ok := in[p]; !ok {
			extra = append(extra, p)
		}
	}

	return extra
}

// TestBuiltinRoles_GrantsAreCatalogueKeys is the FK, asserted before the database gets a chance to.
//
// role_permission references permission(key), so a grant naming a key the catalogue does not ship
// fails the INSERT inside the boot transaction — after the permission upserts have been written, which
// means the whole reconciliation rolls back and the instance does not start. That is the correct
// runtime behaviour and a terrible way to find a typo, so this fails first, in `make test-unit`, with
// the role and the key named.
func TestBuiltinRoles_GrantsAreCatalogueKeys(t *testing.T) {
	t.Parallel()

	keys := make(map[string]struct{}, len(authz.Catalogue()))
	for _, p := range authz.Catalogue() {
		keys[p.Key] = struct{}{}
	}

	for _, role := range authz.BuiltinRoles() {
		require.NotEmptyf(t, role.Permissions, "built-in role %s grants nothing", role.Key)

		for _, granted := range role.Permissions {
			_, ok := keys[granted]
			require.Truef(t, ok,
				"built-in role %s grants %q, which is not in internal/authz/catalogue.go. "+
					"role_permission is FK-constrained to permission(key), so this is a boot failure "+
					"waiting for a fresh install.", role.Key, granted)
		}
	}
}

// TestBuiltinRoles_Grants_AreUnique guards the composition against a key being granted twice — which
// the ladder makes easy, since every role starts from its parent's list. A duplicate is a primary-key
// violation on role_permission(role_id, permission_key), inside the boot transaction.
func TestBuiltinRoles_Grants_AreUnique(t *testing.T) {
	t.Parallel()

	for _, role := range authz.BuiltinRoles() {
		seen := make(map[string]bool, len(role.Permissions))

		for _, granted := range role.Permissions {
			require.Falsef(t, seen[granted],
				"built-in role %s grants %q twice; role_permission's primary key is "+
					"(role_id, permission_key), so the second INSERT aborts the boot", role.Key, granted)

			seen[granted] = true
		}
	}
}

// TestBuiltinRoles_Composition_MatchesTheDomainModel is the structural comparison against §5.1.
//
// Each delta is asserted as an EXACT set, not a subset: a key that §5.1 puts on `admin` and this
// function puts on `officer` is a capability given to a whole tier of people who should not have it,
// and a subset assertion would pass on it.
func TestBuiltinRoles_Composition_MatchesTheDomainModel(t *testing.T) {
	t.Parallel()

	guest := roleByKey(t, authz.RoleKeyGuest)
	member := roleByKey(t, authz.RoleKeyMember)
	raider := roleByKey(t, authz.RoleKeyRaider)
	raidLeader := roleByKey(t, authz.RoleKeyRaidLeader)
	officer := roleByKey(t, authz.RoleKeyOfficer)
	admin := roleByKey(t, authz.RoleKeyAdmin)
	owner := roleByKey(t, authz.RoleKeyOwner)
	botReadonly := roleByKey(t, authz.RoleKeyBotReadonly)
	botRaid := roleByKey(t, authz.RoleKeyBotRaid)

	// "the six *.read keys" — the derivation is in guestReads()'s comment: the fourteen catalogue keys
	// ending in .read, minus the four §5.1 assigns elsewhere, minus the four added after §5.1 was
	// written. Pinned as an exact list because it is the one grant set the document does not enumerate.
	require.Equal(t, []string{
		"roster.read", "raid.read", "item.read", "dkp.read", "bid.read", "calendar.read",
	}, guest.Permissions, "guest is the six read keys of §5.1; see guestReads() for the derivation")

	require.Equal(t, []string{"cms.read"}, delta(t, guest, member),
		"§5.1: member is `guest` + `cms.read`")

	require.Equal(t, member.Permissions, raider.Permissions,
		"§5.1: raider is `= member` — a distinct assignable name, not a distinct capability")

	require.ElementsMatch(t, []string{
		"raid.create", "raid.update", "raid.finalize", "raid.tick.create", "raid.tick.delete",
		"item.award", "signup.manage",
	}, delta(t, member, raidLeader), "§5.1: raid_leader is `member` + these seven")

	require.ElementsMatch(t, []string{
		"roster.write", "person.merge", "character.claim.approve", "dkp.adjust", "bid.manage",
		"bid.reveal_early", "item.alias.manage", "calendar.write", "cms.write", "audit.read",
	}, delta(t, raidLeader, officer), "§5.1: officer is `raid_leader` + these ten")

	require.ElementsMatch(t, []string{
		"dkp.decay.run", "ledger.reverse", "cms.moderate", "import.run", "import.commit",
		"webhook.manage", "token.mint", "token.revoke", "admin.settings", "admin.security.manage",
		"admin.roles.manage", "admin.backup", "person.pii.read", "ops.read",
	}, delta(t, officer, admin), "§5.1: admin is `officer` + these fourteen")

	require.Equal(t, []string{"admin.owner"}, delta(t, admin, owner),
		"§5.1: owner is `admin` + `admin.owner`, and admin.owner is an ordinary permission row "+
			"(ADR-0011) rather than a hardcoded branch")

	require.Equal(t, guest.Permissions, botReadonly.Permissions,
		"bot_readonly is the read keys only; see guestReads() for why it is the same six")

	require.ElementsMatch(t, []string{
		"raid.create", "raid.update", "raid.tick.create", "item.award",
	}, delta(t, botReadonly, botRaid), "§5.1: bot_raid is `bot_readonly` + these four")
}

// TestBuiltinRoles_BotRaid_CannotAdjustPoints is the one grant §5.1 calls out in bold, and it gets its
// own test because it is the product's founding claim in miniature.
//
// A raid bot records attendance and awards items. A bot that can also mint points is EQdkp's `api_key`
// with extra steps — the thing ADR-0011 exists to refuse — and the failure mode is a compromised bot
// token quietly writing itself a balance, which the append-only ledger then makes permanent.
func TestBuiltinRoles_BotRaid_CannotAdjustPoints(t *testing.T) {
	t.Parallel()

	for _, key := range []string{authz.RoleKeyBotReadonly, authz.RoleKeyBotRaid} {
		role := roleByKey(t, key)

		require.NotContains(t, role.Permissions, "dkp.adjust",
			"%s must not grant dkp.adjust (§5.1, in bold)", key)
		require.NotContains(t, role.Permissions, "ledger.reverse", "%s must not grant ledger.reverse", key)
		require.NotContains(t, role.Permissions, "admin.owner", "%s must not grant admin.owner", key)
	}
}

// TestBuiltinRoles_OnlyOwnerHoldsTheOwnerCapability pins the shape of the escalation ladder's top.
//
// admin.owner is an ordinary permission row, and the only built-in role that grants it is `owner`. A
// second role holding it would make "the last account holding admin.owner cannot be demoted"
// (03-security.md §4.3) a rule about a set nobody can enumerate from the role list.
func TestBuiltinRoles_OnlyOwnerHoldsTheOwnerCapability(t *testing.T) {
	t.Parallel()

	var holders []string

	for _, role := range authz.BuiltinRoles() {
		if slices.Contains(role.Permissions, "admin.owner") {
			holders = append(holders, role.Key)
		}
	}

	require.Equal(t, []string{authz.RoleKeyOwner}, holders,
		"exactly one built-in role grants admin.owner")
}

// TestBuiltinRoles_ServiceAccounts_HoldNoCapabilityFloorKey checks the seed against canonical §6's
// capability floor, on the one axis the floor actually constrains.
//
// THE FLOOR IS ABOUT THE CREDENTIAL, NOT ABOUT THE ROLE, and getting that backwards is easy: an
// earlier draft of this test asserted that only `admin` and `owner` may hold a floor key, and it went
// red on `officer` + `audit.read` — which §5.1 grants deliberately. The two documents do not conflict.
// A floor key means the OPERATION is session-and-step-up only and carries no PAT scope; it says
// nothing about which role may hold the permission. An officer reading the audit log through a
// re-authenticated browser session is exactly the intended shape.
//
// What the floor DOES rule out is a service account holding one. Those operations have no scope at
// all, and a service account has no session to step up, so the grant could never be exercised — it
// would be capability on paper that the middleware always refuses, which is how a role list stops
// describing what anyone can actually do.
func TestBuiltinRoles_ServiceAccounts_HoldNoCapabilityFloorKey(t *testing.T) {
	t.Parallel()

	floor := authz.CapabilityFloor()

	for _, role := range authz.BuiltinRoles() {
		if role.AppliesTo != rolekinds.AppliesToServiceAccount {
			continue
		}

		for _, key := range floor {
			require.NotContainsf(t, role.Permissions, key,
				"built-in role %s applies to a service account and grants the capability-floor key "+
					"%q. Floor operations are session-and-step-up only and carry no PAT scope, so a "+
					"service account can never exercise it.", role.Key, key)
		}
	}
}

// TestBuiltinRoles_AreWellFormed covers the row-shaped properties: the ids are valid, distinct,
// deterministic ULIDs; the keys and names are distinct; applies_to is in its catalogue; and sort_order
// is 1-based and dense.
//
// The ids matter more than they look. They are written into a database on every fresh install and are
// how code and a support conversation address a built-in role, so a duplicate is two roles that are the
// same row and an invalid one is a value the ULID type will refuse the day something parses it.
func TestBuiltinRoles_AreWellFormed(t *testing.T) {
	t.Parallel()

	// Crockford base32: the digits plus the alphabet with I, L, O and U removed.
	ulid := regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{26}$`)

	seenID := map[string]bool{}
	seenKey := map[string]bool{}
	seenName := map[string]bool{}

	roles := authz.BuiltinRoles()

	for i, role := range roles {
		require.Regexpf(t, ulid, role.ID,
			"role %s has id %q, which is not a 26-character Crockford base32 ULID (no I, L, O or U)",
			role.Key, role.ID)

		require.Falsef(t, seenID[role.ID], "two built-in roles share the id %q", role.ID)
		require.Falsef(t, seenKey[role.Key], "two built-in roles share the key %q", role.Key)
		require.Falsef(t, seenName[role.NameNorm], "two built-in roles normalise to the name %q — "+
			"ux_role_name is unique over name_norm, so the second INSERT aborts the boot", role.NameNorm)

		seenID[role.ID] = true
		seenKey[role.Key] = true
		seenName[role.NameNorm] = true

		require.NotEmptyf(t, role.Name, "%s has no display name", role.Key)
		require.NotEmptyf(t, role.Description, "%s has no description", role.Key)

		require.Truef(t, rolekinds.IsAppliesTo(role.AppliesTo),
			"%s has applies_to %q, which is not in internal/authz/role/kinds", role.Key, role.AppliesTo)

		require.Equalf(t, int64(i)+1, role.SortOrder,
			"%s has SortOrder %d at position %d; the seed is 1-based and dense, in §5.1's order",
			role.Key, role.SortOrder, i)
	}

	require.Len(t, roles, 9, "§5.1 specifies nine built-in roles")
}

// TestBuiltinRoles_NameNorm_IsTheNormalisedName pins each literal against the transform it stands in
// for.
//
// name_norm is normalised in Go — NFKC + casefold + strip ' ` - (canonical §8) — and that normaliser
// does not exist yet: NFKC needs golang.org/x/text, an indirect dependency this package must not
// promote, and it lands with the roster's name handling. All nine names are ASCII, where NFKC is the
// identity and casefold is ToLower, so the literals are checkable against the ASCII half today. The
// day the real normaliser lands, this test is what says the seeded values still agree with it — which
// matters because ux_role_name is unique over name_norm, so a custom role named "Guest" must collide
// with the built-in one.
func TestBuiltinRoles_NameNorm_IsTheNormalisedName(t *testing.T) {
	t.Parallel()

	asciiNormalise := func(name string) string {
		out := strings.ToLower(name)
		for _, strip := range []string{"'", "`", "-"} {
			out = strings.ReplaceAll(out, strip, "")
		}

		return out
	}

	for _, role := range authz.BuiltinRoles() {
		require.Equalf(t, asciiNormalise(role.Name), role.NameNorm,
			"%s has name %q and name_norm %q; name_norm is casefolded with ' ` and - stripped",
			role.Key, role.Name, role.NameNorm)

		require.NotContains(t, role.NameNorm, "'", "%s: name_norm still holds an apostrophe", role.Key)
		require.NotContains(t, role.NameNorm, "-", "%s: name_norm still holds a hyphen", role.Key)
	}
}
