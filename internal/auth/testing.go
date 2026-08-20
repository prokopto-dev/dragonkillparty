package auth

import (
	"context"
	"net/http"
	"testing"

	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	sakinds "github.com/prokopto-dev/dragonkillparty/internal/auth/serviceaccount/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The test harness lives in the production package, on the same terms internal/store/testing.go
// argues for at length: this file imports `testing`, so `testing` and `flag` link into the binary
// and these five helpers sit on the shipped API surface, which is the trade net/http/httptest makes.
//
// THE REASON IT CANNOT LIVE IN A SIBLING PACKAGE is stronger here than there. Seeding a credential
// means writing a row whose hash was produced by THIS package's minting code — a sibling would have
// to be handed the hashing primitives, and a test that fabricates a token by hand is a test that
// proves the resolver accepts what the TEST computes rather than what the product mints. Every
// helper below goes through the same NewToken and CreateSession an endpoint will.
//
// THE THREE CONSUMERS are internal/auth's own tests, internal/api's middleware tests and
// test/integration. Before this file existed each of them would have had to know the shape of an
// app_user row, and the day a NOT NULL column is added all three break in the same way.

// testRootKey is the root key every test keyring derives from.
//
// A FIXED, PUBLISHED, NON-SECRET VALUE, and it is safe precisely because it is published: a test
// pepper that were random would make a failure depend on which run you were looking at, and one that
// looked plausible would eventually be copied into a deployment. Nothing in the product reads it —
// LoadOrCreateKeyring generates from crypto/rand — and a grep for this constant finds every place a
// test key is used.
var testRootKey = []byte("dkp-test-root-key-32-bytes-long!")

// NewTestKeyring returns a keyring derived from the fixed test root key.
func NewTestKeyring(tb testing.TB) *Keyring {
	tb.Helper()

	ring, err := NewKeyring(testRootKey)
	if err != nil {
		tb.Fatalf("derive test keyring: %v", err)
	}

	return ring
}

// NewTestService wires a resolver over st with a test keyring.
func NewTestService(tb testing.TB, st *store.Store, clk clock.Clock) *Service {
	tb.Helper()

	return NewService(st, clk, NewTestKeyring(tb))
}

// SeedUser writes an ACTIVE app_user and returns its id.
//
// The email is left NULL: an account without one is legal (SMTP is optional, §3.7) and is the shape
// a test should exercise by default, because it is the shape a guild with no mail server has.
func SeedUser(tb testing.TB, st *store.Store, clk clock.Clock, username string) core.ULID {
	tb.Helper()

	id := core.NewGenerator(clk).MustNew()
	now := int64(core.FromTime(clk.Now()))

	err := st.Tx(context.Background(), func(ctx context.Context, q store.Queries) error {
		return q.InsertAppUser(ctx, sqlitegen.InsertAppUserParams{
			ID:           id.String(),
			Username:     username,
			UsernameNorm: username,
			DisplayName:  username,
			State:        appuserkinds.StateActive,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	})
	if err != nil {
		tb.Fatalf("seed user %s: %v", username, err)
	}

	return id
}

// SeedSession opens a session for a user through Service.CreateSession and returns the cookie a
// client must send, plus the session's id so a test can revoke it.
//
// THE COOKIE IS BUILT HERE AND NOT BY THE CALLER because its name is the control (§3.6): a test that
// spelled `__Host-dkp_session` itself would still pass if the product started reading a different
// cookie, which is the one thing this helper exists to make impossible.
//
// Secure is NOT set, and that is right for a test rather than a shortcut: httptest serves plain
// HTTP, Go's client drops a Secure cookie over it, and the real Set-Cookie is built by the login
// endpoint from the effective scheme (§8.3) — which this wave does not ship.
func SeedSession(tb testing.TB, svc *Service, userID core.ULID) (*http.Cookie, core.ULID) {
	tb.Helper()

	session, err := svc.CreateSession(context.Background(), CreateSessionParams{
		UserID:    userID,
		UserAgent: "dkp-test",
	})
	if err != nil {
		tb.Fatalf("open session for %s: %v", userID, err)
	}

	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    session.Secret,
		Path:     "/",
		HttpOnly: true,
	}, session.ID
}

// SeedServiceAccount writes an ACTIVE service account owned by owner and returns its id.
func SeedServiceAccount(
	tb testing.TB, st *store.Store, clk clock.Clock, owner core.ULID, name string,
) core.ULID {
	tb.Helper()

	id := core.NewGenerator(clk).MustNew()
	now := int64(core.FromTime(clk.Now()))

	err := st.Tx(context.Background(), func(ctx context.Context, q store.Queries) error {
		return q.InsertServiceAccount(ctx, sqlitegen.InsertServiceAccountParams{
			ID:          id.String(),
			Name:        name,
			NameNorm:    name,
			OwnerUserID: owner.String(),
			State:       sakinds.StateActive,
			CreatedBy:   owner.String(),
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	})
	if err != nil {
		tb.Fatalf("seed service account %s: %v", name, err)
	}

	return id
}

// SeedTokenParams is what a test wants to vary about a minted token: its scopes, and whether it is
// already dead.
//
// The dead states are here rather than in separate helpers because they are one row apart, and a
// resolver test that cannot easily produce an expired token is a resolver test that only ever
// exercises the happy path.
type SeedTokenParams struct {
	// ServiceAccount owns the token; CreatedBy is the human who minted it.
	ServiceAccount core.ULID
	CreatedBy      core.ULID

	// Scopes are space-separated, as api_token.scopes stores them.
	Scopes string

	// ExpiresAt and RevokedAt are nil for a live token.
	ExpiresAt *core.Micros
	RevokedAt *core.Micros
}

// SeedToken mints a PAT through the real minting primitive, writes its row, and returns the
// plaintext — the only moment it exists.
//
// THE ROW IS WRITTEN FROM WHAT NewToken PRODUCED, hash and prefix and pepper kid, so a test that
// resolves this token is testing the product's minting against the product's verification. A helper
// that computed the hash itself would pass forever, including on the day the two disagree.
func SeedToken(tb testing.TB, st *store.Store, keys *Keyring, clk clock.Clock, p SeedTokenParams) string {
	tb.Helper()

	minted, err := NewToken(keys)
	if err != nil {
		tb.Fatalf("mint token: %v", err)
	}

	id := core.NewGenerator(clk).MustNew()
	now := int64(core.FromTime(clk.Now()))

	params := sqlitegen.InsertAPITokenParams{
		ID:               id.String(),
		Prefix:           minted.Prefix,
		TokenHash:        minted.Hash,
		ServiceAccountID: p.ServiceAccount.String(),
		Name:             "test token",
		Scopes:           p.Scopes,
		PepperKid:        minted.PepperKID,
		RateLimitRpm:     600,
		CreatedBy:        p.CreatedBy.String(),
		CreatedAt:        now,
	}

	if p.ExpiresAt != nil {
		at := int64(*p.ExpiresAt)
		params.ExpiresAt = &at
	}

	err = st.Tx(context.Background(), func(ctx context.Context, q store.Queries) error {
		return q.InsertAPIToken(ctx, params)
	})
	if err != nil {
		tb.Fatalf("seed token %s: %v", minted.Prefix, err)
	}

	if p.RevokedAt != nil {
		revokeSeededToken(tb, st, id, *p.RevokedAt)
	}

	return minted.Plaintext
}

// revokeSeededToken stamps revoked_at through the SAME statement the revoke endpoint will call.
//
// Not a hand-written UPDATE and not a second insert with the column pre-set: the resolver's
// revoked-token branch has to be exercised against a row produced the way production produces one,
// or the test proves only that the fixture and the resolver agree with each other.
func revokeSeededToken(tb testing.TB, st *store.Store, id core.ULID, at core.Micros) {
	tb.Helper()

	err := st.Tx(context.Background(), func(ctx context.Context, q store.Queries) error {
		return q.RevokeAPIToken(ctx, sqlitegen.RevokeAPITokenParams{
			RevokedAt:    int64(at),
			RevokeReason: "revoked by a test",
			ID:           id.String(),
		})
	})
	if err != nil {
		tb.Fatalf("revoke seeded token %s: %v", id, err)
	}
}

// RevokeSession ends a seeded session, through the statement sign-out will call.
func RevokeSession(tb testing.TB, st *store.Store, id core.ULID, at core.Micros) {
	tb.Helper()

	err := st.Tx(context.Background(), func(ctx context.Context, q store.Queries) error {
		return q.RevokeSession(ctx, sqlitegen.RevokeSessionParams{
			RevokedAt: int64(at),
			ID:        id.String(),
		})
	})
	if err != nil {
		tb.Fatalf("revoke seeded session %s: %v", id, err)
	}
}
