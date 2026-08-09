package ledger_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/prokopto-dev/dragonkillparty/db"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestMain builds the store template once, from the real migrations, so every ledger test can clone a
// migrated database (with the ledger schema, the four triggers and the seeded pool + system accounts)
// through store.NewDB. It is the same ~20-line shape as internal/store/main_test.go and
// test/integration/main_test.go: store.InitTemplate over store.ApplySchema of the embedded migration
// set.
//
// No goleak here: the store package already runs goleak over the pool lifecycle, and every Store a
// ledger test opens is closed in that test's Cleanup by store.NewDB.
func TestMain(m *testing.M) {
	fsys, err := db.SQLiteMigrations()
	if err != nil {
		slog.Error("root the embedded migration set", "error", err)
		os.Exit(1)
	}

	cleanup, err := store.InitTemplate(context.Background(), store.ApplySchema(fsys))
	if err != nil {
		slog.Error("build the ledger template database", "error", err)
		os.Exit(1)
	}

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// seedBatchWithEntry inserts one ledger_batch and one ledger_entry, returning their ids.
//
// PR 9 has no commit service (that is PR 10), so a ledger test writes the rows a service will later
// write. It does so through store.ExecForTest rather than a handle of its own: SQL001/SQL002 allow raw
// .Exec only under internal/store, so the raw call lives there and this package stays free of raw SQL
// (law 2). The values are minimal but schema-valid: every CHECK is satisfied and the seq is 1.
func seedBatchWithEntry(tb testing.TB, s *store.Store, poolID, accountID string) (batchID, entryID string) {
	tb.Helper()

	batchID = "0000000000000000000BATCH01"
	entryID = "0000000000000000000ENTRY01"

	s.ExecForTest(tb,
		`INSERT INTO ledger_batch
		   (id, pool_id, seq, kind, strategy_id, strategy_version, source, actor_is_beneficiary,
		    effective_at, recorded_at, effective_day, entry_count, net_amount_cp, hash)
		 VALUES (?, ?, 1, 'award', 'zero_sum', '0.0.0', 'system', 0,
		         1704067200000000, 1704067200000000, '2024-01-01', 1, 100, X'00')`,
		batchID, poolID)

	s.ExecForTest(tb,
		`INSERT INTO ledger_entry
		   (id, batch_id, pool_id, seq, account_id, balance_kind, amount_cp)
		 VALUES (?, ?, ?, 1, ?, 'dkp', 100)`,
		entryID, batchID, poolID, accountID)

	return batchID, entryID
}
