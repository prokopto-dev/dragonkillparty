package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the account enum catalogue — canonical §5's "one Go catalogue" for account.kind
// and account.system_key.
//
// WHY THIS FILE EXISTS, and the two columns arrive here for different reasons.
//
// account.system_key was written THREE times and generated from none of them (#51): a const block in
// internal/strategy, a hand-written CHECK in db/schema.hcl, and the seed rows of
// db/migrations-sqlite/000003_ledger.sql. The comment in internal/ledger/account.go used to say so
// out loud — "these strings are identical to the account.system_key CHECK" — which is the workaround,
// not the fix. The two lists fail in opposite directions and both failures land late: a fifth key
// added in Go only routes a split to an account the CHECK rejects, at the INSERT, INSIDE the commit
// transaction; added to the CHECK only, nothing refuses the value early and the first thing to notice
// is a NULL system-account lookup. system_key is the one with real blast radius, because the
// degenerate routes of the largest-remainder allocator (residue, write_off, guild_bank) are what keep
// the Conserved invariant verifiable — a split that cannot reach its system account has nowhere to
// put the remainder.
//
// account.kind had no Go catalogue at all (#53), and two unexported copies of one of its values:
// internal/strategy and internal/ledger each carried `const accountKindSystem = "system"` against the
// same column, the second restating the first because neither could see the schema.
//
// WHY THE ACCOUNT VOCABULARY IS ITS OWN PACKAGE. It is not the ledger's and it is not the strategy's.
// internal/strategy declared the system keys because a planner has to name the guild bank without
// importing the package that knows what a guild bank's row id is (law 3), and internal/ledger
// re-exported them because it owns the seeded rows — so BOTH sides of that boundary need the
// vocabulary and neither can own it. A catalogue under either would make the other import a package
// it has no business depending on to name a column both write. internal/audit/kinds is here for the
// same reason on a different table.
//
// A LEAF PACKAGE WITH NO IMPORTS BUT THE STANDARD LIBRARY AND internal/schemaenum, which is itself
// such a leaf. That is a hard constraint rather than a tidiness preference: scripts/gen-enums.sh is
// the FIRST step of `make gen` and runs internal/ledger/enumgen, which reaches this. Anything it
// compiles must build BEFORE sqlc runs, or a tree whose generated code does not build can no longer
// run `make gen` to repair itself. TestGen_EnumGenerator_DependsOnNoGeneratedCode holds that open.
// It is also what lets internal/strategy import this at all: internal/schemaenum reaches no store, so
// law 3 is untouched and the purity audit (internal/strategy/arch_test.go) walks the real graph and
// would say so if that changed.
//
// THE DIRECTION OF TRUTH IS: this file → db/schema.hcl (via `make gen`) → the migration (via
// `make migration`) → the database, and separately this file → internal/strategy's and
// internal/ledger's constants. Nothing reads it in the other direction.
//
// ADDING A SYSTEM KEY IS A SCHEMA CHANGE, AND MORE. Appending here does not change a deployed
// database: run `make gen` to rewrite the CHECK in db/schema.hcl, then `make migration
// NAME=<snake_case>` and read what Atlas wrote — a CHECK change on SQLite is the 12-step table
// rebuild (.claude/rules/migrations.md). A new system key also needs a seeded row with a
// deterministic id, or every lookup of it returns store.ErrNotFound on a fresh install:
// internal/ledger.SystemAccountIDs is the pairing, and TestSystemAccountIDs_CoverTheCatalogue fails
// until the new key has one.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between.
//
// It IS schemaenum.ErrMarkersMissing rather than a second sentinel wrapping it: one condition, one
// value, so `errors.Is` gives the same answer whichever name the caller reaches for.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The account.kind vocabulary, as the Go const block canonical §5 requires: WHOSE BALANCE this is.
// The two paired CHECKs in db/schema.hcl (account_person_shape, account_system_shape) tie the kind to
// which of person_id / system_key is populated, so the value is not advisory.
const (
	KindPerson = "person" // a human's account; person_id is set and system_key is NULL
	KindSystem = "system" // one of the ledger-addressable non-human accounts below
)

// The account.system_key vocabulary: the ledger-addressable non-human targets that make zero-sum
// splits, rot handling and write-offs expressible (docs/design/01-domain-model.md §6.1). NULL for a
// person account, which is why the CHECK is rendered in schemaenum's nullable form.
//
//   - GuildBank    receives solo-kill and no-attendee awards per the pool's solo_policy.
//   - Residue      absorbs the unallocatable remainder of a largest-remainder split.
//   - WriteOff     receives the debit for a rotted item nobody bought.
//   - ImportOpening is the counter-account for opening balances brought in by the EQdkp importer.
//
// Named constants rather than bare literals because a consumer needs a SYMBOL to reference:
// `ctx.SystemAccount(SystemKeyGuildBank)` is a compile error when it is misspelled, where
// `ctx.SystemAccount("gild_bank")` is valid Go that fails at runtime with a not-found error naming a
// key nobody typed on purpose.
const (
	SystemKeyGuildBank     = "guild_bank"
	SystemKeyResidue       = "residue"
	SystemKeyWriteOff      = "write_off"
	SystemKeyImportOpening = "import_opening"
)

// Kinds returns every legal account.kind, in the order the CHECK constraint carries them.
//
// A FUNCTION returning a FRESH SLICE of the constants above, never a package-level var —
// .claude/rules/go-idioms.md bans package-level mutable state, and a shared slice is one append in a
// test away from an intermittent failure under -shuffle=on. internal/ledger/kinds.BatchKinds() and
// internal/audit/kinds.ActorKinds() are the same shape for the same reason.
//
// The order is not semantic but it is FIXED: CheckExpr renders in this order, so reordering rewrites
// the CHECK expression, which Atlas sees as a schema change and a migration nobody wanted.
func Kinds() []string {
	return []string{
		KindPerson,
		KindSystem,
	}
}

// SystemKeys returns every legal account.system_key, in the order the CHECK constraint carries them.
// A fresh slice over the constants above, for the reason Kinds is one.
//
// THE ORDER IS THE COMMITTED CHECK'S, not the domain model's narrative order (residue first) nor the
// seed rows'. It is the shipped expression's order because that is what makes a regeneration
// byte-identical; every reader who wants a stable order for display should sort.
func SystemKeys() []string {
	return []string{
		SystemKeyGuildBank,
		SystemKeyResidue,
		SystemKeyWriteOff,
		SystemKeyImportOpening,
	}
}

// IsKind reports whether v is a legal account.kind.
//
// The RUNTIME half of the catalogue: a caller writing an account row can refuse a bad kind with a Go
// error naming the legal values, rather than have SQLite name a constraint from inside a write
// transaction that has already done work. There is no account writer at Phase 0 — the four system
// accounts arrive as seed rows — so this has one caller today (the drift test) and exists so the
// first writer has something to call instead of a literal.
func IsKind(v string) bool { return contains(Kinds(), v) }

// IsSystemKey reports whether v is a legal account.system_key. The runtime half of SystemKeys, for
// the reason IsKind is.
//
// NULL is NOT expressible here and deliberately so: the column's absence is a property of the row
// (kind = 'person'), which account_system_shape enforces, and a `""` sentinel meaning NULL would be a
// second spelling of "no system key" that some caller eventually compares against the wrong one.
func IsSystemKey(v string) bool { return contains(SystemKeys(), v) }

func contains(values []string, v string) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}

	return false
}

// The columns this catalogue governs. Unexported: the CheckExpr functions below are the only things
// that need to name them, and a caller wanting the string wants the whole expression.
const (
	kindColumn      = "kind"
	systemKeyColumn = "system_key"
)

// KindCheckExpr renders the body of account's kind CHECK constraint:
//
//	kind IN ('person', 'system')
//
// Named for its column, where internal/audit/kinds' actor-kind renderer once was not: this catalogue
// governs two columns, so an unqualified CheckExpr would be an invitation to render one column's
// values against the other's — which is a CHECK that compiles, applies, and rejects every row.
func KindCheckExpr() string {
	return schemaenum.CheckExpr(kindColumn, Kinds())
}

// SystemKeyCheckExpr renders the body of account's system_key CHECK constraint:
//
//	system_key IS NULL OR system_key IN ('guild_bank', 'residue', …)
//
// THE NULLABLE FORM, and it is not a stylistic wrapper. `system_key IN (…)` is NULL rather than true
// for a person account, and SQLite admits a row whose CHECK is not false — so a bare IN list would
// happen to work while saying something it does not mean. It also would not be the expression the
// database already carries, and a rendered CHECK that differs from the committed one by so much as a
// word is a 12-step rebuild of account rather than a comment-only diff.
func SystemKeyCheckExpr() string {
	return schemaenum.NullableCheckExpr(systemKeyColumn, SystemKeys())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside `table "account"`.
// Everything between them is written by `make gen`; everything outside them — including the paired
// account_person_shape and account_system_shape CHECKs, which are structural rather than enum truth —
// is hand-authored schema truth.
//
// HCL line comments, so Atlas parses the file unchanged and the region is invisible to the diff
// engine. The marker text names the catalogue because db/schema.hcl carries several generated regions
// and each is found by an exact whole-line match on ITS OWN markers: two regions sharing a marker line
// would each rewrite the other's.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — account enum CHECKs, from internal/account/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — account enum CHECKs."
)

// schemaRegion is the marked region this catalogue owns. A function rather than a package-level var,
// for the reason Kinds is one.
func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the two account enum CHECKs",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "account"`.
//
// No trailing newline: Replace joins it back into the file's line stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestAccountKinds_CheckMatchesCatalogue fails on.",
		`  check "account_kind_enum" {`,
		fmt.Sprintf(`    expr = %q`, KindCheckExpr()),
		"  }",
		"",
		`  check "account_system_key_enum" {`,
		fmt.Sprintf(`    expr = %q`, SystemKeyCheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock(),
// and is one of the three rewrites `make gen` composes before writing db/schema.hcl back.
//
// Idempotent, and it touches ONLY this catalogue's region: rendering an already-current file returns
// it unchanged, which is what lets the drift test be "generating again changes nothing" and lets this
// render compose with the other catalogues' in any order.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
