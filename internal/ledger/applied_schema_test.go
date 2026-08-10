package ledger_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The applied-schema half of the enum catalogues' drift tests — both of them: ledger_batch's kind and
// source, and audit_log's actor_kind.
//
// They live in THIS package rather than beside their catalogues because they need a migrated
// database, and this package already builds one in TestMain. internal/ledger/kinds and
// internal/audit/kinds are deliberately leaves with no repository imports (see their package
// comments: `make gen`'s first step compiles them, so anything they reach must build before sqlc has
// run), and giving either suite a store harness would blur that line — a test dependency does not
// break the bootstrap, but the next person to add a non-test import would not know that.
//
// audit_log is here for a second reason as well as the harness: internal/ledger is its only writer
// today, and this file is the only place in the repository that asks a real migrated database what
// its CHECK constraints actually say.

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

// TestAuditKinds_AppliedSchema_MatchesCatalogue is the same authoritative check for
// audit_log.actor_kind: the CHECK a FRESH INSTALL actually ends up with, read back from
// sqlite_schema after every migration has been applied.
//
// It exists because reading the migration TEXT cannot answer this question — see the sibling test in
// internal/audit/kinds, which reads it anyway for the better error message. audit_log is the table
// where the difference bites hardest: it carries an append-only UPDATE trigger and DELIBERATELY no
// DELETE trigger (retention pruning needs the deletes), so a 12-step rebuild that forgets to
// re-create the constraint leaves a table that still looks right, still prunes, and quietly accepts
// any actor_kind at all.
func TestAuditKinds_AppliedSchema_MatchesCatalogue(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	var ddl string

	require.NoError(t,
		s.QueryRowForTest(t,
			`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'audit_log'`).Scan(&ddl),
		"read the applied audit_log DDL")

	want := fmt.Sprintf("CONSTRAINT %q CHECK (%s)", "audit_log_actor_kind_enum", auditkinds.CheckExpr())

	require.Contains(t, ddl, want,
		"the migrated database's audit_log does not carry the catalogue's actor_kind CHECK — either a "+
			"migration is missing after a catalogue change, or a later migration rebuilt the table and "+
			"dropped the constraint. Applied DDL:\n%s", ddl)
}
