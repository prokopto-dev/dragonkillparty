package authz

import (
	rolekinds "github.com/prokopto-dev/dragonkillparty/internal/authz/role/kinds"
)

// Role is one built-in role: a named bundle of permission keys, seeded into the role table.
//
// It is the SEED, not a second catalogue (docs/design/01-domain-model.md §5.1: "Permission keys come
// from the catalogue in conventions §6; this table is the seed, not a second catalogue"). Every key in
// Permissions is a key in Catalogue(); TestBuiltinRoles_GrantsAreCatalogueKeys asserts it, and the FK
// from role_permission to permission(key) enforces it at runtime.
type Role struct {
	// ID is the deterministic ULID the seeded row carries. See the RoleID* const block for why they
	// are constants rather than minted.
	ID string

	// Key is the stable name code finds a built-in role by ('owner'). Non-NULL is what MARKS a role
	// built-in: not deletable, not renamable.
	Key string

	// Name is the officer-facing display name, and NameNorm is its normalised form — the column
	// ux_role_name is unique over. Normalisation is NFKC + casefold + strip ' ` - (canonical §8), done
	// in Go; see BuiltinRoles' comment for why these nine are literals.
	Name     string
	NameNorm string

	// Description is one sentence an officer reads in the role editor.
	Description string

	// AppliesTo is which kind of principal may hold the role: internal/authz/role/kinds' vocabulary,
	// referenced through its constants so an illegal value is a compile error rather than a CHECK
	// violation inside the boot transaction.
	AppliesTo string

	// SortOrder is the display order in the role editor, 1-based, in §5.1's order — least capable
	// first, then the two service-account roles.
	SortOrder int64

	// Permissions is the set of catalogue keys this role grants, in catalogue order.
	Permissions []string
}

// The deterministic ULIDs the built-in role rows carry.
//
// Constants rather than minted, for the reason internal/ledger's AccountIDResidue and friends are: a
// seeded row that code and support conversations address by id must have the same id on every fresh
// install, or "check role 01J8Z…" is a sentence nobody can act on and the fresh-install fingerprint
// moves on every run.
//
// THE TAIL IS NUMERIC AND THAT IS NOT LAZINESS. Crockford base32 excludes I, L, O and U to remove
// visual ambiguity, and "officer", "owner", "raid_leader", "guest" and "bot_readonly" cannot be
// spelled without them — a mnemonic tail would have to be legible for some roles and mangled for
// others, which is worse than a consistent number. The number is the role's SortOrder, and the key it
// belongs to is on the same line below.
//
// Each is a valid 26-character Crockford base32 ULID with a zero timestamp prefix.
const (
	RoleIDGuest       = "0000000000000000DKPRB00001"
	RoleIDMember      = "0000000000000000DKPRB00002"
	RoleIDRaider      = "0000000000000000DKPRB00003"
	RoleIDRaidLeader  = "0000000000000000DKPRB00004"
	RoleIDOfficer     = "0000000000000000DKPRB00005"
	RoleIDAdmin       = "0000000000000000DKPRB00006"
	RoleIDOwner       = "0000000000000000DKPRB00007"
	RoleIDBotReadonly = "0000000000000000DKPRB00008"
	RoleIDBotRaid     = "0000000000000000DKPRB00009"
)

// The built-in role keys. Constants so a caller looking one up ('owner', the role the last-holder
// rule protects) cannot misspell it.
const (
	RoleKeyGuest       = "guest"
	RoleKeyMember      = "member"
	RoleKeyRaider      = "raider"
	RoleKeyRaidLeader  = "raid_leader"
	RoleKeyOfficer     = "officer"
	RoleKeyAdmin       = "admin"
	RoleKeyOwner       = "owner"
	RoleKeyBotReadonly = "bot_readonly"
	RoleKeyBotRaid     = "bot_raid"
)

// guestReads is the "six `*.read` keys" docs/design/01-domain-model.md §5.1 gives `guest`, and the set
// is DERIVED rather than guessed — the phrase is older than the catalogue it describes.
//
// The catalogue now holds fourteen keys ending in `.read`. Four of them §5.1 assigns explicitly and
// therefore excludes from guest's six: cms.read (member), audit.read (officer), person.pii.read and
// ops.read (admin). Four more — bank.read, draft.read, recruit.read, policy.read — are among the
// TWENTY keys canonical §6 records as added when the UI mockups were reconciled, which is after §5.1's
// table was written. What is left is exactly six, and they are these.
//
// The four newer read keys are deliberately NOT added on that reasoning alone: recruit.read is
// "members read applications and leave signed feedback" (canonical §6), which is not a guest
// capability, and widening a seeded grant is a security decision rather than an inference. Issue #267
// carries the question to whoever builds the role editor.
func guestReads() []string {
	return []string{
		"roster.read",
		"raid.read",
		"item.read",
		"dkp.read",
		"bid.read",
		"calendar.read",
	}
}

// BuiltinRoles returns the nine roles docs/design/01-domain-model.md §5.1 specifies, in its order.
//
// A FUNCTION returning a FRESH LITERAL, never a package-level var, for the reason Catalogue() is one:
// .claude/rules/go-idioms.md bans package-level mutable state, and a shared slice is one append in a
// test away from an intermittent failure under -shuffle=on.
//
// THE GRANTS ARE COMPOSED, exactly as §5.1 writes them — `member` is `guest` + `cms.read`, `officer`
// is `raid_leader` + ten more. Composition rather than nine flat lists is what keeps the table and
// this function comparable line by line, and TestBuiltinRoles_Composition_MatchesTheDomainModel
// asserts each delta is exactly what the document says it is, so a key added to `member` cannot
// silently reach `owner` unnoticed.
//
// NameNorm IS A LITERAL, and only because all nine names are ASCII. The real normaliser is NFKC +
// casefold + strip ' ` - (canonical §8); NFKC needs golang.org/x/text, which is an indirect dependency
// this package must not promote, and it lands with the roster's name handling. For ASCII input the two
// agree, TestBuiltinRoles_NameNorm_IsTheNormalisedName pins that each literal is what the ASCII
// transform yields, and a general normaliser claiming an NFKC it does not do would be the worse thing
// to ship.
func BuiltinRoles() []Role {
	guest := guestReads()
	member := append(guestReads(), "cms.read")

	raidLeader := append(append([]string{}, member...),
		"raid.create", "raid.update", "raid.finalize", "raid.tick.create", "raid.tick.delete",
		"item.award", "signup.manage")

	officer := append(append([]string{}, raidLeader...),
		"roster.write", "person.merge", "character.claim.approve", "dkp.adjust",
		"bid.manage", "bid.reveal_early", "item.alias.manage", "calendar.write", "cms.write",
		"audit.read")

	admin := append(append([]string{}, officer...),
		"dkp.decay.run", "ledger.reverse", "cms.moderate", "import.run", "import.commit",
		"webhook.manage", "token.mint", "token.revoke", "admin.settings", "admin.security.manage",
		"admin.roles.manage", "admin.backup", "person.pii.read", "ops.read")

	owner := append(append([]string{}, admin...), "admin.owner")

	// The service-account roles. bot_readonly gets guest's read set for the reason guestReads()
	// records: "the `*.read` keys only" predates four of the read keys in the catalogue, and widening
	// a seeded bot grant on an inference is the wrong direction to be wrong in.
	botReadonly := guestReads()

	// NO dkp.adjust, and §5.1 says so in bold. A raid bot records attendance and awards items; a bot
	// that can also mint points is EQdkp's api_key with extra steps, which is the thing ADR-0011
	// exists to refuse.
	botRaid := append(guestReads(),
		"raid.create", "raid.update", "raid.tick.create", "item.award")

	return []Role{
		{
			ID: RoleIDGuest, Key: RoleKeyGuest,
			Name: "Guest", NameNorm: "guest",
			Description: "Read-only access to the surfaces a guild chooses to make public.",
			AppliesTo:   rolekinds.AppliesToUser, SortOrder: 1, Permissions: guest,
		},
		{
			ID: RoleIDMember, Key: RoleKeyMember,
			Name: "Member", NameNorm: "member",
			Description: "An ordinary guild member. Signing up and claiming your own character are ownership, not permissions.",
			AppliesTo:   rolekinds.AppliesToUser, SortOrder: 2, Permissions: member,
		},
		{
			ID: RoleIDRaider, Key: RoleKeyRaider,
			Name: "Raider", NameNorm: "raider",
			Description: "The same capability as Member, under a name guilds that want the rank distinction can assign.",
			AppliesTo:   rolekinds.AppliesToUser, SortOrder: 3, Permissions: member,
		},
		{
			ID: RoleIDRaidLeader, Key: RoleKeyRaidLeader,
			Name: "Raid Leader", NameNorm: "raid leader",
			Description: "Runs raids: opens them, takes attendance ticks and awards loot. Commonly assigned scoped to a raid group.",
			AppliesTo:   rolekinds.AppliesToUser, SortOrder: 4, Permissions: raidLeader,
		},
		{
			ID: RoleIDOfficer, Key: RoleKeyOfficer,
			Name: "Officer", NameNorm: "officer",
			Description: "Runs the guild day to day: the roster, point adjustments, bidding and the audit log.",
			AppliesTo:   rolekinds.AppliesToUser, SortOrder: 5, Permissions: officer,
		},
		{
			ID: RoleIDAdmin, Key: RoleKeyAdmin,
			Name: "Admin", NameNorm: "admin",
			Description: "Runs the instance: decay, reversals, imports, tokens, backups and security settings.",
			AppliesTo:   rolekinds.AppliesToUser, SortOrder: 6, Permissions: admin,
		},
		{
			ID: RoleIDOwner, Key: RoleKeyOwner,
			Name: "Owner", NameNorm: "owner",
			Description: "Everything an Admin can do, plus the owner capability. At least one account must hold it.",
			AppliesTo:   rolekinds.AppliesToUser, SortOrder: 7, Permissions: owner,
		},
		{
			ID: RoleIDBotReadonly, Key: RoleKeyBotReadonly,
			Name: "Read-only Bot", NameNorm: "readonly bot",
			Description: "A service account that can read the guild's data and change nothing.",
			AppliesTo:   rolekinds.AppliesToServiceAccount, SortOrder: 8, Permissions: botReadonly,
		},
		{
			ID: RoleIDBotRaid, Key: RoleKeyBotRaid,
			Name: "Raid Bot", NameNorm: "raid bot",
			Description: "A service account that records raids, attendance ticks and item awards. It cannot adjust points.",
			AppliesTo:   rolekinds.AppliesToServiceAccount, SortOrder: 9, Permissions: botRaid,
		},
	}
}
