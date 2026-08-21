package authz_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/authz"
)

// TestValidateScopes_RefusesAnythingOutsideTheCatalogue is the mint-time half of ADR-0011 that needs
// nothing from a caller: a token may carry only scopes canonical §6 publishes.
//
// `admin:*` IS THE ROW THAT MATTERS. EQdkp Plus's api_key impersonates the first superadmin, and the
// fix is not only that this product refuses to honour such a scope at the choke point — it is that
// the string never reaches a token row, where it would sit until somebody added a route that happened
// to name it.
func TestValidateScopes_RefusesAnythingOutsideTheCatalogue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		requested []string
		wantErr   bool
	}{
		{name: "no scopes at all is valid", requested: nil},
		{name: "one catalogue scope", requested: []string{"roster:read"}},
		{
			name:      "every catalogue scope at once",
			requested: allScopes(),
		},
		{name: "the superadmin scope ADR-0011 denies", requested: []string{"admin:*"}, wantErr: true},
		{name: "a plausible near-miss", requested: []string{"roster:admin"}, wantErr: true},
		{name: "a permission key used as a scope", requested: []string{"roster.read"}, wantErr: true},
		{
			name:      "one bad scope among good ones",
			requested: []string{"roster:read", "admin:tokens", "dkp:read"},
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := authz.ValidateScopes(tc.requested)
			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, authz.ErrUnknownScope)
		})
	}
}

// TestValidateScopes_NamesEveryOffender. A bot author fixing a mint call wants the whole list, for
// the reason authz.requireKeys collects every missing permission key rather than the first.
func TestValidateScopes_NamesEveryOffender(t *testing.T) {
	t.Parallel()

	err := authz.ValidateScopes([]string{"admin:*", "roster:read", "admin:backup"})
	require.ErrorIs(t, err, authz.ErrUnknownScope)
	require.Contains(t, err.Error(), "admin:*")
	require.Contains(t, err.Error(), "admin:backup")
	require.NotContains(t, err.Error(), "roster:read", "a valid scope must not be reported as unknown")
}

// allScopes is the catalogue's keys, so the "everything is valid" row cannot drift from Scopes().
func allScopes() []string {
	scopes := authz.Scopes()

	keys := make([]string, 0, len(scopes))
	for _, s := range scopes {
		keys = append(keys, s.Key)
	}

	return keys
}
