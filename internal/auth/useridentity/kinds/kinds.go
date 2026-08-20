package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the user_identity enum catalogue — canonical §5's "one Go catalogue" for
// user_identity.provider and user_identity.password_algo.
//
// TWO COLUMNS, ONE PACKAGE, because both belong to one table and therefore to one generated region:
// a catalogue owns its region through a schemaEnumBegin/schemaEnumEnd const pair, ENUM001 matches
// those by identifier name, and a region cannot span two `table` blocks. internal/account/kinds is
// the worked example of a package governing two columns of one table; internal/auth's four tables
// with string enums are four packages for the same reason .claude/rules/migrations.md gives.
//
// A LEAF PACKAGE: standard library and internal/schemaenum only, never internal/auth. See the note
// in internal/auth/appuser/kinds — scripts/gen-enums.sh compiles this before sqlc has run.
//
// WHY IDENTITY IS POLYMORPHIC FROM DAY ONE (docs/design/03-security.md §3.4, §3.5). A local password,
// a Discord account and an OIDC subject are three credentials that can each authenticate the same
// app_user, and WebAuthn is a fourth after 1.0. One row per credential with a provider discriminator
// is what makes "unlinking is blocked if it would leave the account with no usable credential" a
// query rather than a retrofit. Phase 2 Wave 0d ships the table with all three provider values
// declared and only 'local' reachable — the OAuth flows are Wave 2 — because a value added to a
// CHECK later is a 12-step table rebuild, and the vocabulary is not in doubt.
//
// WHAT DRIFT COSTS HERE. provider is half of the unique index (provider, provider_key, subject) that
// makes account takeover by handle reuse impossible (§3.5: identity is the provider id, never the
// username). password_algo has exactly ONE legal value and that is the point: EQdkp Plus carries
// seven verifiers, this product carries argon2id, and a second value appearing here would be the
// first step of importing a legacy hash — which AGENTS.md forbids outright.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between. It IS schemaenum.ErrMarkersMissing rather than a second sentinel
// wrapping it: one condition, one value.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The user_identity.provider vocabulary (docs/design/01-domain-model.md §4.1): WHICH AUTHORITY
// asserts this identity.
//
//   - Local   a password on this instance. subject is the app_user's username_norm.
//   - Discord the OAuth2 provider every P99 guild already runs on. subject is the snowflake — never
//     the handle, which became changeable and REUSABLE after the 2023 pomelo migration.
//   - OIDC    a generic issuer. provider_key discriminates between issuers; subject is `sub`.
//
// A LOCAL IDENTITY IS STRUCTURALLY MANDATORY (§4.1, §3.5 rule 6): first-owner bootstrap is
// local-only, so there is never a state where the only path to admin.owner runs through a third
// party. That is a property of the seeding code, not of this list.
const (
	ProviderLocal   = "local"
	ProviderDiscord = "discord"
	ProviderOIDC    = "oidc"
)

// The user_identity.password_algo vocabulary. NULLABLE, and the CHECK is rendered in the nullable
// form: the column is NULL for every non-local identity and for a local identity whose password has
// been cleared, which is how the importer disables login without inventing a sentinel hash.
//
// ONE VALUE, DELIBERATELY. docs/design/03-security.md §3.1 locks password storage to argon2id, and
// legacy EQdkp hashes are never imported (AGENTS.md). The column exists so the PHC string's
// algorithm is queryable — "which accounts still need a rehash" is a question the rehash-on-login
// path asks — not so a second algorithm can be added quietly.
const AlgoArgon2id = "argon2id"

// Providers returns every legal user_identity.provider, in the order the CHECK constraint carries
// them. A FRESH SLICE from a function, never a package-level var (.claude/rules/go-idioms.md bans
// package-level mutable state); the order is fixed because CheckExpr renders in it, and reordering
// rewrites the CHECK, which Atlas sees as a schema change.
func Providers() []string {
	return []string{
		ProviderLocal,
		ProviderDiscord,
		ProviderOIDC,
	}
}

// PasswordAlgos returns every legal user_identity.password_algo, on the same terms as Providers.
func PasswordAlgos() []string {
	return []string{
		AlgoArgon2id,
	}
}

// IsProvider reports whether v is a legal user_identity.provider — the runtime half, so an identity
// writer refuses a bad provider with a Go error rather than having SQLite name a constraint from
// inside a write transaction.
func IsProvider(v string) bool { return contains(Providers(), v) }

// IsPasswordAlgo reports whether v is a legal user_identity.password_algo.
//
// NULL is NOT expressible here and deliberately so: the column's absence is a property of the row (a
// non-local identity, or a local one with login disabled), and a "" sentinel meaning NULL would be a
// second spelling of "no password" that some caller eventually compares against the wrong one.
func IsPasswordAlgo(v string) bool { return contains(PasswordAlgos(), v) }

func contains(values []string, v string) bool {
	for _, candidate := range values {
		if candidate == v {
			return true
		}
	}

	return false
}

// The columns this catalogue governs. Unexported: the CheckExpr functions are the only things that
// need to name them, and a caller wanting the string wants the whole expression.
const (
	providerColumn     = "provider"
	passwordAlgoColumn = "password_algo"
)

// ProviderCheckExpr renders the body of user_identity's provider CHECK constraint:
//
//	provider IN ('local', 'discord', 'oidc')
//
// Named for its column: this catalogue governs two, so an unqualified CheckExpr would be an
// invitation to render one column's values against the other's — a CHECK that compiles, applies, and
// rejects every row.
func ProviderCheckExpr() string {
	return schemaenum.CheckExpr(providerColumn, Providers())
}

// PasswordAlgoCheckExpr renders the body of user_identity's password_algo CHECK constraint:
//
//	password_algo IS NULL OR password_algo IN ('argon2id')
//
// THE NULLABLE FORM, and it is not a stylistic wrapper. `password_algo IN (…)` is NULL rather than
// true for an identity with no password, and SQLite admits a row whose CHECK is not false — so a bare
// IN list would happen to work while saying something it does not mean.
func PasswordAlgoCheckExpr() string {
	return schemaenum.NullableCheckExpr(passwordAlgoColumn, PasswordAlgos())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside `table
// "user_identity"`. HCL line comments, so Atlas parses the file unchanged; the marker text names the
// catalogue because each region is found by an exact whole-line match on ITS OWN markers.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — user_identity enum CHECKs, from internal/auth/useridentity/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — user_identity enum CHECKs."
)

func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the two user_identity enum CHECKs",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "user_identity"`. No trailing newline: Replace joins it back into the file's line
// stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestUserIdentityKinds_CheckMatchesCatalogue fails on.",
		`  check "user_identity_provider_enum" {`,
		fmt.Sprintf(`    expr = %q`, ProviderCheckExpr()),
		"  }",
		"",
		`  check "user_identity_password_algo_enum" {`,
		fmt.Sprintf(`    expr = %q`, PasswordAlgoCheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock().
// Idempotent, and it touches only this catalogue's region.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
