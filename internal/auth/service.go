package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	sakinds "github.com/prokopto-dev/dragonkillparty/internal/auth/serviceaccount/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// touchInterval is how stale a credential's last-used stamp must be before the resolver writes it
// again.
//
// A WRITE PER REQUEST IS NOT AFFORDABLE AND IS NOT NEEDED. SQLite has one writer here by
// construction (internal/store), and that connection is where raid-night awards queue; a bot polling
// every second would otherwise put 3600 writes an hour in front of them for a column nobody reads at
// that resolution. One minute is far finer than the question the column answers — "was this token
// used after we revoked it, and roughly when" — and the SQL carries the same bound as a guard, so a
// burst of concurrent requests on one credential still produces at most one statement.
const touchInterval = time.Minute

// queryTokenParams are the query-string parameter names a credential is refused in.
//
// ADR-0011 and §6.3: transport is `Authorization: Bearer dkp_pat_…` only, because a token in a URL
// is a token in the access log, the proxy log, the browser history and the referrer header. The
// refusal is EXPLICIT rather than a silent 401 — fifteen years of EQdkp bots send `?atoken=`, and an
// unexplained 401 reads as "my token is wrong" when the fix is "move it to a header".
//
// The compat shim's `?atoken=` (ADR-0013) is the single documented exception and does not route
// through this package; it is listed here because this API must refuse what that surface accepts.
var queryTokenParams = []string{"atoken", "token", "api_key", "apikey", "access_token", "key"}

// Service resolves credentials into principals, and mints the one credential that needs a
// transaction to be born correctly.
//
// IT HOLDS A *store.Store AND NOT A Queries, because it needs both pools: resolution is a read on
// the read pool (Q) and the throttled touch is a write through Tx. Nothing here ever sees a *sql.DB
// or a *sql.Tx — law 2.
//
// THE CLOCK IS INJECTED, ALWAYS (gate CLOCK001). Session expiry, the step-up window and the touch
// throttle are all comparisons against now, and a package that read the wall clock could not be
// tested for any of them without sleeping.
type Service struct {
	store *store.Store
	clock clock.Clock
	keys  *Keyring
	ids   *core.Generator
}

// NewService wires the resolver.
//
// A NIL KEYRING IS LEGAL AND FAILS CLOSED FOR BEARERS ONLY. `dkp openapi` and the architectural
// tests build the handler tree without a data directory, and a constructor that refused would make
// the spec depend on a secrets file. Sessions still resolve without one (their hash is unkeyed);
// every bearer is refused with ErrNoPepper, which names the cause in the log rather than leaving a
// bot author to discover it.
func NewService(st *store.Store, clk clock.Clock, keys *Keyring) *Service {
	if clk == nil {
		clk = clock.System{}
	}

	return &Service{store: st, clock: clk, keys: keys, ids: core.NewGenerator(clk)}
}

// ResolveRequest is the one entry point: it decides which credential a request presents, verifies
// it, and returns the principal it proves.
//
// PRECEDENCE IS FIXED AND EXPLICIT (§6.3): `Authorization` wins outright, and when it is present the
// cookie is NOT READ AT ALL. "Send both, get the union" is a confusion attack — a low-privilege
// bearer alongside a high-privilege cookie — and the only way to be sure it cannot happen is for
// one branch to never look at the other's input. TestResolve_BearerAndCookie_BearerWins is the
// machine half.
//
// A missing credential is ErrNoCredential rather than an error the caller must interpret: on a
// public operation the middleware proceeds anonymously, and on every other one it answers 401.
func (s *Service) ResolveRequest(ctx context.Context, r *http.Request) (*Principal, error) {
	// Both are wiring bugs rather than inputs to validate, and both fail CLOSED with a named error
	// instead of a panic. A nil store is a Service built without one — the middleware would otherwise
	// panic on the first authenticated request, which reads as a crash rather than as a misconfigured
	// server; a nil request cannot happen through humago, which is exactly why the day it does, the
	// answer should be a refusal.
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("no store wired: %w", ErrNoStore)
	}

	if r == nil {
		return nil, fmt.Errorf("nil request: %w", ErrMalformedCredential)
	}

	if param, found := tokenInQueryString(r); found {
		return nil, fmt.Errorf("credential in ?%s=: %w", param, ErrTokenInQueryString)
	}

	if header := r.Header.Get("Authorization"); strings.TrimSpace(header) != "" {
		presented, err := bearerToken(header)
		if err != nil {
			return nil, err
		}

		return s.resolveToken(ctx, presented)
	}

	if cookie, ok := sessionCookie(r); ok {
		return s.resolveSession(ctx, cookie)
	}

	return nil, ErrNoCredential
}

// tokenInQueryString reports the first token-shaped query parameter carrying a value.
func tokenInQueryString(r *http.Request) (string, bool) {
	if r.URL == nil || r.URL.RawQuery == "" {
		return "", false
	}

	q := r.URL.Query()

	for _, name := range queryTokenParams {
		if q.Get(name) != "" {
			return name, true
		}
	}

	return "", false
}

// resolveToken verifies a bearer against api_token.
//
// THE ORDER OF THE CHECKS IS THE SECURITY PROPERTY. The constant-time compare happens BEFORE
// revocation, expiry and account state are looked at, so a caller holding the wrong secret learns
// nothing about the row they guessed the prefix of — not even that it is revoked. Every one of those
// failures is the same 401 outside and a different sentinel inside.
//
// THERE IS NO DUMMY HASH FOR A MISSING PREFIX, where §3.3 requires a dummy argon2 verify for a
// missing username, and the difference is real rather than an inconsistency. The password path is
// guarding a guessable secret against an enumeration oracle; here the "user name" is 48 bits of
// crypto/rand and the response time is dominated by an indexed lookup that runs either way. What an
// attacker could learn is that a prefix they cannot guess exists.
//
// THE FAILURE METADATA IS ATTACHED ONCE, in a deferred wrap, rather than at each of the six return
// sites. `prefix` and `at` are filled in as the function learns them, so a failure before the parse
// carries neither and one after the revocation check carries both — and no return site can forget to
// wrap, which is the failure mode a per-site constructor invites the day a seventh check is added.
func (s *Service) resolveToken(ctx context.Context, presented string) (principal *Principal, err error) {
	var (
		prefix string
		at     *core.Micros
	)

	defer func() {
		if err != nil {
			err = tokenFailure(prefix, at, err)
		}
	}()

	parsed, err := parseToken(presented)
	if err != nil {
		return nil, err
	}

	prefix = parsed.prefix

	if s.keys == nil {
		return nil, ErrNoPepper
	}

	row, err := s.store.Q().ResolveAPIToken(ctx, parsed.prefix)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("token prefix %s: %w", parsed.prefix, ErrUnknownCredential)
		}

		return nil, fmt.Errorf("resolve token %s: %w", parsed.prefix, err)
	}

	presentedHash, err := s.keys.TokenHash(row.ApiToken.PepperKid, parsed.secret)
	if err != nil {
		return nil, fmt.Errorf("hash presented token %s: %w", parsed.prefix, err)
	}

	if subtle.ConstantTimeCompare(presentedHash, row.ApiToken.TokenHash) != 1 {
		return nil, fmt.Errorf("token prefix %s: %w", parsed.prefix, ErrUnknownCredential)
	}

	now := core.FromTime(s.clock.Now())

	switch {
	case row.ApiToken.RevokedAt != nil:
		at = micros(row.ApiToken.RevokedAt)

		return nil, fmt.Errorf("token prefix %s revoked at %s: %w",
			parsed.prefix, core.Micros(*row.ApiToken.RevokedAt), ErrRevokedCredential)

	case row.ApiToken.ExpiresAt != nil && now >= core.Micros(*row.ApiToken.ExpiresAt):
		at = micros(row.ApiToken.ExpiresAt)

		return nil, fmt.Errorf("token prefix %s expired at %s: %w",
			parsed.prefix, core.Micros(*row.ApiToken.ExpiresAt), ErrExpiredCredential)

	case row.ServiceAccountState != sakinds.StateActive:
		return nil, fmt.Errorf("service account %s is %s: %w",
			row.ApiToken.ServiceAccountID, row.ServiceAccountState, ErrPrincipalNotActive)
	}

	s.touchToken(ctx, row.ApiToken, now)

	return &Principal{
		Kind:        auditkinds.ActorServiceAccount,
		ID:          core.ULID(row.ApiToken.ServiceAccountID),
		Name:        row.ServiceAccountName,
		Credential:  CredentialToken,
		TokenID:     core.ULID(row.ApiToken.ID),
		TokenPrefix: row.ApiToken.Prefix,
		OwnerUserID: core.ULID(row.OwnerUserID),
		Scopes:      strings.Fields(row.ApiToken.Scopes),
	}, nil
}

// resolveSession verifies a cookie against session.
//
// THE EPOCH COMPARISON IS WHY THIS JOINS app_user (§3.6). "Sign out everywhere" is one UPDATE on the
// user row; every session minted under an older epoch dies here, at its next request, without a
// sweep and without a row being touched. It is reported as its own sentinel because it arrives in
// bulk — a hundred of them in a minute is a password change, not an attack.
// The deferred wrap is resolveToken's, for the same reason and with less to carry: a session failure
// has no prefix and no instant to report, because every one of them is `unauthenticated` on the wire.
func (s *Service) resolveSession(ctx context.Context, cookie string) (principal *Principal, err error) {
	defer func() {
		if err != nil {
			err = sessionFailure(err)
		}
	}()

	hash, err := hashSessionSecret(cookie)
	if err != nil {
		return nil, err
	}

	row, err := s.store.Q().ResolveSession(ctx, hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("session: %w", ErrUnknownCredential)
		}

		return nil, fmt.Errorf("resolve session: %w", err)
	}

	now := core.FromTime(s.clock.Now())

	switch {
	case row.Session.RevokedAt != nil:
		return nil, fmt.Errorf("session %s revoked at %s: %w",
			row.Session.ID, core.Micros(*row.Session.RevokedAt), ErrRevokedCredential)

	case now >= core.Micros(row.Session.ExpiresAt):
		return nil, fmt.Errorf("session %s idle-expired at %s: %w",
			row.Session.ID, core.Micros(row.Session.ExpiresAt), ErrExpiredCredential)

	case now >= core.Micros(row.Session.AbsoluteExpiresAt):
		return nil, fmt.Errorf("session %s reached its absolute expiry at %s: %w",
			row.Session.ID, core.Micros(row.Session.AbsoluteExpiresAt), ErrExpiredCredential)

	case row.Session.SessionEpoch != row.UserSessionEpoch:
		return nil, fmt.Errorf("session %s carries epoch %d, user %s is at %d: %w",
			row.Session.ID, row.Session.SessionEpoch, row.Session.UserID, row.UserSessionEpoch,
			ErrStaleSessionEpoch)

	case row.UserDeleted != 0:
		return nil, fmt.Errorf("user %s is deleted: %w", row.Session.UserID, ErrPrincipalNotActive)

	case row.UserState != appuserkinds.StateActive:
		return nil, fmt.Errorf("user %s is %s: %w",
			row.Session.UserID, row.UserState, ErrPrincipalNotActive)
	}

	s.touchSession(ctx, row.Session, now)

	principal = &Principal{
		Kind:       auditkinds.ActorUser,
		ID:         core.ULID(row.Session.UserID),
		Name:       row.Username,
		Credential: CredentialSession,
		SessionID:  core.ULID(row.Session.ID),
	}

	if row.Session.MfaSatisfiedAt != nil {
		steppedUp := core.Micros(*row.Session.MfaSatisfiedAt)
		principal.SteppedUpAt = &steppedUp
	}

	return principal, nil
}

// touchSession advances last_seen_at and slides the idle expiry, at most once per touchInterval.
//
// THE ERROR IS LOGGED AND NOT RETURNED, which is the one shape of "never swallow an error"
// (.claude/rules/go-idioms.md) that applies here: the credential has already been verified, the
// request is authenticated, and failing it because a bookkeeping write lost a race would turn a busy
// database into a wave of spurious 401s. A caller can do nothing about it; an operator reading
// "could not stamp last_seen_at" can.
func (s *Service) touchSession(ctx context.Context, row sqlitegen.Session, now core.Micros) {
	cutoff := now.Add(-touchInterval)
	if core.Micros(row.LastSeenAt) > cutoff {
		return
	}

	err := s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		return q.TouchSession(ctx, sqlitegen.TouchSessionParams{
			Now:           int64(now),
			IdleExpiresAt: int64(now.Add(SessionIdleWindow)),
			ID:            row.ID,
			TouchBefore:   int64(cutoff),
		})
	})
	if err != nil {
		slog.WarnContext(ctx, "could not stamp session last_seen_at",
			"session_id", row.ID, "error", err)
	}
}

// touchToken stamps last_used_at, at most once per touchInterval, on the same terms as
// touchSession.
//
// last_used_ip is NOT written: behind the reverse proxy this project recommends every request
// arrives from 127.0.0.1, and there is no DKP_TRUSTED_PROXIES yet (issue #98), so the only address
// available is either useless or attacker-controlled.
func (s *Service) touchToken(ctx context.Context, row sqlitegen.ApiToken, now core.Micros) {
	cutoff := now.Add(-touchInterval)
	if row.LastUsedAt != nil && core.Micros(*row.LastUsedAt) > cutoff {
		return
	}

	err := s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		return q.TouchAPIToken(ctx, sqlitegen.TouchAPITokenParams{
			Now:         int64(now),
			ID:          row.ID,
			TouchBefore: int64(cutoff),
		})
	})
	if err != nil {
		slog.WarnContext(ctx, "could not stamp token last_used_at",
			"token_prefix", row.Prefix, "error", err)
	}
}

// Session is a newly created session: the cookie value, once, and what the row says about it.
type Session struct {
	// ID is the session row — what the session list shows and what "revoke this device" names. It is
	// NOT the cookie.
	ID core.ULID

	// Secret is the cookie value, and this is the only moment it exists outside a browser. The
	// database holds its SHA-256 and nothing else.
	Secret string

	ExpiresAt         core.Micros
	AbsoluteExpiresAt core.Micros
}

// CreateSessionParams is what opening a session needs from its caller.
type CreateSessionParams struct {
	// UserID is the account the session belongs to.
	UserID core.ULID

	// IdentityID is the credential that proved it — a local password, a Discord identity. Empty is
	// legal and means "not recorded": first-run bootstrap opens a session before any identity row is
	// the answer to anything.
	IdentityID core.ULID

	// UserAgent is stored for the session list. The IP is NOT taken here and the column stays empty
	// until DKP_TRUSTED_PROXIES makes a client address a fact rather than a header (issue #98).
	UserAgent string

	// SteppedUp stamps mfa_satisfied_at, starting the five-minute step-up window of §3.4. The login
	// endpoint sets it when the login itself was a full re-authentication.
	SteppedUp bool
}

// CreateSession opens a session for a user and returns the cookie value, once.
//
// IT IS THE ONLY WAY TO MINT A SESSION, and it takes a transaction to do it because of the epoch:
// the row must record the user's CURRENT app_user.session_epoch, so the read and the insert have to
// be atomic with respect to a concurrent "sign out everywhere". Either the bump precedes the read —
// and the session is born already dead, which is correct — or it follows the insert and kills it,
// which is also correct. A read outside the transaction admits the third case, where the session is
// born under a stale epoch and survives.
//
// IT REFUSES A USER WHO MAY NOT ACT. A pending, suspended, disabled or deleted account cannot be
// given a session at all, rather than being given one the resolver will refuse: the failure belongs
// at the login attempt, where somebody is watching, not on the next request.
//
// THERE IS NO Service.MintToken beside it, and the asymmetry is the reason: a token needs no
// transactional read to be well-formed, so NewToken is a pure function and the endpoint that mints
// one owns the policy (default 365-day expiry, scope subsetting, the step-up gate) that this wave
// does not ship.
func (s *Service) CreateSession(ctx context.Context, p CreateSessionParams) (Session, error) {
	minted, err := newSessionSecret()
	if err != nil {
		return Session{}, err
	}

	id, err := s.ids.New()
	if err != nil {
		return Session{}, fmt.Errorf("generate session id: %w", err)
	}

	now := core.FromTime(s.clock.Now())
	created := Session{
		ID:                id,
		Secret:            minted.Secret,
		ExpiresAt:         now.Add(SessionIdleWindow),
		AbsoluteExpiresAt: now.Add(SessionAbsoluteWindow),
	}

	err = s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		user, getErr := q.GetAppUser(ctx, p.UserID.String())
		if getErr != nil {
			if errors.Is(getErr, sql.ErrNoRows) {
				return fmt.Errorf("open session for %s: %w", p.UserID, store.ErrNotFound)
			}

			return fmt.Errorf("load user %s: %w", p.UserID, getErr)
		}

		if user.DeletedAt != nil || user.State != appuserkinds.StateActive {
			return fmt.Errorf("open session for %s (state %s): %w",
				p.UserID, user.State, ErrPrincipalNotActive)
		}

		params := sqlitegen.InsertSessionParams{
			ID:                id.String(),
			UserID:            p.UserID.String(),
			TokenHash:         minted.Hash,
			SessionEpoch:      user.SessionEpoch,
			CreatedAt:         int64(now),
			LastSeenAt:        int64(now),
			ExpiresAt:         int64(created.ExpiresAt),
			AbsoluteExpiresAt: int64(created.AbsoluteExpiresAt),
			UserAgent:         p.UserAgent,
		}

		if p.IdentityID != "" {
			identity := p.IdentityID.String()
			params.IdentityID = &identity
		}

		if p.SteppedUp {
			at := int64(now)
			params.MfaSatisfiedAt = &at
		}

		return q.InsertSession(ctx, params)
	})
	if err != nil {
		return Session{}, err
	}

	return created, nil
}
