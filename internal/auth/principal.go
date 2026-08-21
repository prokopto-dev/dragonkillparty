package auth

import (
	"context"
	"log/slog"
	"slices"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// Credential is HOW a principal was proved, and its two values are the two security-scheme names the
// OpenAPI document publishes (`session` and `pat`, internal/api/security.go).
//
// THE SAME STRINGS, ON PURPOSE, and the binding is asserted rather than remembered:
// TestSecuritySchemes_MatchTheCredentialClasses in internal/api compares the constants. internal/auth
// cannot import internal/api — the dependency runs the other way — so the vocabulary lives here,
// where the resolution happens, and the published names reference it.
//
// It is NOT a schemaenum catalogue: no column holds it. The credential class reaches the database
// only as audit_log.actor_kind, which is a different vocabulary (person versus bot) owned by
// internal/audit/kinds.
type Credential string

const (
	// CredentialSession is the `__Host-dkp_session` cookie: opaque, server-side, no scopes.
	CredentialSession Credential = "session"

	// CredentialToken is `Authorization: Bearer dkp_pat_…`: opaque, scoped, owned by a service
	// account.
	CredentialToken Credential = "pat"
)

// Principal is the single identity a handler sees, whichever credential proved it
// (docs/design/03-security.md §5).
//
// ONE TYPE, NOT TWO. A `SessionPrincipal` and a `TokenPrincipal` would be two shapes for one
// concept — AGENTS.md's "one exported type per concept" — and every handler and every authorization
// check would have to branch on which it got. That branch is the divergence between the API and the
// UI, arriving one handler at a time. The fields a session has no use for (Scopes, TokenPrefix) are
// empty on a session, and the one a token has no use for (SteppedUpAt) is nil on a token, and both
// facts are questions the authorization layer asks rather than shapes it dispatches on.
//
// A PRINCIPAL IS PROOF OF IDENTITY AND NOTHING ELSE. It carries no permissions and answers no
// "may I" question: `authz.Check` reads the role assignments for Kind and ID and intersects them
// with Scopes, per request and without caching. Holding one of these means the credential was real
// and live, and nothing about what it may reach.
//
// IT IS ALWAYS A POINTER AND NEVER NIL WHEN PRESENT. An unauthenticated request has NO principal —
// FromContext returns false — rather than a zero-valued one, because a zero Principal has Kind ""
// and ID "", and every "is this the owner" comparison against an empty id is a bug waiting for a row
// whose column is also empty.
type Principal struct {
	// Kind is internal/audit/kinds' actor vocabulary: ActorUser for a human on a session,
	// ActorServiceAccount for a bot on a token. The same two strings are role_assignment's
	// subject_kind (internal/authz/roleassignment/kinds), which is what lets an assignment name
	// either — TestPrincipal_Kinds_AgreeWithBothCatalogues holds the three together.
	Kind string

	// ID is app_user.id for a user and service_account.id for a bot: the subject a role assignment
	// names, and the id that reaches audit_log.
	ID core.ULID

	// Name is the username or the service account's name. FOR LOGS AND THE UI ONLY. Nothing is ever
	// authorized by it — §3.5's whole account-takeover section is about what happens when a display
	// handle is treated as an identity.
	Name string

	// Credential is which of the two schemes proved this principal.
	Credential Credential

	// SessionID is the session row, empty for a token. It is what "revoke this device" names in the
	// session list.
	SessionID core.ULID

	// TokenID and TokenPrefix are the token row and its PUBLIC 8-character prefix, empty for a
	// session. THE PREFIX IS LOGGABLE AND THE SECRET IS NOT (.claude/rules/go-idioms.md): the prefix
	// is how a leaked token is found, named in `dkp token revoke <prefix>` and in every audit row the
	// token writes.
	TokenID     core.ULID
	TokenPrefix string

	// OwnerUserID is the human answerable for a service account, empty for a session. ADR-0011: the
	// bot survives officer turnover, and the audit trail still names a responsible person.
	OwnerUserID core.ULID

	// Scopes are the token's, in the order it was minted with, and nil for a session — a session
	// carries no scope and is bounded by its roles alone (§4.2).
	//
	// A SCOPE NARROWS, IT NEVER GRANTS. Effective capability is `role permissions ∩ scopes`, so this
	// slice can only take capability away from what the service account's roles already allow. There
	// is no `admin:*` and no all-powerful token; authz.Check performs the intersection, and this field
	// is the half of it that the credential decides. A nil slice therefore reaches nothing.
	Scopes []string

	// SteppedUpAt is session.mfa_satisfied_at: when this session last re-authenticated. nil means
	// never. The five-minute step-up window of §3.4 is measured from it, and a token has none —
	// which is exactly why the capability floor is session-only.
	SteppedUpAt *core.Micros
}

// IsUser reports whether the principal is a human on a session.
func (p *Principal) IsUser() bool { return p != nil && p.Kind == auditkinds.ActorUser }

// IsServiceAccount reports whether the principal is a bot on a token.
func (p *Principal) IsServiceAccount() bool {
	return p != nil && p.Kind == auditkinds.ActorServiceAccount
}

// HasScope reports whether the token carries scope.
//
// FALSE FOR A SESSION, ALWAYS, and that is not an oversight to "fix" by returning true. A session's
// capability is its roles, which this package does not read; a caller asking "does this principal
// have scope X" is asking a token question, and the authorization layer that wants "may this
// principal do X" asks authz.Check instead. Returning true here would make a session look like a
// token holding every scope, which is the all-powerful token ADR-0011 refuses.
func (p *Principal) HasScope(scope string) bool {
	if p == nil || p.Credential != CredentialToken {
		return false
	}

	return slices.Contains(p.Scopes, scope)
}

// LogValue implements slog.LogValuer so that logging a principal cannot log a credential.
//
// This is a CONTROL, not a convenience. The struct holds no secret today — the session id is not the
// cookie and the prefix is not the token — but a future field that does (a decrypted TOTP seed, a
// provider access token) would otherwise be printed by every existing log line the moment it is
// added. Naming the loggable fields explicitly means a new field is invisible until somebody decides
// it should not be.
func (p *Principal) LogValue() slog.Value {
	if p == nil {
		return slog.StringValue("anonymous")
	}

	attrs := []slog.Attr{
		slog.String("principal_kind", p.Kind),
		slog.String("principal_id", p.ID.String()),
		slog.String("credential", string(p.Credential)),
	}

	if p.TokenPrefix != "" {
		attrs = append(attrs, slog.String("token_prefix", p.TokenPrefix))
	}

	if p.SessionID != "" {
		attrs = append(attrs, slog.String("session_id", p.SessionID.String()))
	}

	return slog.GroupValue(attrs...)
}

// principalKey is the context key. An unexported empty struct, so nothing outside this package can
// put a Principal into a context — which is what makes "the middleware is the only way to become
// authenticated" a property of the type system rather than of a convention.
type principalKey struct{}

// NewContext returns ctx carrying p.
//
// The middleware is its only caller in the product. It is exported for tests that need a context
// with a principal without a round trip through HTTP — and being exported costs nothing, because a
// caller who can construct a Principal has already decided to bypass authentication, which they can
// only do inside this repository.
func NewContext(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal the middleware resolved, and whether there was one.
//
// THE BOOLEAN IS NOT DECORATION. An anonymous request on a public operation carries no principal,
// and a handler that ignored the second return would get a nil *Principal whose methods happen to be
// nil-safe — and would then compare its empty ID against a row. Handlers on authenticated operations
// may treat `!ok` as unreachable, because the middleware refused the request before them, but they
// must say so out loud rather than silently.
func FromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)

	return p, ok && p != nil
}
