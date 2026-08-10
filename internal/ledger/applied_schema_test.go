package ledger_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	accountkinds "github.com/prokopto-dev/dragonkillparty/internal/account/kinds"
	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The applied-schema half of the enum catalogues' drift tests — all three of them: ledger_batch's
// kind and source, audit_log's actor_kind and outcome, and account's kind and system_key.
//
// They live in THIS package rather than beside their catalogues because they need a migrated
// database, and this package already builds one in TestMain. internal/ledger/kinds,
// internal/audit/kinds and internal/account/kinds are deliberately leaves with no repository imports
// (see their package comments: `make gen`'s first step compiles them, so anything they reach must
// build before sqlc has run), and giving any of those suites a store harness would blur that line — a
// test dependency does not break the bootstrap, but the next person to add a non-test import would
// not know that.
//
// audit_log and account are here for a second reason as well as the harness: internal/ledger is
// audit_log's only writer today and owns the system accounts' ids, and this file is the only place in
// the repository that asks a real migrated database what its CHECK constraints actually say.

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

	tests := []struct {
		constraint string
		expr       string
	}{
		{constraint: "audit_log_actor_kind_enum", expr: auditkinds.ActorKindCheckExpr()},
		{constraint: "audit_log_outcome_enum", expr: auditkinds.OutcomeCheckExpr()},
	}

	for _, tt := range tests {
		want := fmt.Sprintf("CONSTRAINT %q CHECK (%s)", tt.constraint, tt.expr)

		require.Contains(t, ddl, want,
			"the migrated database's audit_log does not carry the catalogue's %s — either a migration "+
				"is missing after a catalogue change, or a later migration rebuilt the table and dropped "+
				"the constraint. Applied DDL:\n%s", tt.constraint, ddl)
	}
}

// TestAccountKinds_AppliedSchema_MatchesCatalogue is the same authoritative check for account.kind
// and account.system_key: the CHECKs a FRESH INSTALL actually ends up with, read back from
// sqlite_schema after every migration has been applied.
//
// system_key is the one that has to be read from the applied schema rather than from a migration's
// text, and for a reason the other catalogues do not share: account has NO append-only trigger, so a
// 12-step rebuild that dropped this constraint would leave a table that looks entirely normal and
// accepts a system account keyed on anything at all — including a key with no seeded row, which is a
// balance nobody can look up rather than an error anybody sees.
func TestAccountKinds_AppliedSchema_MatchesCatalogue(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	var ddl string

	require.NoError(t,
		s.QueryRowForTest(t,
			`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'account'`).Scan(&ddl),
		"read the applied account DDL")

	tests := []struct {
		constraint string
		expr       string
	}{
		{constraint: "account_kind_enum", expr: accountkinds.KindCheckExpr()},
		{constraint: "account_system_key_enum", expr: accountkinds.SystemKeyCheckExpr()},
	}

	for _, tt := range tests {
		want := fmt.Sprintf("CONSTRAINT %q CHECK (%s)", tt.constraint, tt.expr)

		require.Contains(t, ddl, want,
			"the migrated database's account does not carry the catalogue's %s — either a migration is "+
				"missing after a catalogue change, or a later migration rebuilt the table and dropped the "+
				"constraint. Applied DDL:\n%s", tt.constraint, ddl)
	}
}

// TestAccountKinds_SeededSystemAccounts_MatchTheCatalogue closes the copy no schema comparison can
// see: the SEED ROWS in db/migrations-sqlite/000003_ledger.sql.
//
// account.system_key's CHECK says which keys are LEGAL; it cannot say which ones EXIST. A fifth key
// added to the catalogue and to the CHECK, with no seeded row, passes every other test in this file
// and fails at runtime as store.ErrNotFound from strategy.Ctx.SystemAccount — on a fresh install, on
// the degenerate path of a split, which is the path that only runs on the night a raid kills
// something solo.
//
// The comparison is SET EQUALITY in both directions, so it also catches a seeded row whose key was
// removed from the catalogue: that account holds a balance that no longer has a name.
func TestAccountKinds_SeededSystemAccounts_MatchTheCatalogue(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	rows := s.QueryForTest(t,
		`SELECT system_key FROM account WHERE kind = ? ORDER BY system_key`, accountkinds.KindSystem)
	defer func() { require.NoError(t, rows.Close()) }()

	var seeded []string

	for rows.Next() {
		var key string

		require.NoError(t, rows.Scan(&key))

		seeded = append(seeded, key)
	}

	require.NoError(t, rows.Err())

	want := accountkinds.SystemKeys()
	sort.Strings(want)

	require.Equal(t, want, seeded,
		"the seeded system accounts and internal/account/kinds disagree — a key with no row cannot be "+
			"resolved by strategy.Ctx.SystemAccount, and a row with no key holds a balance nothing names")
}
