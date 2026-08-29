package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	identitykinds "github.com/prokopto-dev/dragonkillparty/internal/auth/useridentity/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// Creating a local account: the normalisation, the password policy and the two-row write that
// together make one login (docs/design/01-domain-model.md §4.1, docs/design/03-security.md §3.2).
//
// IT LIVES IN internal/auth BECAUSE THE ROWS ARE THIS PACKAGE'S. app_user and user_identity are the
// credential tables the resolver reads, argon2id is this package's hash, and username_norm is the
// column its unique index is over. What is NOT here is the ROLE the account is given: that is a
// role_assignment, internal/authz owns it, and keeping the two apart is the same split ADR-0028
// draws between authentication and authorization.
//
// THERE IS NO Service METHOD, and the shape is deliberate: InsertLocalAccount takes a
// store.Queries rather than opening its own transaction, because its callers — the first-run
// bootstrap and `dkp admin create` — must write the account and its owner role in ONE transaction.
// An account created without its role is an account nobody can use and nothing cleans up; a role
// granted to an account that does not exist is a dangling assignment, because role_assignment's
// subject is polymorphic and carries no foreign key to catch it.

// MinPasswordBytes is the policy floor of §3.2: twelve characters, NIST SP 800-63B's posture rather
// than 2005's — no composition rules, no forced rotation, and a long ceiling (MaxPasswordBytes).
//
// MEASURED IN BYTES, as the ceiling is, and the two agree on the unit for the reason the ceiling
// exists: what is being bounded is the input argon2 reads, not a count of user-perceived
// characters. A twelve-byte floor is a shorter passphrase in a language whose characters are
// multibyte, which is the direction to be conservative in.
const MinPasswordBytes = 12

var (
	// ErrPasswordTooShort reports an input under MinPasswordBytes. It is separate from
	// ErrPasswordEmpty — which is a bug in the caller — because this one is a person choosing badly
	// and the endpoint tells them the number.
	ErrPasswordTooShort = errors.New("password is shorter than 12 bytes")

	// ErrPasswordContainsIdentifier reports a password containing the username, the email local part
	// or the guild name, case-insensitively (§3.2). Those three are the strings an attacker guesses
	// FIRST against a specific instance, and every one of them is printed on the page the person is
	// looking at while they choose.
	ErrPasswordContainsIdentifier = errors.New("password contains a name an attacker already knows")

	// ErrUsernameEmpty reports a username that normalises to nothing: whitespace, or a string made
	// only of the characters normalisation strips. It is refused rather than stored, because
	// username_norm is what ux_user_username is unique over and an empty one would let exactly one
	// such account exist and no one log in to it.
	ErrUsernameEmpty = errors.New("username is empty after normalisation")
)

// NormaliseUsername returns the username_norm form of a username: casefolded, with apostrophes,
// backticks and hyphens stripped, and outer whitespace trimmed (canonical §8).
//
// IT IS THE ASCII HALF OF THE RULE, and saying so is the point. The full normalisation is NFKC then
// casefold then strip, and NFKC needs golang.org/x/text — an indirect dependency this package must
// not promote, and one that lands with the roster's name handling. For ASCII input the two agree
// exactly, which is what internal/authz's built-in role names rely on too
// (TestBuiltinRoles_NameNorm_IsTheNormalisedName). For non-ASCII input this is a WEAKER
// normalisation than the design specifies: two names that NFKC would fold together stay distinct,
// so a homoglyph of an officer's name can be a second account. That is a real gap and it is issue
// #305, not a silent approximation — and the direction is at least the safe one, because a
// normaliser claiming an NFKC it does not do would let a name COLLIDE that should not.
//
// IT IS DONE IN GO, not in SQL, and that is not a preference: core SQLite has no NFKC and lower() is
// ASCII-only, so a normalisation written into the schema would be a second, different answer living
// in the index that decides whether two accounts are the same person.
func NormaliseUsername(username string) string {
	out := strings.ToLower(strings.TrimSpace(username))

	for _, strip := range []string{"'", "`", "-"} {
		out = strings.ReplaceAll(out, strip, "")
	}

	return out
}

// NormaliseEmail returns the email_norm form: trimmed and casefolded.
//
// CASE ONLY, and nothing else. It does not strip dots, does not drop a `+tag`, and does not
// canonicalise a provider's aliases — every one of those is a guess about a mail server this
// product does not run, and Gmail's dot-insensitivity is a Gmail fact rather than an email fact.
// What ux_user_email is for is stopping one address being registered twice in two capitalisations,
// which is the part the RFC actually settles: the domain is case-insensitive, and every mail
// provider a guild will use treats the local part that way too.
func NormaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidatePassword applies the policy of §3.2 to a password somebody is CHOOSING.
//
// IT IS NOT THE VERIFY PATH'S CHECK. HashPassword bounds length because argon2 must not be handed a
// gigabyte; this decides whether a password is allowed to be set, which is a policy an operator can
// be told about and a person can act on. Splitting them is what keeps an existing account whose
// password predates a policy change able to log in.
//
// forbidden is the set of strings §3.2 names — the username, the email local part and the guild
// name — passed by the caller rather than read here, because this package knows nothing about a
// guild and reading one would put a database call inside a string check. Empty entries are skipped,
// which is what makes "an instance with no guild row yet" and "an account with no email" ordinary
// rather than special cases.
//
// WHAT IS NOT HERE YET is the bundled ~100k breached-password blocklist §3.2 also requires, and its
// absence is issue #306 rather than a decision. It is a data artefact plus a lookup, it belongs
// beside the endpoint that sets a password rather than in the first caller that needs a floor, and
// shipping the length and substring halves now is strictly better than shipping neither.
func ValidatePassword(plaintext string, forbidden ...string) error {
	switch {
	case plaintext == "":
		return ErrPasswordEmpty
	case len(plaintext) < MinPasswordBytes:
		return fmt.Errorf("password is %d bytes, the floor is %d: %w",
			len(plaintext), MinPasswordBytes, ErrPasswordTooShort)
	case len(plaintext) > MaxPasswordBytes:
		return fmt.Errorf("password is %d bytes, the ceiling is %d: %w",
			len(plaintext), MaxPasswordBytes, ErrPasswordTooLong)
	}

	lowered := strings.ToLower(plaintext)

	for _, banned := range forbidden {
		trimmed := strings.ToLower(strings.TrimSpace(banned))
		if trimmed == "" {
			continue
		}

		if strings.Contains(lowered, trimmed) {
			// The banned string is NOT echoed into the error. It is a name the caller already knows
			// — that is the whole reason it is banned — and an error string travels into logs that
			// are read by people who do not need the account's email address in them.
			return fmt.Errorf("password contains a name this instance publishes: %w",
				ErrPasswordContainsIdentifier)
		}
	}

	return nil
}

// EmailLocalPart returns everything before the last `@`, which is the string §3.2 forbids inside a
// password. An address with no `@` is returned whole: it is not a valid address, and the caller is
// about to store it, so treating it as a name to ban is the conservative reading.
func EmailLocalPart(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return email
	}

	return email[:at]
}

// LocalAccount is one human login: the app_user row and the local user_identity row that lets it
// authenticate with a password.
//
// THE IDs AND THE INSTANT ARRIVE FROM THE CALLER rather than being minted here, because this runs
// inside somebody else's transaction and the caller needs both ids afterwards — the user id to
// grant a role to, and the identity id to open a session against. It is the same reason
// ledger.Service hands its own generator down rather than each writer minting.
type LocalAccount struct {
	// UserID and IdentityID are the two ULIDs the rows carry.
	UserID     core.ULID
	IdentityID core.ULID

	// Username is what the person types at a login prompt; UsernameNorm is what the unique index is
	// over and what the local identity's `subject` is, so the two must be derived from one string.
	// InsertLocalAccount normalises, so a caller passes the typed form only.
	Username string

	// DisplayName is the officer-facing name. Empty means "use the username", which is what the
	// column's own DEFAULT '' would leave a screen rendering as nothing.
	DisplayName string

	// Email is optional and stays NULL when empty: an account without one is legal, because SMTP is
	// optional (§3.7) and a guild with no mail server is the shape to make ordinary.
	Email string

	// PasswordHash is the argon2id PHC string HashPassword produced. It is REQUIRED here — a local
	// identity with no password is the shape the EQdkp importer writes, and the importer writes it
	// through its own path with must_reset = 1, not through this one.
	PasswordHash string

	// State is internal/auth/appuser/kinds' vocabulary. Empty means active.
	State string

	// At is the instant both rows record as created_at and updated_at.
	At core.Micros
}

// InsertLocalAccount writes the app_user row and its local identity, inside the caller's
// transaction.
//
// TWO ROWS, ONE STATEMENT EACH, AND NO ROLE. What this produces is an account that can log in and
// do nothing: capability is a role_assignment, and internal/authz writes it in the same transaction
// (see authz.Bootstrapper.CreateAccount). The separation is ADR-0028's, and it is what keeps
// "authentication decides who, authorization decides whether" true in the code rather than only in
// the document.
//
// THE UNIQUE INDEXES ARE THE DUPLICATE CHECK. There is no pre-flight SELECT for "does this username
// exist", deliberately: ux_user_username is partial over deleted_at IS NULL and would have to be
// consulted inside this transaction anyway, and a check-then-insert is a race with a second
// operator running the same command. The caller maps the constraint failure — see
// store.IsUniqueViolation.
func InsertLocalAccount(ctx context.Context, q store.Queries, a LocalAccount) error {
	norm := NormaliseUsername(a.Username)
	if norm == "" {
		return fmt.Errorf("username %q: %w", a.Username, ErrUsernameEmpty)
	}

	state := a.State
	if state == "" {
		state = appuserkinds.StateActive
	}

	if !appuserkinds.IsState(state) {
		return fmt.Errorf("app_user state %q is not in internal/auth/appuser/kinds: %w",
			state, ErrMalformedCredential)
	}

	display := strings.TrimSpace(a.DisplayName)
	if display == "" {
		display = strings.TrimSpace(a.Username)
	}

	user := sqlitegen.InsertAppUserParams{
		ID:           a.UserID.String(),
		Username:     strings.TrimSpace(a.Username),
		UsernameNorm: norm,
		DisplayName:  display,
		State:        state,
		CreatedAt:    int64(a.At),
		UpdatedAt:    int64(a.At),
	}

	if email := strings.TrimSpace(a.Email); email != "" {
		emailNorm := NormaliseEmail(email)
		user.Email = &email
		user.EmailNorm = &emailNorm
	}

	if err := q.InsertAppUser(ctx, user); err != nil {
		return fmt.Errorf("insert user %s: %w", norm, err)
	}

	algo := identitykinds.AlgoArgon2id
	hash := a.PasswordHash
	setAt := int64(a.At)

	if err := q.InsertUserIdentity(ctx, sqlitegen.InsertUserIdentityParams{
		ID:     a.IdentityID.String(),
		UserID: a.UserID.String(),
		// The local provider's `subject` is username_norm, not the typed username: it is the value
		// the login lookup resolves, and a second spelling of the same name would be a second way to
		// be the same person (internal/auth/useridentity/kinds).
		Provider:      identitykinds.ProviderLocal,
		ProviderKey:   "",
		Subject:       norm,
		PasswordHash:  &hash,
		PasswordAlgo:  &algo,
		PasswordSetAt: &setAt,
		MustReset:     0,
		CreatedAt:     int64(a.At),
		UpdatedAt:     int64(a.At),
	}); err != nil {
		return fmt.Errorf("insert local identity for %s: %w", norm, err)
	}

	return nil
}
