package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	appuserkinds "github.com/prokopto-dev/dragonkillparty/internal/auth/appuser/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// accountEpoch is the instant the account fixtures' clock is frozen at.
var accountEpoch = time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)

func TestNormaliseUsername_Casing_Folds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"already normal", "courtney", "courtney"},
		{"mixed case", "CourtNey", "courtney"},
		{"outer whitespace", "  courtney \n", "courtney"},
		{"apostrophe", "O'Malley", "omalley"},
		{"backtick", "O`Malley", "omalley"},
		{"hyphen", "Smith-Jones", "smithjones"},
		{"all three", " D'Arcy-Smith` ", "darcysmith"},
		{"inner space is kept", "Raid Leader", "raid leader"},
		{"empty", "   ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, auth.NormaliseUsername(tc.input))
		})
	}
}

// TestNormaliseUsername_MatchesTheBuiltinRoleTransform pins this function against the one
// internal/authz's built-in role names were normalised by.
//
// The two are the same rule (canonical §8) applied to two columns, and until the real NFKC
// normaliser lands they are the same ASCII half. If they ever disagree, one of the two unique indexes
// — ux_user_username or ux_role_name — is deciding "same name" differently from the other, which is
// how a homoglyph gets to be a second account or a second role.
func TestNormaliseUsername_MatchesTheBuiltinRoleTransform(t *testing.T) {
	t.Parallel()

	asciiNormalise := func(name string) string {
		out := strings.ToLower(name)
		for _, strip := range []string{"'", "`", "-"} {
			out = strings.ReplaceAll(out, strip, "")
		}

		return out
	}

	for _, name := range []string{"Guest", "Raid Leader", "Read-only Bot", "O'Malley"} {
		require.Equalf(t, asciiNormalise(name), auth.NormaliseUsername(name),
			"NormaliseUsername disagrees with the transform internal/authz's role names use, on %q", name)
	}
}

func TestNormaliseEmail_Casing_FoldsAndKeepsEverythingElse(t *testing.T) {
	t.Parallel()

	// The dots and the +tag SURVIVE, and that is the assertion rather than an omission: stripping
	// them is a Gmail fact rather than an email fact, and this product does not run the mail server
	// that would get to decide.
	require.Equal(t, "first.last+dkp@example.com",
		auth.NormaliseEmail("  First.Last+DKP@Example.COM \n"))
}

func TestEmailLocalPart_Shapes_ReturnEverythingBeforeTheLastAt(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"courtney@example.com":     "courtney",
		"a@b@example.com":          "a@b",
		"nobody":                   "nobody",
		"@example.com":             "@example.com",
		"first.last+x@example.com": "first.last+x",
	}

	for input, want := range cases {
		require.Equalf(t, want, auth.EmailLocalPart(input), "local part of %q", input)
	}
}

// TestValidatePassword_Policy_MatchesSecurity32 is the length and substring halves of §3.2.
//
// The breached-password blocklist is NOT asserted here because it is not implemented (issue #306);
// this test says exactly what the policy does today, so the day the blocklist lands it gains cases
// rather than being discovered to have been claiming more than it checked.
func TestValidatePassword_Policy_MatchesSecurity32(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		password  string
		forbidden []string
		wantErr   error
	}{
		{"twelve bytes is the floor", "correct-hors", nil, nil},
		{"eleven bytes is refused", "correct-hor", nil, auth.ErrPasswordTooShort},
		{"empty", "", nil, auth.ErrPasswordEmpty},
		{"one over the ceiling", strings.Repeat("a", auth.MaxPasswordBytes+1), nil, auth.ErrPasswordTooLong},
		{"exactly the ceiling", strings.Repeat("a", auth.MaxPasswordBytes), nil, nil},
		{
			"contains the username",
			"xxcourtneyxx-really-long",
			[]string{"Courtney"},
			auth.ErrPasswordContainsIdentifier,
		},
		{
			"contains the email local part",
			"my-cmusgrave-password",
			[]string{"", "cmusgrave"},
			auth.ErrPasswordContainsIdentifier,
		},
		{
			"contains the guild name, case-insensitively",
			"AzureGuardiansForever",
			[]string{"", "", "azure guardians"},
			nil, // the guild name has a space; "azureguardians" is not a substring of it
		},
		{
			"contains the guild name verbatim",
			"AzureGuardians-Forever",
			[]string{"", "", "Azure"},
			auth.ErrPasswordContainsIdentifier,
		},
		{"empty forbidden entries are skipped", "a-fine-passphrase", []string{"", "  "}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := auth.ValidatePassword(tc.password, tc.forbidden...)

			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// TestValidatePassword_Refusal_DoesNotEchoTheForbiddenString keeps an email address out of an error
// string that travels into logs.
func TestValidatePassword_Refusal_DoesNotEchoTheForbiddenString(t *testing.T) {
	t.Parallel()

	err := auth.ValidatePassword("prefix-cmusgrave-suffix", "cmusgrave")
	require.ErrorIs(t, err, auth.ErrPasswordContainsIdentifier)
	require.NotContains(t, err.Error(), "cmusgrave",
		"the refusal echoed the banned string; it is a name the caller already knows and a log does not need")
}

// TestInsertLocalAccount_WritesBothRows is the shape of one login: an app_user and the local identity
// that lets it authenticate.
func TestInsertLocalAccount_WritesBothRows(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	clk := clock.NewFake(accountEpoch)
	ids := core.NewGenerator(clk)

	userID, identityID := ids.MustNew(), ids.MustNew()
	hash, err := auth.HashPassword(auth.Argon2Profiles()[len(auth.Argon2Profiles())-1], "a-fine-passphrase")
	require.NoError(t, err)

	err = st.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return auth.InsertLocalAccount(ctx, q, auth.LocalAccount{
			UserID:       userID,
			IdentityID:   identityID,
			Username:     "  D'Arcy-Smith ",
			Email:        " Courtney@Example.COM ",
			PasswordHash: hash,
			At:           core.FromTime(clk.Now()),
		})
	})
	require.NoError(t, err)

	row, err := st.Q().GetAppUser(t.Context(), userID.String())
	require.NoError(t, err)

	require.Equal(t, "D'Arcy-Smith", row.Username, "the typed form is stored verbatim")
	require.Equal(t, "darcysmith", row.UsernameNorm, "the normalised form is what the unique index is over")
	require.Equal(t, "D'Arcy-Smith", row.DisplayName, "an empty display name falls back to the username")
	require.NotNil(t, row.Email)
	require.Equal(t, "Courtney@Example.COM", *row.Email)
	require.NotNil(t, row.EmailNorm)
	require.Equal(t, "courtney@example.com", *row.EmailNorm)
	require.Equal(t, appuserkinds.StateActive, row.State)
	require.Nil(t, row.DeletedAt)

	// The identity is proved through the product's own resolution path rather than by reading the
	// row: a session opened against this identity is what the account is FOR, and CreateSession
	// refuses a user who may not act.
	svc := auth.NewTestService(t, st, clk)

	session, err := svc.CreateSession(t.Context(), auth.CreateSessionParams{
		UserID: userID, IdentityID: identityID,
	})
	require.NoError(t, err, "the account this wrote must be able to hold a session")
	require.NotEmpty(t, session.Secret)
}

// TestInsertLocalAccount_NoEmail_LeavesItNull is the shape a guild with no mail server has, and §3.7
// makes it ordinary rather than degraded.
func TestInsertLocalAccount_NoEmail_LeavesItNull(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	clk := clock.NewFake(accountEpoch)
	row := insertTestAccount(t, st, clk, "courtney", "")

	require.Nil(t, row.Email)
	require.Nil(t, row.EmailNorm)
}

// TestInsertLocalAccount_EmptyUsername_IsRefused stops a row whose normalised name is empty from
// reaching a unique index that would then admit exactly one of them and let nobody log in.
func TestInsertLocalAccount_EmptyUsername_IsRefused(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	clk := clock.NewFake(accountEpoch)
	ids := core.NewGenerator(clk)

	err := st.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return auth.InsertLocalAccount(ctx, q, auth.LocalAccount{
			UserID:       ids.MustNew(),
			IdentityID:   ids.MustNew(),
			Username:     " -'` ",
			PasswordHash: "hash",
			At:           core.FromTime(clk.Now()),
		})
	})

	require.ErrorIs(t, err, auth.ErrUsernameEmpty)
}

// TestInsertLocalAccount_DuplicateUsername_IsAUniqueViolation proves the duplicate check is the
// database's, which is the only version of it that is correct under two operators running the same
// command at once.
func TestInsertLocalAccount_DuplicateUsername_IsAUniqueViolation(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	clk := clock.NewFake(accountEpoch)
	ids := core.NewGenerator(clk)

	insertTestAccount(t, st, clk, "Courtney", "")

	err := st.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return auth.InsertLocalAccount(ctx, q, auth.LocalAccount{
			UserID:       ids.MustNew(),
			IdentityID:   ids.MustNew(),
			Username:     "COURTNEY",
			PasswordHash: "hash",
			At:           core.FromTime(clk.Now()),
		})
	})

	require.Error(t, err)
	require.True(t, store.IsUniqueViolation(err),
		"a duplicate username must arrive as a unique violation, got: %v", err)
}

// TestInsertLocalAccount_IdentitySubject_IsTheNormalisedUsername pins the value the local login
// lookup resolves. A second spelling of the same name would be a second way to be the same person.
func TestInsertLocalAccount_IdentitySubject_IsTheNormalisedUsername(t *testing.T) {
	t.Parallel()

	st := store.NewDB(t)
	clk := clock.NewFake(accountEpoch)
	ids := core.NewGenerator(clk)
	userID, identityID := ids.MustNew(), ids.MustNew()

	err := st.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return auth.InsertLocalAccount(ctx, q, auth.LocalAccount{
			UserID:       userID,
			IdentityID:   identityID,
			Username:     "O'Malley",
			PasswordHash: "hash",
			At:           core.FromTime(clk.Now()),
		})
	})
	require.NoError(t, err)

	// A SECOND identity with the same (provider, provider_key, subject) must collide, which is what
	// proves the subject is the normalised name rather than the typed one: ux_identity_subject is
	// unique over the triple.
	err = st.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		return auth.InsertLocalAccount(ctx, q, auth.LocalAccount{
			UserID:       ids.MustNew(),
			IdentityID:   ids.MustNew(),
			Username:     "omalley",
			PasswordHash: "hash",
			At:           core.FromTime(clk.Now()),
		})
	})
	require.True(t, store.IsUniqueViolation(err),
		"two spellings of one name must be one identity, got: %v", err)
}

// insertTestAccount writes one account through the product path and returns the stored row.
func insertTestAccount(
	tb testing.TB, st *store.Store, clk clock.Clock, username, email string,
) sqlitegen.AppUser {
	tb.Helper()

	ids := core.NewGenerator(clk)
	userID := ids.MustNew()

	err := st.Tx(context.Background(), func(ctx context.Context, q store.Queries) error {
		return auth.InsertLocalAccount(ctx, q, auth.LocalAccount{
			UserID:       userID,
			IdentityID:   ids.MustNew(),
			Username:     username,
			Email:        email,
			PasswordHash: "hash",
			At:           core.FromTime(clk.Now()),
		})
	})
	require.NoError(tb, err)

	row, err := st.Q().GetAppUser(context.Background(), userID.String())
	require.NoError(tb, err)

	return row
}
