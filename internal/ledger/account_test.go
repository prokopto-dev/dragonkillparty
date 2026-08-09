package ledger_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestSystemAccounts_SeededAndAddressableByID proves the four system accounts and the default pool
// exist after migration and are addressable at exactly the deterministic ids
// internal/ledger/account.go declares (PR 9 acceptance criterion: the Conserved invariant must be
// verifiable from outside the package). This is the domain-helper variant; test/migrations holds the
// raw-SQL variant that imports no domain package.
//
// It reads through ledger.GetAccount / ledger.GetSystemAccount on store.Q(), so it exercises the
// exact helpers a later service and the API will use, and it asserts the id, kind, system_key and the
// nil person_id together — a system account with a person_id would be a malformed seed.
func TestSystemAccounts_SeededAndAddressableByID(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	q := s.Q()

	for key, wantID := range ledger.SystemAccountIDs() {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			// Addressable by id.
			byID, err := ledger.GetAccount(t.Context(), q, wantID)
			require.NoError(t, err, "system account %q must be addressable at id %s", key, wantID)
			require.Equal(t, wantID, byID.ID)
			require.Equal(t, "system", byID.Kind)
			require.Nil(t, byID.PersonID, "a system account has no person")
			require.NotNil(t, byID.SystemKey)
			require.Equal(t, key, *byID.SystemKey)

			// Addressable by system_key, resolving to the same row.
			byKey, err := ledger.GetSystemAccount(t.Context(), q, key)
			require.NoError(t, err, "system account %q must be addressable by system_key", key)
			require.Equal(t, wantID, byKey.ID, "id-lookup and key-lookup must return the same account")
		})
	}
}

// TestDefaultPool_Seeded proves the one default pool the migration seeds exists at its deterministic
// id. The balance and seq helpers all take a pool id, so a missing default pool would surface as a
// confusing empty balance rather than a clear "no such pool".
func TestDefaultPool_Seeded(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	var id, name, strategyID string
	require.NoError(t,
		s.QueryRowForTest(t,
			`SELECT id, name, strategy_id FROM pool WHERE id = ?`,
			ledger.DefaultPoolID.String()).Scan(&id, &name, &strategyID),
		"the default pool must be seeded at %s", ledger.DefaultPoolID)

	require.Equal(t, ledger.DefaultPoolID.String(), id)
	require.NotEmpty(t, name, "the default pool must carry a name")
	require.Equal(t, "zero_sum", strategyID, "the default pool is seeded with the zero_sum strategy")
}

// TestGetAccount_Unknown_ReturnsNotFound is the negative control for the reader: an id that was never
// seeded returns store.ErrNotFound rather than a zero-value account or a raw driver error, so a
// caller can branch on errors.Is.
func TestGetAccount_Unknown_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	_, err := ledger.GetAccount(t.Context(), s.Q(), core.ULID("0000000000000000000MISSING"))
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestSystemAccountConstants_AreValidULIDs guards the single source of truth: every seeded id and the
// default pool id must be a syntactically valid ULID, because a typo in one of these literals would
// otherwise only surface as a mysteriously unaddressable account. The migration and this file share
// these constants, so validating them here validates the seed.
func TestSystemAccountConstants_AreValidULIDs(t *testing.T) {
	t.Parallel()

	ids := []core.ULID{
		ledger.DefaultPoolID,
		ledger.AccountIDResidue,
		ledger.AccountIDGuildBank,
		ledger.AccountIDWriteOff,
		ledger.AccountIDImportOpening,
	}

	seen := make(map[core.ULID]bool, len(ids))
	for _, id := range ids {
		require.True(t, id.Valid(), "%q is not a valid ULID", id)
		require.False(t, seen[id], "%q is duplicated across the seeded id constants", id)
		seen[id] = true
	}
}
