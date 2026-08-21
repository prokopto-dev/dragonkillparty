package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestNewToken_Shape_MatchesTheSpecifiedFormat pins the wire format of a PAT against
// docs/design/03-security.md §6.1, character by character:
//
//	dkp_pat_<8-char public prefix>_<43 chars base64url of 32 random bytes>
//
// The format is PUBLIC API in the way an operationId is: a secret scanner matches the prefix, the
// `dkp token revoke <prefix>` command takes it, and every bot config in every guild holds one. A
// change here invalidates every token in circulation.
func TestNewToken_Shape_MatchesTheSpecifiedFormat(t *testing.T) {
	t.Parallel()

	minted, err := auth.NewToken(auth.NewTestKeyring(t))
	require.NoError(t, err)

	require.Len(t, minted.Plaintext, auth.TokenLen)
	require.True(t, strings.HasPrefix(minted.Plaintext, auth.TokenScheme),
		"a token must be greppable by its scheme: %q", minted.Plaintext)
	require.Len(t, minted.Prefix, auth.TokenPrefixLen)
	require.Contains(t, minted.Plaintext, auth.TokenScheme+minted.Prefix+"_",
		"the public prefix must be the part after the scheme")
	require.Len(t, minted.Hash, 32, "HMAC-SHA256 is 32 bytes")
	require.Equal(t, auth.PepperKIDv1, minted.PepperKID)

	secret := minted.Plaintext[len(auth.TokenScheme)+auth.TokenPrefixLen+1:]
	require.Len(t, secret, auth.TokenSecretLen)
}

// TestNewToken_TwoMints_ShareNothing is the entropy assertion, and it is worth its three lines: a
// mint that returned a constant would satisfy every shape test above and hand the guild one token.
func TestNewToken_TwoMints_ShareNothing(t *testing.T) {
	t.Parallel()

	keys := auth.NewTestKeyring(t)

	first, err := auth.NewToken(keys)
	require.NoError(t, err)

	second, err := auth.NewToken(keys)
	require.NoError(t, err)

	require.NotEqual(t, first.Plaintext, second.Plaintext)
	require.NotEqual(t, first.Prefix, second.Prefix)
	require.NotEqual(t, first.Hash, second.Hash)
}

// TestResolve_MalformedBearer_IsRefusedWithoutADatabase walks the shapes a parser gets wrong.
//
// THE UNDERSCORE CASE IS THE ONE THAT MATTERS. base64url's alphabet contains `_`, so a secret half
// legitimately holds underscores — and a parser that split the token on `_` would carve roughly half
// of all real tokens into the wrong pieces. This table drives a token whose SECRET contains an
// underscore through the real resolver and requires it to be refused for the right reason rather
// than mis-parsed into a prefix lookup that happens to miss.
func TestResolve_MalformedBearer_IsRefusedWithoutADatabase(t *testing.T) {
	t.Parallel()

	svc, _ := newResolver(t)

	// A well-formed token that was never minted: the right length, the right shape, and an
	// underscore inside the secret half. It must reach the lookup and come back unknown.
	wellFormed := auth.TokenScheme + "abcdefgh" + "_" +
		"aaaaaaaaaaaaaaaaaaaaaa_aaaaaaaaaaaaaaaaaaaa"
	require.Len(t, wellFormed, auth.TokenLen)

	tests := []struct {
		name   string
		header string
		want   error
	}{
		{name: "not bearer", header: "Basic abc", want: auth.ErrMalformedCredential},
		{name: "bearer with no value", header: "Bearer ", want: auth.ErrMalformedCredential},
		{name: "not a dkp token", header: "Bearer github_pat_11ABCDEFG", want: auth.ErrMalformedCredential},
		{name: "feed token in a header", header: "Bearer dkp_feed_abcdefgh_" + strings.Repeat("a", 43), want: auth.ErrMalformedCredential},
		{name: "legacy token in a header", header: "Bearer dkp_legacy_abcdefgh_" + strings.Repeat("a", 43), want: auth.ErrMalformedCredential},
		{name: "truncated", header: "Bearer " + auth.TokenScheme + "abcdefgh_short", want: auth.ErrMalformedCredential},
		{name: "secret is not base64url", header: "Bearer " + auth.TokenScheme + "abcdefgh_" + strings.Repeat("!", 43), want: auth.ErrMalformedCredential},
		{name: "underscore in the secret, never minted", header: "Bearer " + wellFormed, want: auth.ErrUnknownCredential},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/guild", nil)
			req.Header.Set("Authorization", tt.header)

			principal, err := svc.ResolveRequest(t.Context(), req)
			require.Nil(t, principal)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// TestResolve_LowercaseBearerScheme_IsAccepted holds RFC 7235's case-insensitive scheme token. A bot
// sending `authorization: bearer …` is sending a valid request, and a 401 it cannot debug from the
// outside is the worst possible way to tell it otherwise.
func TestResolve_LowercaseBearerScheme_IsAccepted(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/guild", nil)
	req.Header.Set("Authorization", "bearer "+env.token)

	principal, err := svc.ResolveRequest(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, env.serviceAccount.String(), principal.ID.String())
}

// TestResolve_TokenWithoutAPepper_FailsClosed proves a resolver with no keyring refuses bearers —
// and does so with a sentinel that names the cause, because the alternative is a bot author being
// told their token is invalid when the server simply could not read its secrets file.
func TestResolve_TokenWithoutAPepper_FailsClosed(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	svc := auth.NewService(st, clock.System{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/guild", nil)
	req.Header.Set("Authorization", "Bearer "+auth.TokenScheme+"abcdefgh_"+strings.Repeat("a", 43))

	principal, err := svc.ResolveRequest(t.Context(), req)
	require.Nil(t, principal)
	require.ErrorIs(t, err, auth.ErrNoPepper)
}
