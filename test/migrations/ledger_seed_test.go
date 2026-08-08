package migrations_test

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// TestMigrate_LedgerSeed_PoolAndSystemAccountsExist proves the ledger migration's seed shipped: after
// a fresh install the default pool and the four system accounts exist and are addressable by their
// deterministic ids (PR 9 acceptance criterion: the Conserved invariant must be verifiable from
// outside the ledger package).
//
// This is the RAW-SQL variant. It imports no domain package (docs/design/04-testing.md:541) — the ids
// and keys are restated as literals here on purpose, so a domain refactor that changed a constant
// could not silently rewrite what this migration test asserts the migration produced. The
// internal/ledger variant (account_test.go) proves the same seed through the domain helpers; the two
// must agree, and if they ever diverge one of the two literals is wrong.
func TestMigrate_LedgerSeed_PoolAndSystemAccountsExist(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	runner, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()))

	handle := openRaw(t, dbPath)

	// The one default pool, at its deterministic id.
	const defaultPoolID = "00000000000000000000DKPP00"

	var poolID string
	require.NoError(t,
		handle.QueryRowContext(t.Context(),
			`SELECT id FROM pool WHERE id = ?`, defaultPoolID).Scan(&poolID),
		"the default pool must be seeded at %s", defaultPoolID)
	require.Equal(t, defaultPoolID, poolID)

	// Exactly one pool ships in PR 9.
	var poolCount int
	require.NoError(t,
		handle.QueryRowContext(t.Context(), `SELECT count(*) FROM pool`).Scan(&poolCount))
	require.Equal(t, 1, poolCount, "PR 9 seeds exactly one default pool")

	// The four system accounts, keyed by system_key, each at its deterministic id. The pairing is
	// restated as a literal map so this test is self-contained and imports nothing from the product.
	wantAccounts := map[string]string{
		"residue":        "0000000000000000DKPACCTRES",
		"guild_bank":     "0000000000000000DKPACCTBNK",
		"write_off":      "0000000000000000DKPACCTWRF",
		"import_opening": "0000000000000000DKPACCTMPN",
	}

	for key, wantID := range wantAccounts {
		var (
			id, kind string
			personID *string
		)
		require.NoError(t,
			handle.QueryRowContext(t.Context(),
				`SELECT id, kind, person_id FROM account WHERE system_key = ?`, key).
				Scan(&id, &kind, &personID),
			"system account %q must be seeded", key)
		require.Equal(t, wantID, id, "system account %q must be at its deterministic id", key)
		require.Equal(t, "system", kind, "a seeded system account has kind='system'")
		require.Nil(t, personID, "a system account has no person_id")
	}

	// Exactly four system accounts, and no person accounts (PR 9 seeds no people).
	rows, err := handle.QueryContext(t.Context(),
		`SELECT system_key FROM account WHERE kind = 'system' ORDER BY system_key`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var keys []string
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		keys = append(keys, k)
	}
	require.NoError(t, rows.Err())

	wantKeys := []string{"guild_bank", "import_opening", "residue", "write_off"}
	sort.Strings(wantKeys)
	require.Equal(t, wantKeys, keys, "exactly the four known system accounts must be seeded")
}

// TestMigrate_LedgerSeed_TriggersInstalled proves the four append-only triggers are present in
// sqlite_schema after a fresh install. The behavioural test that they FIRE lives in
// internal/ledger/trigger_test.go; this one is the schema-level backstop that they EXIST, so a
// migration that dropped a trigger fails here as well as in the fingerprint.
func TestMigrate_LedgerSeed_TriggersInstalled(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	runner, err := migrate.New(migrationDir(t), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()))

	handle := openRaw(t, dbPath)

	rows, err := handle.QueryContext(t.Context(),
		`SELECT name FROM sqlite_schema WHERE type = 'trigger' ORDER BY name`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var got []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		got = append(got, n)
	}
	require.NoError(t, rows.Err())

	want := []string{
		"trg_ledger_batch_no_delete",
		"trg_ledger_batch_no_update",
		"trg_ledger_entry_no_delete",
		"trg_ledger_entry_no_update",
	}
	require.Equal(t, want, got, "all four append-only triggers must be installed")
}
