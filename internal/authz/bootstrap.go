package authz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	rolekinds "github.com/prokopto-dev/dragonkillparty/internal/authz/role/kinds"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The admin.owner HOLDER and the first-run bootstrap that creates it (issue #264) — the half of this
// package doc.go has been naming as missing since Wave 0b.
//
// THERE IS NO DEFAULT PASSWORD, EVER (docs/design/03-security.md §9.2). EQdkp Plus, phpBB, and most
// of what a guild officer has installed before ship one and tell you to change it; the ones that are
// still on the internet with `admin/admin` are the reason this file exists. The alternative is two
// doors and no third:
//
//   - ARM. A boot that finds no owner generates 32 bytes from crypto/rand, prints them once, writes
//     them to <data-dir>/first-run-token.txt at 0600, and stores only their SHA-256. The operator
//     pastes the token into the setup form. Five attempts, sixty minutes, then it is dead and the
//     recovery is a command run on the host — which proves host access and is the right bar.
//   - THE CLI. `dkp admin create` needs no token at all, and that is not a hole: it needs a shell on
//     the box and the database file, which is strictly more than the token proves.
//
// WHY THIS LIVES IN internal/authz. The thing first-run produces is a role_assignment — a row this
// package owns, granting a capability this package's catalogue defines, subject to an invariant
// (§4.3's last-holder rule) this package's tests are the Go half of. The account it hangs on is
// internal/auth's, and this file writes it through auth.InsertLocalAccount rather than reimplementing
// two INSERTs: authentication decides who, authorization decides whether, and ADR-0028 is that split.
//
// WHAT IS NOT HERE, and each is a pointer rather than an omission:
//
//   - THE /setup SURFACE. The server-rendered form, the `503 setup_required` gate over every other
//     route while unbootstrapped, and the `410 Gone` afterwards are the web slice, issue #307.
//     Redeem below is the function that handler calls, landing one wave ahead of it in the shape
//     ADR-0028 commitment 3 describes — the same reason InsertRoleAssignment shipped before its
//     endpoint. It is covered by this package's tests, not by a caller.
//   - THE AUDIT ROW §9.2 item 6 requires. The audit hash chain is written inside
//     internal/ledger.Service.Commit and there is no writer for a non-ledger action yet; issue #308
//     carries it, and the boot log names the account created in the meantime.
//   - TOTP ENROLMENT at first run, which §9.2 offers as optional and which needs the enrolment
//     endpoint.

// PermissionAdminOwner is the owner capability's key.
//
// A NAMED CONSTANT AND NOT A SECOND CATALOGUE. Catalogue() is still the one source — this is the key
// code looks a row up BY, in the three places that must not misspell it: the holder count, the
// bootstrap's role choice, and the tests that read the trigger DDL. The literal in catalogue.go stays
// a whole quoted literal because internal/specgate's SPEC005 reads that file as text (doc.go).
const PermissionAdminOwner = "admin.owner"

// The first-run token's shape and lifetime, all four from §9.2.
const (
	// BootstrapTokenBytes is the entropy: 32 bytes from crypto/rand, 43 characters of unpadded
	// base64url, exactly as a session secret is. The operator retypes or pastes this, so it is
	// URL-safe and has no padding to lose at the end of a copy.
	BootstrapTokenBytes = 32

	// BootstrapTokenLen is the encoded length, and checking it is what keeps a malformed paste from
	// reaching the comparison — and, more usefully, from spending one of the five attempts.
	BootstrapTokenLen = 43

	// BootstrapTokenTTL is sixty minutes from arming, renewable by restarting the binary or by
	// `dkp admin bootstrap --new-token`. Short because the window is an operator's attention span
	// rather than a session: they started the container in order to do this.
	BootstrapTokenTTL = 60 * time.Minute

	// BootstrapMaxAttempts is five presentations in total, successful or not. After that the token is
	// dead and the operator must re-arm from the host.
	//
	// TOTAL RATHER THAN PER-WINDOW, which is stricter than a rate limit and is the right shape here:
	// there is exactly one secret, it is 256 bits, and nobody typing it correctly needs a sixth go.
	// A rate limit would leave an unauthenticated endpoint that can be hammered forever.
	BootstrapMaxAttempts = 5

	// BootstrapTokenFileName is the file the token is written to inside the data directory, at 0600.
	// docker logs shows the stdout copy; this is the one an operator can still read tomorrow.
	BootstrapTokenFileName = "first-run-token.txt"

	// bootstrapTokenFileMode is 0600 — the same mode secrets.json is held at, for the same reason.
	bootstrapTokenFileMode = 0o600
)

// Sentinel errors, in the owning package. Callers compare with errors.Is.
//
// EVERY REDEEM FAILURE IS ONE REFUSAL ON THE WIRE and a different line in the log, exactly as
// internal/auth's resolution sentinels are: the operator at the keyboard learns only that the token
// was not accepted, and the operator reading `docker logs` learns whether it was the wrong token, a
// token that expired while they went to make tea, or one somebody has already spent five guesses at.
var (
	// ErrAlreadyBootstrapped reports that an account already holds admin.owner, so there is nothing
	// for a first-run token to do. Arming anyway would mint a credential that creates a SECOND owner
	// on a live instance, printed to a log — which is a backdoor rather than a bootstrap.
	ErrAlreadyBootstrapped = errors.New("instance already has an admin.owner holder")

	// ErrNoBootstrapToken reports that no token has ever been armed on this instance.
	ErrNoBootstrapToken = errors.New("no bootstrap token has been armed")

	// ErrBootstrapTokenExpired reports a token past its sixty minutes.
	ErrBootstrapTokenExpired = errors.New("bootstrap token has expired")

	// ErrBootstrapTokenConsumed reports a token that already created an owner. It is distinct from
	// ErrAlreadyBootstrapped because the two are different stories: this one is a retry or a second
	// browser tab, and that one is an instance that was set up some other way.
	ErrBootstrapTokenConsumed = errors.New("bootstrap token has already been redeemed")

	// ErrBootstrapTokenBurned reports that all five attempts are spent. §9.2's recovery is
	// `dkp admin bootstrap --new-token` on the host.
	ErrBootstrapTokenBurned = errors.New("bootstrap token has no attempts left")

	// ErrBootstrapTokenMismatch reports a token that did not survive the constant-time compare. An
	// attempt has already been spent by the time this is returned — see Redeem.
	ErrBootstrapTokenMismatch = errors.New("bootstrap token does not match")

	// ErrUnknownRole reports a role key that is not one of the nine built-ins. It names the set,
	// because the caller is a human at a command line who has just mistyped one.
	ErrUnknownRole = errors.New("unknown built-in role")

	// ErrRoleNotForUsers reports a role that applies to service accounts only. `dkp admin create
	// --role bot_raid` is a plausible typo and the database would otherwise take the row: nothing in
	// the schema can refuse it, because the constraint spans two tables and role_assignment's subject
	// is polymorphic (db/queries/authz.sql). It is refused here and ignored by EffectivePermission,
	// which is belt and braces on the one rule that has no third enforcement point.
	ErrRoleNotForUsers = errors.New("role does not apply to user accounts")

	// ErrUsernameTaken reports the unique index refusing a duplicate username or email. It is the
	// constraint failure translated, not a pre-flight check: a check-then-insert would be a race with
	// a second operator running the same command.
	ErrUsernameTaken = errors.New("username or email is already registered")
)

// Bootstrapper creates the accounts an instance is administered through, and owns the one-time
// first-run token.
//
// It holds a store, a clock, an id generator, the argon2 profile passwords are hashed at, and the
// data directory the token file lives in — no ctx field, no mutable state
// (.claude/rules/go-idioms.md). The clock is injected because time.Now is grep-banned outside
// internal/clock and because every one of the token's three refusals is a comparison against now.
type Bootstrapper struct {
	store   *store.Store
	clock   clock.Clock
	ids     *core.Generator
	profile auth.Argon2Profile
	dataDir string
}

// NewBootstrapper wires a Bootstrapper.
//
// AN EMPTY dataDir IS LEGAL and turns off the file half only: Arm still writes the row and returns
// the secret, and nothing tries to write or delete a path. That is what a test wants, and it is also
// the honest behaviour for an instance whose data directory could not be resolved — the token is
// still on stdout, which is where `docker logs dkp` reads it from.
//
// A nil clock defaults to the system one, as auth.NewService's does; a nil store fails closed at the
// first call rather than at construction, because cmd/dkp deliberately boots with no store when the
// database cannot be opened (canonical §13 keeps /healthz green).
func NewBootstrapper(st *store.Store, clk clock.Clock, profile auth.Argon2Profile, dataDir string) *Bootstrapper {
	if clk == nil {
		clk = clock.System{}
	}

	return &Bootstrapper{store: st, clock: clk, ids: core.NewGenerator(clk), profile: profile, dataDir: dataDir}
}

// OwnerHolders counts the accounts that can still unlock this instance: active, non-deleted users
// holding admin.owner through an unconditional role assignment.
//
// IT IS A READ THROUGH Q(), not inside a transaction, because it is a read — the boot path and the
// CLI both ask it before deciding to do anything, and neither should take the single write
// connection to find out. The callers that need it to be true AT THE MOMENT THEY WRITE do not use
// it: the eight triggers of 000011_bootstrap_token_and_last_owner_guard.sql are what make the
// invariant hold under concurrency, and this is the question rather than the enforcement.
func (b *Bootstrapper) OwnerHolders(ctx context.Context) (int64, error) {
	if b == nil || b.store == nil {
		return 0, fmt.Errorf("count admin.owner holders: %w", ErrNoStore)
	}

	holders, err := b.store.Q().CountOwnerHolders(ctx, ownerHolderParams())
	if err != nil {
		return 0, fmt.Errorf("count admin.owner holders: %w", err)
	}

	return holders, nil
}

// IsBootstrapped reports whether this instance has an owner yet.
func (b *Bootstrapper) IsBootstrapped(ctx context.Context) (bool, error) {
	holders, err := b.OwnerHolders(ctx)

	return holders > 0, err
}

// ownerHolderParams is CountOwnerHolders' argument, in one place.
//
// The three values are constants from three catalogues — this package's owner key, the assignment
// subject vocabulary, and the app_user state vocabulary — and they are passed rather than written
// into the SQL so that each stays spelled exactly once in Go.
func ownerHolderParams() sqlitegen.CountOwnerHoldersParams {
	return sqlitegen.CountOwnerHoldersParams{
		PermissionKey: PermissionAdminOwner,
		SubjectKind:   assignmentkinds.SubjectKindUser,
		ActiveState:   appuserkinds.StateActive,
	}
}

// AccountParams is what creating an administered account needs.
//
// THE PASSWORD IS A FIELD AND NOT A HASH, deliberately: hashing is this package's job so that the
// argon2 profile, the policy check and the write cannot get out of step, and so that a caller cannot
// pass a hash it produced under parameters nobody agreed to.
type AccountParams struct {
	// Username is what the person types at a login prompt. It is normalised by internal/auth; the
	// unique index is over the normalised form.
	Username string

	// DisplayName is the officer-facing name. Empty means "use the username".
	DisplayName string

	// Email is optional — SMTP is optional (§3.7) and a guild with no mail server is ordinary.
	Email string

	// Password is the plaintext, checked against §3.2's policy and then hashed. It is never stored,
	// never logged, and is not read again after HashPassword returns.
	Password string

	// RoleKey is the built-in role granted, globally. Empty means owner, which is what first run
	// wants and what makes the zero value of this struct correct rather than merely valid.
	RoleKey string

	// GrantedVia is role_assignment.granted_via. Empty means `bootstrap` — the value the catalogue
	// carries for exactly this grant and the one that makes "why does this person have owner?"
	// answerable a year later. `dkp admin create` on a bootstrapped instance passes `manual`.
	GrantedVia string

	// GrantedBy is the account that authorised the grant, for attribution. Empty is legal and is what
	// first run writes: nobody granted it, the instance had no accounts.
	GrantedBy core.ULID

	// GuildName is passed to the password policy as a third forbidden substring (§3.2). Empty when
	// there is no guild row yet, which on a fresh instance is the ordinary case.
	GuildName string
}

// Account is what CreateAccount and Redeem produced.
type Account struct {
	UserID       core.ULID
	IdentityID   core.ULID
	AssignmentID core.ULID

	// RoleKey is the role actually granted, resolved from AccountParams.RoleKey's default.
	RoleKey string
}

// CreateAccount writes one administered account: the user, its local identity and its role, in ONE
// transaction.
//
// ONE TRANSACTION IS THE WHOLE POINT. An account written without its role is an account that can log
// in and do nothing, which nothing cleans up and which looks to an officer exactly like a permissions
// bug; a role written without its account is a dangling assignment, because role_assignment's
// subject is polymorphic and carries no foreign key that would catch it (db/queries/authz.sql).
//
// THE HASH IS COMPUTED OUTSIDE THE TRANSACTION, and that is not a micro-optimisation. argon2id at the
// default profile is 19 MiB and tens of milliseconds by design, store's write pool is capped at ONE
// connection, and holding it for the duration of a deliberately slow hash puts every other writer —
// on raid night, the awards — behind a password.
//
// IT RETIRES A LIVE BOOTSTRAP TOKEN in the same transaction, when the account it just created is the
// one that ends first-run. A token is a credential for creating the first owner; once one exists it
// authorises nothing, and leaving it redeemable would mean a token printed into a log file still
// created accounts on a running instance.
func (b *Bootstrapper) CreateAccount(ctx context.Context, p AccountParams) (Account, error) {
	if b == nil || b.store == nil {
		return Account{}, fmt.Errorf("create account: %w", ErrNoStore)
	}

	role, err := builtinRoleForUser(p.RoleKey)
	if err != nil {
		return Account{}, err
	}

	if err := auth.ValidatePassword(p.Password,
		p.Username, auth.EmailLocalPart(p.Email), p.GuildName); err != nil {
		return Account{}, fmt.Errorf("password for %s: %w", p.Username, err)
	}

	hash, err := auth.HashPassword(b.profile, p.Password)
	if err != nil {
		return Account{}, fmt.Errorf("hash password for %s: %w", p.Username, err)
	}

	var created Account

	err = b.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		created, err = b.writeAccount(ctx, q, p, role, hash)
		if err != nil {
			return err
		}

		return b.retireLiveToken(ctx, q)
	})
	if err != nil {
		return Account{}, err
	}

	slog.InfoContext(ctx, "created an administered account",
		"user_id", created.UserID, "username", p.Username, "role", created.RoleKey,
		"granted_via", grantedViaOrDefault(p.GrantedVia))

	return created, nil
}

// writeAccount is the row half of CreateAccount, inside the caller's transaction, with the password
// already hashed.
//
// SHARED WITH Redeem, which is why it takes a Queries rather than opening its own transaction: the
// token path has to consume the token and write the account atomically, and two code paths that
// wrote "the first owner" differently would be two definitions of what an owner is.
func (b *Bootstrapper) writeAccount(
	ctx context.Context, q store.Queries, p AccountParams, role Role, passwordHash string,
) (Account, error) {
	userID, err := b.ids.New()
	if err != nil {
		return Account{}, fmt.Errorf("mint user id: %w", err)
	}

	identityID, err := b.ids.New()
	if err != nil {
		return Account{}, fmt.Errorf("mint identity id: %w", err)
	}

	assignmentID, err := b.ids.New()
	if err != nil {
		return Account{}, fmt.Errorf("mint role assignment id: %w", err)
	}

	now := core.FromTime(b.clock.Now())

	if err := auth.InsertLocalAccount(ctx, q, auth.LocalAccount{
		UserID:       userID,
		IdentityID:   identityID,
		Username:     p.Username,
		DisplayName:  p.DisplayName,
		Email:        p.Email,
		PasswordHash: passwordHash,
		State:        appuserkinds.StateActive,
		At:           now,
	}); err != nil {
		if store.IsUniqueViolation(err) {
			return Account{}, fmt.Errorf("create %s: %w", p.Username, ErrUsernameTaken)
		}

		return Account{}, err
	}

	assignment := sqlitegen.InsertRoleAssignmentParams{
		ID:          assignmentID.String(),
		SubjectKind: assignmentkinds.SubjectKindUser,
		SubjectID:   userID.String(),
		RoleID:      role.ID,
		// GLOBAL AND UNCONDITIONAL, and every part of that is load-bearing. A scoped owner is not an
		// owner; an expiring one is an instance that locks itself out at midnight; a suspendable one
		// is not what the last-holder triggers count, because their notion of "holds admin.owner" is
		// structural and time-free (the migration says why).
		ScopeType:  assignmentkinds.ScopeGlobal,
		GrantedVia: grantedViaOrDefault(p.GrantedVia),
		CreatedAt:  int64(now),
		UpdatedAt:  int64(now),
	}

	if p.GrantedBy != "" {
		grantedBy := p.GrantedBy.String()
		assignment.GrantedBy = &grantedBy
	}

	if err := q.InsertRoleAssignment(ctx, assignment); err != nil {
		return Account{}, fmt.Errorf("grant %s to %s: %w", role.Key, p.Username, err)
	}

	return Account{
		UserID:       userID,
		IdentityID:   identityID,
		AssignmentID: assignmentID,
		RoleKey:      role.Key,
	}, nil
}

// retireLiveToken consumes an unredeemed bootstrap token once the instance has an owner.
//
// A NO-OP IN EVERY ORDINARY CASE: there is no token on a bootstrapped instance, and there is nothing
// to retire when the account just created was not an owner. It runs inside the caller's transaction
// so that "the instance has an owner" and "the token is spent" are one fact rather than two that can
// disagree after a crash.
func (b *Bootstrapper) retireLiveToken(ctx context.Context, q store.Queries) error {
	holders, err := q.CountOwnerHolders(ctx, ownerHolderParams())
	if err != nil {
		return fmt.Errorf("count admin.owner holders: %w", err)
	}

	if holders == 0 {
		return nil
	}

	row, err := q.GetLatestBootstrapToken(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return fmt.Errorf("read the latest bootstrap token: %w", err)
	}

	if row.ConsumedAt != nil {
		return nil
	}

	if err := q.ConsumeBootstrapToken(ctx, sqlitegen.ConsumeBootstrapTokenParams{
		ConsumedAt: int64(core.FromTime(b.clock.Now())),
		ID:         row.ID,
	}); err != nil {
		return fmt.Errorf("retire bootstrap token %s: %w", row.ID, err)
	}

	return nil
}

// builtinRoleForUser resolves a role key to the built-in role a user may hold.
func builtinRoleForUser(key string) (Role, error) {
	if key == "" {
		key = RoleKeyOwner
	}

	roles := BuiltinRoles()

	index := slices.IndexFunc(roles, func(r Role) bool { return r.Key == key })
	if index < 0 {
		names := make([]string, 0, len(roles))
		for _, r := range roles {
			names = append(names, r.Key)
		}

		return Role{}, fmt.Errorf("%q: %w; the built-in roles are %s",
			key, ErrUnknownRole, strings.Join(names, ", "))
	}

	role := roles[index]

	if role.AppliesTo != rolekinds.AppliesToUser && role.AppliesTo != rolekinds.AppliesToBoth {
		return Role{}, fmt.Errorf("%q applies to %s: %w", role.Key, role.AppliesTo, ErrRoleNotForUsers)
	}

	return role, nil
}

// grantedViaOrDefault defaults role_assignment.granted_via to `bootstrap`.
//
// THE DEFAULT IS bootstrap RATHER THAN THE COLUMN'S OWN manual, and the difference is the whole
// reason the value exists: internal/authz/roleassignment/kinds calls a bootstrap grant "the first
// owner the setup wizard created and the assignment nobody may be left without". A CLI invocation on
// a running instance passes `manual` explicitly.
func grantedViaOrDefault(via string) string {
	if via == "" {
		return assignmentkinds.GrantedViaBootstrap
	}

	return via
}

// ArmedToken is a freshly armed first-run credential. The Secret exists exactly once, here; the
// database holds its SHA-256 and nothing else.
type ArmedToken struct {
	// Secret is the 43-character value the operator pastes into the setup form.
	Secret string

	// ExpiresAt is sixty minutes from now.
	ExpiresAt core.Micros

	// Path is where the secret was written, or empty when the Bootstrapper has no data directory or
	// the write failed. A caller reports the path so an operator who missed the console has somewhere
	// to look; an empty one means stdout was the only copy.
	Path string

	// FileErr is why Path is empty when a data directory was configured. It is a FIELD RATHER THAN A
	// RETURNED ERROR because it must not fail the boot: a read-only or full data directory is a real
	// deployment (a container with a read-only rootfs and no volume yet), the token is already on
	// stdout where docker logs shows it, and refusing to start would leave the operator with an
	// instance they cannot set up in order to protect a convenience copy of a secret they have.
	FileErr error
}

// Arm generates a first-run token, stores its hash, and writes the plaintext to the data directory.
//
// IT REFUSES A BOOTSTRAPPED INSTANCE, and that refusal is the security property rather than a
// convenience. A token armed on an instance that already has an owner would be a credential for
// creating a SECOND owner, printed to stdout and into whatever aggregates that container's logs —
// which is a backdoor with a nice name. The way to add an owner to a running instance is
// `dkp admin create`, which requires a shell on the box.
//
// RE-ARMING WRITES A NEW ROW rather than editing the live one, so the attempt counter, the expiry
// and the record of what happened stay with the token they describe. GetLatestBootstrapToken reads
// the newest row, so every earlier one is superseded by construction.
//
// THE ROW IS COMMITTED BEFORE THE FILE IS WRITTEN. A file naming a token the database does not know
// about is worse than no file: an operator would paste it, be refused, and spend an attempt on the
// token that is actually live.
func (b *Bootstrapper) Arm(ctx context.Context) (ArmedToken, error) {
	if b == nil || b.store == nil {
		return ArmedToken{}, fmt.Errorf("arm a bootstrap token: %w", ErrNoStore)
	}

	bootstrapped, err := b.IsBootstrapped(ctx)
	if err != nil {
		return ArmedToken{}, err
	}

	if bootstrapped {
		return ArmedToken{}, fmt.Errorf("arm a bootstrap token: %w", ErrAlreadyBootstrapped)
	}

	secret, hash, err := newBootstrapSecret()
	if err != nil {
		return ArmedToken{}, err
	}

	id, err := b.ids.New()
	if err != nil {
		return ArmedToken{}, fmt.Errorf("mint bootstrap token id: %w", err)
	}

	now := core.FromTime(b.clock.Now())
	armed := ArmedToken{Secret: secret, ExpiresAt: now.Add(BootstrapTokenTTL)}

	if err := b.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		// Re-checked INSIDE the transaction, on the single write connection, because the read above
		// ran on the read pool: two `dkp admin bootstrap --new-token` invocations racing a
		// `dkp admin create` would otherwise both pass the check outside and both arm.
		holders, countErr := q.CountOwnerHolders(ctx, ownerHolderParams())
		if countErr != nil {
			return fmt.Errorf("count admin.owner holders: %w", countErr)
		}

		if holders > 0 {
			return ErrAlreadyBootstrapped
		}

		return q.InsertBootstrapToken(ctx, sqlitegen.InsertBootstrapTokenParams{
			ID:        id.String(),
			TokenHash: hash,
			ExpiresAt: int64(armed.ExpiresAt),
			CreatedAt: int64(now),
		})
	}); err != nil {
		return ArmedToken{}, fmt.Errorf("arm a bootstrap token: %w", err)
	}

	armed.Path, armed.FileErr = b.writeTokenFile(secret)

	// The token itself is NEVER logged, here or anywhere: it goes to the command's stdout, once, by
	// the caller. What is logged is that an instance is waiting to be set up and until when, which is
	// the line an operator greps for after scrolling past the banner.
	slog.InfoContext(ctx, "armed a first-run bootstrap token; this instance has no owner yet",
		"token_id", id, "expires_at", armed.ExpiresAt, "attempts_allowed", BootstrapMaxAttempts,
		"token_file", armed.Path)

	return armed, nil
}

// Redeem exchanges a presented token for the instance's first owner.
//
// THE ORDER OF THE STEPS IS THE SECURITY PROPERTY, and each one is here rather than somewhere more
// convenient for a reason:
//
//  1. THE PASSWORD POLICY IS CHECKED FIRST, before an attempt is spent. An operator who pastes the
//     right token and picks an eight-character password has made a recoverable mistake, and charging
//     them one of five attempts for it would turn a typo into a locked instance.
//  2. THE ATTEMPT IS SPENT BEFORE THE COMPARISON, in its own committed transaction. A process killed
//     between compare and increment would otherwise hand the attempt back for free, so an attacker
//     who can crash the server gets unlimited guesses. Spending first makes a crash cost an attempt,
//     which is the safe direction.
//  3. THE COMPARISON IS CONSTANT-TIME over the SHA-256s (§9.2). There is one row, so it is a compare
//     rather than a lookup: an indexed read keyed on the presented secret's hash would decide in a
//     time that depends on the value, which is the thing constant-time comparison exists to avoid.
//  4. THE PASSWORD IS HASHED OUTSIDE THE WRITE TRANSACTION, for the reason CreateAccount's is.
//  5. THE TOKEN IS CONSUMED IN THE SAME TRANSACTION AS THE ACCOUNT, and the consume is guarded on
//     consumed_at IS NULL and verified by reading the row back. Two operators redeeming the same
//     token concurrently therefore produce one owner and one refusal, not two owners.
//
// THE TOKEN FILE IS REMOVED LAST, after the commit, and a failure to remove it is logged rather than
// returned: the owner exists and the token is dead, so failing the redeem would report a disaster
// over a stale file — which `dkp doctor` is the thing that complains about (§9.2 item 7).
func (b *Bootstrapper) Redeem(ctx context.Context, presented string, p AccountParams) (Account, error) {
	if b == nil || b.store == nil {
		return Account{}, fmt.Errorf("redeem a bootstrap token: %w", ErrNoStore)
	}

	role, err := builtinRoleForUser(p.RoleKey)
	if err != nil {
		return Account{}, err
	}

	if err := auth.ValidatePassword(p.Password,
		p.Username, auth.EmailLocalPart(p.Email), p.GuildName); err != nil {
		return Account{}, fmt.Errorf("password for %s: %w", p.Username, err)
	}

	// Shape first, and it costs nothing: a paste that lost a character is not an attempt at the
	// secret, and refusing it here keeps a fat-fingered copy from spending one of five.
	presentedHash, err := hashBootstrapSecret(presented)
	if err != nil {
		return Account{}, err
	}

	row, err := b.spendAttempt(ctx)
	if err != nil {
		return Account{}, err
	}

	if subtle.ConstantTimeCompare(presentedHash, row.TokenHash) != 1 {
		remaining := max(BootstrapMaxAttempts-(row.Attempts+1), 0)

		return Account{}, fmt.Errorf("bootstrap token %s: %w; %d attempt(s) left before it dies and "+
			"`dkp admin bootstrap --new-token` is the way back",
			row.ID, ErrBootstrapTokenMismatch, remaining)
	}

	hash, err := auth.HashPassword(b.profile, p.Password)
	if err != nil {
		return Account{}, fmt.Errorf("hash password for %s: %w", p.Username, err)
	}

	var created Account

	err = b.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		now := core.FromTime(b.clock.Now())

		// Re-read on the write connection: everything above ran outside this transaction, and the
		// only state that may legitimately have changed under it is "somebody else redeemed first".
		current, readErr := q.GetLatestBootstrapToken(ctx)
		if readErr != nil {
			return bootstrapReadErr(readErr)
		}

		if err := usableToken(current, now); err != nil {
			return err
		}

		if current.ID != row.ID {
			return fmt.Errorf("bootstrap token %s was superseded by %s: %w",
				row.ID, current.ID, ErrNoBootstrapToken)
		}

		if err := q.ConsumeBootstrapToken(ctx, sqlitegen.ConsumeBootstrapTokenParams{
			ConsumedAt: int64(now),
			ID:         current.ID,
		}); err != nil {
			return fmt.Errorf("consume bootstrap token %s: %w", current.ID, err)
		}

		// The guard on the UPDATE is what makes the race safe; reading it back is what makes the
		// guard's verdict visible. Without this a lost race commits an account against a token
		// somebody else already spent.
		after, readErr := q.GetLatestBootstrapToken(ctx)
		if readErr != nil {
			return bootstrapReadErr(readErr)
		}

		if after.ConsumedAt == nil || after.ID != current.ID {
			return fmt.Errorf("bootstrap token %s: %w", current.ID, ErrBootstrapTokenConsumed)
		}

		created, err = b.writeAccount(ctx, q, p, role, hash)

		return err
	})
	if err != nil {
		return Account{}, err
	}

	b.removeTokenFile(ctx)

	slog.InfoContext(ctx, "first-run bootstrap complete; this instance now has an owner",
		"user_id", created.UserID, "username", p.Username, "role", created.RoleKey,
		"token_id", row.ID, "attempts_used", row.Attempts+1)

	return created, nil
}

// spendAttempt charges one of the five attempts and returns the row as it was BEFORE the charge.
//
// THE ROW IT RETURNS IS THE PRE-INCREMENT ONE, which is what the caller needs: TokenHash for the
// comparison, and Attempts for the "n attempts left" the refusal reports. The post-increment read is
// what proves the guarded UPDATE won, and that is the only thing it is used for — a concurrent
// redeem that took the last attempt makes this one fail here rather than at the compare.
func (b *Bootstrapper) spendAttempt(ctx context.Context) (sqlitegen.BootstrapToken, error) {
	var row sqlitegen.BootstrapToken

	err := b.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		current, err := q.GetLatestBootstrapToken(ctx)
		if err != nil {
			return bootstrapReadErr(err)
		}

		if err := usableToken(current, core.FromTime(b.clock.Now())); err != nil {
			return err
		}

		if err := q.RecordBootstrapTokenAttempt(ctx, sqlitegen.RecordBootstrapTokenAttemptParams{
			ID:          current.ID,
			MaxAttempts: BootstrapMaxAttempts,
		}); err != nil {
			return fmt.Errorf("spend a bootstrap token attempt: %w", err)
		}

		after, err := q.GetLatestBootstrapToken(ctx)
		if err != nil {
			return bootstrapReadErr(err)
		}

		if after.ID != current.ID || after.Attempts != current.Attempts+1 {
			return fmt.Errorf("bootstrap token %s: %w", current.ID, ErrBootstrapTokenBurned)
		}

		row = current

		return nil
	})
	if err != nil {
		return sqlitegen.BootstrapToken{}, err
	}

	return row, nil
}

// usableToken reports why a token cannot be redeemed, or nil.
//
// THE THREE REFUSALS ARE THREE SENTINELS and one answer on the wire, exactly as internal/auth's
// resolution failures are. An operator at the keyboard learns only that it was refused; the log line
// is what distinguishes "you went to make tea" from "somebody has been guessing".
func usableToken(row sqlitegen.BootstrapToken, now core.Micros) error {
	switch {
	case row.ConsumedAt != nil:
		return fmt.Errorf("bootstrap token %s was redeemed at %s: %w",
			row.ID, core.Micros(*row.ConsumedAt), ErrBootstrapTokenConsumed)
	case now >= core.Micros(row.ExpiresAt):
		return fmt.Errorf("bootstrap token %s expired at %s: %w",
			row.ID, core.Micros(row.ExpiresAt), ErrBootstrapTokenExpired)
	case row.Attempts >= BootstrapMaxAttempts:
		return fmt.Errorf("bootstrap token %s has spent all %d attempts: %w",
			row.ID, BootstrapMaxAttempts, ErrBootstrapTokenBurned)
	default:
		return nil
	}
}

// bootstrapReadErr separates "no token was ever armed" from "the database could not answer".
//
// Collapsing them is the mistake auth.resolveToken's comment describes one table over: an unreadable
// database would tell every operator that their token does not exist, for the duration of the outage.
func bootstrapReadErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoBootstrapToken
	}

	return fmt.Errorf("read the latest bootstrap token: %w", err)
}

// newBootstrapSecret mints a token: 32 bytes from crypto/rand, the encoded form for the operator and
// the SHA-256 of the RAW BYTES for the row.
//
// THE DECODED BYTES ARE WHAT IS HASHED, matching internal/auth's sessions and tokens, so the three
// credential classes agree about what "the secret" is rather than each picking a reading of §9.2.
//
// UNKEYED SHA-256, where a PAT's hash is HMAC under the instance pepper. The pepper defends a
// long-lived credential that gets pasted into bot configs and inherited by the next officer; this one
// lives for sixty minutes on an instance with no accounts, and keying it would tie first-run to a
// secrets file that may be being created in the same boot. Either way the input is 256 bits from
// crypto/rand, so there is nothing to brute-force.
func newBootstrapSecret() (string, []byte, error) {
	raw := make([]byte, BootstrapTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate bootstrap token: %w", err)
	}

	sum := sha256.Sum256(raw)

	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

// hashBootstrapSecret turns a presented token into the value to compare, or reports a malformed one.
//
// IT NEVER TOUCHES THE DATABASE AND NEVER SPENDS AN ATTEMPT. A token of the wrong shape is a paste
// that lost characters, not a guess at a 256-bit secret, and treating it as one would let a
// misconfigured client burn an instance's five attempts without ever having the token.
func hashBootstrapSecret(presented string) ([]byte, error) {
	presented = strings.TrimSpace(presented)

	if len(presented) != BootstrapTokenLen {
		return nil, fmt.Errorf("bootstrap token is %d characters, want %d: %w",
			len(presented), BootstrapTokenLen, ErrBootstrapTokenMismatch)
	}

	raw, err := base64.RawURLEncoding.DecodeString(presented)
	if err != nil {
		return nil, fmt.Errorf("decode bootstrap token: %w", ErrBootstrapTokenMismatch)
	}

	if len(raw) != BootstrapTokenBytes {
		return nil, fmt.Errorf("bootstrap token is %d bytes, want %d: %w",
			len(raw), BootstrapTokenBytes, ErrBootstrapTokenMismatch)
	}

	sum := sha256.Sum256(raw)

	return sum[:], nil
}

// TokenFilePath is where the first-run token is written, or empty when there is no data directory.
func (b *Bootstrapper) TokenFilePath() string {
	if b == nil || b.dataDir == "" {
		return ""
	}

	return filepath.Join(b.dataDir, BootstrapTokenFileName)
}

// writeTokenFile writes the plaintext token at 0600 and returns where it went.
//
// O_EXCL IS NOT USED and the file is truncated, deliberately: re-arming replaces the file, and an
// operator who re-armed because the first token expired must not be left reading the dead one.
//
// THE MODE IS PASSED TO OpenFile AND THEN SET AGAIN. OpenFile's permission argument is masked by the
// process umask, so a container with umask 0022 would land 0600 & ^0022 — which is still 0600, but
// only by arithmetic accident, and a umask of 0 is not. The explicit Chmod is what makes 0600 a
// statement rather than a coincidence; store.Open holds the database file the same way.
func (b *Bootstrapper) writeTokenFile(secret string) (string, error) {
	path := b.TokenFilePath()
	if path == "" {
		return "", nil
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, bootstrapTokenFileMode)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	if _, err := file.WriteString(secret + "\n"); err != nil {
		// The handle is closed on the error path too; a partial file is worse than none, so the
		// caller reports the failure and stdout stays the copy that matters.
		_ = file.Close()

		return "", fmt.Errorf("write %s: %w", path, err)
	}

	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", path, err)
	}

	if err := os.Chmod(path, bootstrapTokenFileMode); err != nil {
		return "", fmt.Errorf("set mode on %s: %w", path, err)
	}

	return path, nil
}

// removeTokenFile deletes the first-run token file after a successful redeem (§9.2 item 6).
//
// BEST EFFORT AND LOGGED, NEVER RETURNED. The owner exists and the token is spent by the time this
// runs, so a failure here cannot make the instance less safe than it already is — and failing the
// redeem over it would tell an operator their setup did not work when it did. `dkp doctor` failing on
// a leftover file (§9.2 item 7) is the thing that gets it noticed.
func (b *Bootstrapper) removeTokenFile(ctx context.Context) {
	path := b.TokenFilePath()
	if path == "" {
		return
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.WarnContext(ctx, "could not remove the first-run token file; delete it by hand",
			"path", path, "error", err)
	}
}
