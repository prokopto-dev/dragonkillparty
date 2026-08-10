package ledger_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The applied-schema half of the enum catalogue's drift tests.
//
// It lives in THIS package rather than beside the catalogue in internal/ledger/kinds because it
// needs a migrated database, and this package already builds one in TestMain. internal/ledger/kinds
// is deliberately a leaf with no repository imports (see its package comment: `make gen`'s first
// step compiles it, so anything it reaches must build before sqlc has run), and giving its test
// suite a store harness would blur the line this PR just drew — a test dependency does not break the
// bootstrap, but the next person to add a non-test import would not know that.

// TestLedgerKinds_AppliedSchema_MatchesCatalogue is the authoritative check on the copy that reaches
// the database: the schema a FRESH INSTALL actually ends up with, read back from sqlite_schema after
// every migration has been applied.
//
// It exists because reading the migration TEXT cannot answer this question. A later migration that
// rebuilds ledger_batch — SQLite's 12-step rebuild, which this PR's own doc comments warn a CHECK
// change triggers — and forgets to re-create the constraint leaves 000003_kinds.sql's text intact
// and the running database without the enum. Only the applied schema knows. The sibling test below
// still reads the migration text, because naming the file that drifted is a better error message,
// but this is the one that is sound on its own.
//
// store.NewDB applies the embedded migration set to a real SQLite database in t.TempDir(), which is
// what every other test in this package writes against — no fake, per .claude/rules/go-idioms.md.
func TestLedgerKinds_AppliedSchema_MatchesCatalogue(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	var ddl string

	require.NoError(t,
		s.QueryRowForTest(t,
			`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'ledger_batch'`).Scan(&ddl),
		"read the applied ledger_batch DDL")

	tests := []struct {
		constraint string
		column     string
		values     []string
	}{
		{constraint: "ledger_batch_kind_enum", column: "kind", values: kinds.BatchKinds()},
		{constraint: "ledger_batch_source_enum", column: "source", values: kinds.BatchSources()},
	}

	for _, tt := range tests {
		want := fmt.Sprintf("CONSTRAINT %q CHECK (%s)", tt.constraint, kinds.CheckExpr(tt.column, tt.values))

		require.Contains(t, ddl, want,
			"the migrated database's ledger_batch does not carry the catalogue's %s CHECK — either a "+
				"migration is missing after a catalogue change, or a later migration rebuilt the table "+
				"and dropped the constraint. Applied DDL:\n%s", tt.constraint, ddl)
	}
}
