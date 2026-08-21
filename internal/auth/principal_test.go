package auth_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	assignmentkinds "github.com/prokopto-dev/dragonkillparty/internal/authz/roleassignment/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// TestPrincipal_Kinds_AgreeWithBothCatalogues ties the three vocabularies that have to be the same
// strings together, in one assertion, so none of them can drift.
//
// A Principal's Kind is written into audit_log.actor_kind when it acts, and read as
// role_assignment.subject_kind when its capability is resolved. Those are two different catalogues
// owning two different columns, and this package sits between them: if they ever disagreed, an audit
// row would name an actor no role assignment could match, and the authorization layer would find no
// grants for a principal that plainly has them.
func TestPrincipal_Kinds_AgreeWithBothCatalogues(t *testing.T) {
	t.Parallel()

	require.Equal(t, assignmentkinds.SubjectKindUser, auditkinds.ActorUser)
	require.Equal(t, assignmentkinds.SubjectKindServiceAccount, auditkinds.ActorServiceAccount)

	user := &auth.Principal{Kind: auditkinds.ActorUser}
	bot := &auth.Principal{Kind: auditkinds.ActorServiceAccount}

	require.True(t, assignmentkinds.IsSubjectKind(user.Kind))
	require.True(t, assignmentkinds.IsSubjectKind(bot.Kind))
}

// TestPrincipal_NilReceiver_IsSafe. An anonymous request carries no principal, and the middleware's
// callers reach for these predicates before they know whether there is one.
func TestPrincipal_NilReceiver_IsSafe(t *testing.T) {
	t.Parallel()

	var p *auth.Principal

	require.False(t, p.IsUser())
	require.False(t, p.IsServiceAccount())
	require.False(t, p.HasScope("raids:read"))
	require.Equal(t, "anonymous", p.LogValue().String())
}

// TestPrincipal_HasScope_IsFalseForASession is the assertion that keeps a session from looking like
// a token holding every scope.
//
// A session's capability is its ROLES, which this package does not read. Returning true here — the
// "well, a session isn't limited by scopes" reading — would make every scope check pass for every
// logged-in member, which is the all-powerful token ADR-0011 refuses, arriving through the browser.
func TestPrincipal_HasScope_IsFalseForASession(t *testing.T) {
	t.Parallel()

	session := &auth.Principal{
		Kind:       auditkinds.ActorUser,
		Credential: auth.CredentialSession,
		Scopes:     []string{"raids:read"}, // even if something put one there
	}

	require.False(t, session.HasScope("raids:read"))

	token := &auth.Principal{
		Kind:       auditkinds.ActorServiceAccount,
		Credential: auth.CredentialToken,
		Scopes:     []string{"raids:read"},
	}

	require.True(t, token.HasScope("raids:read"))
	require.False(t, token.HasScope("dkp:adjust"))
}

// TestPrincipal_LogValue_NamesOnlyLoggableFields is a CONTROL, not a formatting test
// (.claude/rules/go-idioms.md: never log token secrets, session ids are not the cookie).
//
// The struct holds no secret today. The point is that a future field which DOES — a decrypted TOTP
// seed, a stored provider access token — is invisible to every existing log line until somebody
// decides otherwise, because this method names its fields explicitly rather than reflecting over the
// struct.
func TestPrincipal_LogValue_NamesOnlyLoggableFields(t *testing.T) {
	t.Parallel()

	p := &auth.Principal{
		Kind:        auditkinds.ActorServiceAccount,
		ID:          core.ULID("01J8Z5X0000000000000000000"),
		Name:        "raidbot",
		Credential:  auth.CredentialToken,
		TokenID:     core.ULID("01J8Z5X0000000000000000001"),
		TokenPrefix: "abcd1234",
		OwnerUserID: core.ULID("01J8Z5X0000000000000000002"),
		Scopes:      []string{"raids:read"},
	}

	rendered := p.LogValue().String()

	require.Contains(t, rendered, "principal_kind")
	require.Contains(t, rendered, "abcd1234", "the PUBLIC prefix is how a leaked token is found")
	require.NotContains(t, rendered, "01J8Z5X0000000000000000001",
		"the token row id is not a log field; the prefix is")

	for _, field := range []string{"Scopes", "raids:read", "OwnerUserID"} {
		require.NotContainsf(t, rendered, field,
			"LogValue must name its fields explicitly, not spill the struct (%s appeared)", field)
	}

	require.NotContains(t, strings.ToLower(rendered), "secret")
}

// TestPrincipal_LogValue_IsUsedByASlogHandler proves the LogValuer is actually installed — a method
// with the right name and the wrong signature would silently never be called.
func TestPrincipal_LogValue_IsUsedByASlogHandler(t *testing.T) {
	t.Parallel()

	var buf strings.Builder

	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("refused", "principal", &auth.Principal{
		Kind:        auditkinds.ActorServiceAccount,
		ID:          core.ULID("01J8Z5X0000000000000000000"),
		Credential:  auth.CredentialToken,
		TokenPrefix: "abcd1234",
	})

	require.Contains(t, buf.String(), "principal.principal_kind=service_account")
	require.Contains(t, buf.String(), "principal.token_prefix=abcd1234")
}

// TestPrincipal_Context_RoundTrips, and the second half is the load-bearing one: an anonymous
// request must report ok=false rather than handing back a zero Principal whose empty ID would be
// compared against a row.
func TestPrincipal_Context_RoundTrips(t *testing.T) {
	t.Parallel()

	want := &auth.Principal{Kind: auditkinds.ActorUser, ID: core.ULID("01J8Z5X0000000000000000000")}

	got, ok := auth.FromContext(auth.NewContext(context.Background(), want))
	require.True(t, ok)
	require.Equal(t, want, got)

	got, ok = auth.FromContext(context.Background())
	require.False(t, ok, "an anonymous request has NO principal, not an empty one")
	require.Nil(t, got)

	got, ok = auth.FromContext(auth.NewContext(context.Background(), nil))
	require.False(t, ok, "a nil principal in the context is still no principal")
	require.Nil(t, got)
}
