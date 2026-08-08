package ledger_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// ledgerTables are the five tables PR 9 adds. The assertions below run over exactly these, so a
// future table that is not STRICT elsewhere is somebody else's test to add — this one is scoped to
// what this PR ships.
var ledgerTables = []string{"pool", "account", "ledger_batch", "ledger_entry", "balance_snapshot"}

// bannedTypes are the column types STRICT forbids. STRICT permits only INT, INTEGER, REAL, TEXT, BLOB
// and ANY; BIGINT, BOOLEAN, DATETIME, NUMERIC and DECIMAL fail at CREATE TABLE. REAL is added to the
// ban here (beyond STRICT's own list) because a REAL column anywhere in the ledger is a float in the
// money path — canonical §1's central invariant is that there are none.
var bannedTypes = []string{"BIGINT", "BOOLEAN", "DATETIME", "NUMERIC", "DECIMAL", "REAL"}

// TestSchema_LedgerTables_AreStrict enumerates sqlite_schema and asserts every ledger table is STRICT
// and no ledger column declares a banned type (canonical §8, PR 9 acceptance criterion 2). STRICT is
// what makes PRAGMA integrity_check verify column CONTENT types, which is the cheapest guard against a
// value like "350.00" reaching a centipoint column, and it is the property the integer-money
// invariant leans on at the storage layer.
func TestSchema_LedgerTables_AreStrict(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	for _, table := range ledgerTables {
		t.Run(table, func(t *testing.T) {
			t.Parallel()

			var ddl string
			require.NoError(t,
				s.QueryRowForTest(t,
					`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&ddl),
				"table %q must exist in sqlite_schema", table)

			upper := strings.ToUpper(ddl)

			require.Contains(t, upper, "STRICT",
				"table %q is not STRICT; STRICT is what makes integrity_check validate column content "+
					"types (canonical §8)", table)

			for _, banned := range bannedTypes {
				require.NotContains(t, upper, banned,
					"table %q declares a %s column; STRICT forbids it and a REAL/NUMERIC column in the "+
						"ledger is a float in the money path (canonical §1)", table, banned)
			}
		})
	}
}

// TestSchema_LedgerTables_HaveBlobHashColumns is the counterpart to the type ban: it asserts the two
// hash columns are BLOB, so the ban above cannot be "passed" by a schema that quietly dropped them or
// typed them as TEXT. A hash stored as hex TEXT is twice the size and invites a string comparison
// where a byte comparison belongs.
func TestSchema_LedgerTables_HaveBlobHashColumns(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	var ddl string
	require.NoError(t,
		s.QueryRowForTest(t,
			`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'ledger_batch'`).Scan(&ddl))

	lower := strings.ToLower(ddl)
	require.Contains(t, lower, `"prev_hash" blob`, "prev_hash must be a BLOB")
	require.Contains(t, lower, `"hash" blob`, "hash must be a BLOB")
}
