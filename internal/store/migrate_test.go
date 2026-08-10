package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// ledgerTriggersInSchema reads the triggers actually attached to the ledger's two tables.
//
// Straight at sqlite_schema rather than through the catalogue under test, for the obvious reason: a
// test that asked the catalogue what to look for would agree with it whatever it said.
func ledgerTriggersInSchema(tb testing.TB, s *Store) []AppendOnlyTrigger {
	tb.Helper()

	rows, err := s.read.QueryContext(tb.Context(),
		`SELECT tbl_name, name FROM sqlite_schema
		 WHERE type = 'trigger' AND tbl_name IN ('ledger_batch', 'ledger_entry')
		 ORDER BY tbl_name, name`)
	require.NoError(tb, err, "read the ledger's triggers from sqlite_schema")

	defer func() { require.NoError(tb, rows.Close()) }()

	var got []AppendOnlyTrigger

	for rows.Next() {
		var t AppendOnlyTrigger
		require.NoError(tb, rows.Scan(&t.Table, &t.Name))
		got = append(got, t)
	}

	require.NoError(tb, rows.Err())

	return got
}

// TestRestoreForeignKeyEnforcement_AfterANoTransactionMigration is the leak and its plug, in one
// test, because the plug is only interesting if the leak is real.
//
// The migration here is the shape .claude/rules/migrations.md now prescribes for rebuilding a table
// that has children — NO TRANSACTION, so that `PRAGMA foreign_keys = off` is not silently ignored —
// with the closing `on` omitted, which is the mistake the prescription makes possible. goose runs a
// no-transaction migration's statements directly on a connection, the write pool holds exactly one,
// and a pragma is connection state: the setting outlives the migration.
//
// The first half of this test asserts that leak rather than assuming it. If it ever stops leaking —
// a driver that resets session state, a goose that takes a fresh connection per migration — the plug
// becomes unnecessary and this test says so instead of quietly guarding nothing.
func TestRestoreForeignKeyEnforcement_AfterANoTransactionMigration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Numbered above every real migration: the package template has already applied them, so a lower
	// number would be reported as applied and this test would prove nothing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "009999_leaks_the_pragma.sql"), []byte(
		"-- +goose NO TRANSACTION\n"+
			"-- +goose Up\n"+
			"PRAGMA foreign_keys = off;\n"+
			"CREATE TABLE leak_probe (id text NOT NULL PRIMARY KEY) STRICT;\n"), 0o644))

	s := NewDB(t)

	require.Equal(t, 1, foreignKeysOn(t, s), "the write pool must start with foreign keys enforced")

	migrator, err := s.Migrator(os.DirFS(dir))
	require.NoError(t, err)

	_, err = migrator.ApplyNext(t.Context())
	require.NoError(t, err, "the fixture migration must apply")

	require.Equal(t, 0, foreignKeysOn(t, s),
		"the pragma did NOT leak onto the write pool. That is good news and makes "+
			"RestoreForeignKeyEnforcement unnecessary — verify why (driver session reset? goose "+
			"taking a fresh connection?) and remove the guard deliberately rather than leaving a "+
			"check that guards nothing.")

	require.NoError(t, s.RestoreForeignKeyEnforcement(t.Context()))
	require.Equal(t, 1, foreignKeysOn(t, s),
		"foreign keys are still unenforced on the write pool after the restore; every later migration "+
			"in this boot would apply with no referential integrity")
}

// foreignKeysOn reads the write pool's enforcement setting. The write pool specifically: it is the
// one a migration runs on and the one capped at a single connection.
func foreignKeysOn(tb testing.TB, s *Store) int {
	tb.Helper()

	var on int
	require.NoError(tb, s.write.QueryRowContext(tb.Context(), "PRAGMA foreign_keys").Scan(&on))

	return on
}

// TestAppendOnlyTriggers_Catalogue_MatchesAFreshInstall is what stops the hard-coded catalogue from
// becoming a lie.
//
// AppendOnlyTriggerCheck is deliberately literal: it compares the database against a list written
// out in Go rather than against the migration set, because a check derived from the migrations
// cannot notice a migration that dropped a trigger — it would just expect less. The price of that
// choice is a list that has to be maintained, and a stale list narrows the boot check silently: a
// fifth ledger trigger added by a migration and not added here would simply never be verified, and
// nothing would say so.
//
// So this test pays that price mechanically. It applies the real migration set (that is what the
// package template is) and requires the catalogue to be EXACTLY the triggers on the ledger's two
// tables — in both directions, so a trigger removed from the migrations and left in the catalogue
// fails here too rather than making every boot fail on a database that is fine.
func TestAppendOnlyTriggers_Catalogue_MatchesAFreshInstall(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	require.Equal(t, ledgerTriggersInSchema(t, s), AppendOnlyTriggers(),
		"internal/store's append-only trigger catalogue and the migration set disagree. Adding a "+
			"trigger to the ledger means adding it to appendOnlyTriggers in the same change, or the "+
			"boot path's post-migration check quietly stops verifying it.")
}

// TestAppendOnlyTriggers_ReturnsACopy keeps a security guarantee from being one append away from
// rewriting.
func TestAppendOnlyTriggers_ReturnsACopy(t *testing.T) {
	t.Parallel()

	first := AppendOnlyTriggers()
	require.NotEmpty(t, first)

	first[0] = AppendOnlyTrigger{Table: "tampered", Name: "tampered"}

	require.NotEqual(t, first, AppendOnlyTriggers(),
		"AppendOnlyTriggers handed out the package's own slice; a caller can edit the catalogue the "+
			"boot check reads")
}

// TestMissingAppendOnlyTriggers_FreshInstall_ReportsNone is the control for the two tests below.
// Without it, a check that reported every database broken would satisfy them both.
func TestMissingAppendOnlyTriggers_FreshInstall_ReportsNone(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	missing, err := s.MissingAppendOnlyTriggers(t.Context())
	require.NoError(t, err)
	require.Empty(t, missing, "a fresh install has every append-only trigger")
}

// TestMissingAppendOnlyTriggers_DroppedTrigger_IsReported is the failure the check exists for,
// produced the way a 12-step table rebuild produces it: the trigger is gone and nothing else is
// wrong.
//
// The database here is otherwise perfect — integrity_check and foreign_key_check both pass, no row
// moved — which is the entire point. Those two are what the boot path used to run, and neither has
// an opinion about a trigger.
func TestMissingAppendOnlyTriggers_DroppedTrigger_IsReported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		drop []string
		want []string
	}{
		{
			name: "one trigger, as a forgetful rebuild of one table leaves it",
			drop: []string{"trg_ledger_entry_no_update"},
			want: []string{"trg_ledger_entry_no_update"},
		},
		{
			name: "both of a table's triggers, which is what DROP TABLE actually does",
			drop: []string{"trg_ledger_entry_no_update", "trg_ledger_entry_no_delete"},
			want: []string{"trg_ledger_entry_no_delete", "trg_ledger_entry_no_update"},
		},
		{
			name: "the parent table's pair",
			drop: []string{"trg_ledger_batch_no_update", "trg_ledger_batch_no_delete"},
			want: []string{"trg_ledger_batch_no_delete", "trg_ledger_batch_no_update"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := NewDB(t)

			for _, name := range tc.drop {
				_, err := s.write.ExecContext(t.Context(), "DROP TRIGGER "+name)
				require.NoError(t, err, "drop %s to simulate a rebuild that forgot it", name)
			}

			missing, err := s.MissingAppendOnlyTriggers(t.Context())
			require.NoError(t, err)
			require.Equal(t, tc.want, missing)

			// The two checks that used to be the whole story still say the database is fine, which is
			// why this third one had to exist.
			require.NoError(t, s.IntegrityCheck(t.Context()),
				"integrity_check must still pass — a missing trigger is not corruption, and that is "+
					"exactly what makes it invisible")
			require.NoError(t, s.ForeignKeyCheck(t.Context()),
				"foreign_key_check must still pass — no reference dangles when a trigger is dropped")
		})
	}
}

// TestMissingAppendOnlyTriggers_TableAbsent_IsNotAFailure covers the fresh-install path the boot loop
// walks every time.
//
// The check runs after EVERY migration, including the ones that land before the ledger exists. If a
// trigger whose table has not been created yet counted as missing, the first migration of every
// fresh install would fail the check, restore a snapshot of an empty database and exit 1 — turning a
// guarantee about the ledger into a product that cannot start.
func TestMissingAppendOnlyTriggers_TableAbsent_IsNotAFailure(t *testing.T) {
	t.Parallel()

	s := NewDB(t)

	// Drop the ledger outright, child first: the state a database is in partway through a fresh
	// install, expressed on a database that is easy to build.
	for _, stmt := range []string{"DROP TABLE ledger_entry", "DROP TABLE ledger_batch"} {
		_, err := s.write.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}

	missing, err := s.MissingAppendOnlyTriggers(t.Context())
	require.NoError(t, err)
	require.Empty(t, missing,
		"a trigger whose table does not exist yet is early, not missing — demanding it would fail "+
			"migration 000001 of every fresh install")
}
