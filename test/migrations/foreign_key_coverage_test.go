package migrations_test

import (
	"database/sql"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// schemaSource is db/schema.hcl, relative to this package.
//
// A migration test reading the AUTHORING source is a deliberate exception to the rule at the top of
// harness_test.go, and the reason is where the waiver has to live rather than what is convenient
// here. A foreign key deliberately left uncovered needs its reason next to the constraint, in the
// file a schema reviewer reads and CODEOWNERS protects (.github/CODEOWNERS:55) — not in an allowlist
// inside the test, which is the arrangement `// dkp:enum-literal` already exists to refuse
// (.claude/rules/migrations.md). Nothing else is read from it: the COVERAGE half of this gate comes
// from a real database that applied every real migration, so an index hand-appended to a migration
// counts and one that exists but is never chosen does not.
const schemaSource = "../../db/schema.hcl"

// fkWaiverMarker declares a foreign key deliberately uncovered, with its reason. Same shape and same
// rules as `// dkp:enum-literal`: it sits on the line above the block it excepts, and a bare marker
// with no reason is a failure rather than a waiver.
const fkWaiverMarker = "// dkp:fk-uncovered"

// fkWaiverReason is enough of a reason to have been thought about: three whitespace-separated words.
// The marker is a box, and harvesting what the author knew is the entire point of making them type
// in it.
var fkWaiverReason = regexp.MustCompile(`[^ \t]+[ \t]+[^ \t]+[ \t]+[^ \t]`)

var (
	// `table "role_assignment" {` — at column zero, because a table block is the only thing declared
	// there and nothing nested can be mistaken for one.
	hclTableStart = regexp.MustCompile(`^table "([^"]+)" \{`)

	// `foreign_key "role_assignment_role" {`, at any indent.
	hclForeignKeyStart = regexp.MustCompile(`^foreign_key "([^"]+)" \{`)

	// `columns = [column.pool_id, column.account_id]`. Anchored so that `ref_columns` — the PARENT
	// side, which this gate has no opinion about — cannot match it.
	hclColumnsAssign = regexp.MustCompile(`^columns\s*=\s*\[(.*)\]`)

	hclColumnRef = regexp.MustCompile(`column\.([A-Za-z_][A-Za-z0-9_]*)`)
)

// foreignKey is one constraint as the DATABASE reports it: the child table and the child columns in
// the order the constraint declares them, which is the order an index has to lead with.
type foreignKey struct {
	table    string
	columns  []string
	parent   string
	onDelete string
}

// key identifies a constraint by what it constrains rather than by its name, because
// PRAGMA foreign_key_list does not report the name. A waiver in db/schema.hcl is matched to a
// constraint the same way, so renaming one does not silently orphan the other.
func (f foreignKey) key() string { return f.table + "(" + strings.Join(f.columns, ", ") + ")" }

// fkWaiver is a `// dkp:fk-uncovered` declaration, resolved to the constraint it sits above.
type fkWaiver struct {
	table   string
	name    string
	columns []string
	reason  string
	line    int
}

func (w fkWaiver) key() string { return w.table + "(" + strings.Join(w.columns, ", ") + ")" }

// TestMigrate_FreshInstall_EveryForeignKeyIsCovered is issue #274's second half: the gate that makes
// the first half stay decided.
//
// SQLITE ENFORCES A FOREIGN KEY BY LOOKING UP THE CHILD ROWS, through the ordinary query planner.
// Delete a parent row, or update its key, and the child table is searched for the rows that
// referenced it — CASCADE to remove them, SET NULL to blank them, NO ACTION to refuse the delete. If
// nothing indexes the child columns that search is a full table scan, and it happens inside the write
// transaction, on the single write connection every other request is queued behind.
//
// #271 fixed one such foreign key. Auditing the rest found five more, and the finding under the
// finding was that today's coverage is INCIDENTAL: `ledger_batch.pool_id` rides on ix_batch_kind and
// `ledger_entry.account_id` on ix_entry_stmt, both indexes added for reads, and nothing in the
// repository ever asked whether the next foreign key would be so lucky. This test asks, of every
// constraint in the schema a fresh install produces, on every run.
//
// IT ASSERTS THE QUERY PLAN, not the presence of an index name, and that choice is the one thing to
// preserve if this test is ever rewritten. An index that exists and is not chosen buys nothing, and
// two of the misses this gate found were exactly that shape: ix_api_token_sa and
// ix_session_user_active both led with the foreign key's column and were both PARTIAL, and a partial
// index is unusable for a lookup that does not imply its predicate — which a cascade's never does. A
// name assertion would have called both covered.
//
// THE WAIVER IS A COMMENT IN db/schema.hcl, `// dkp:fk-uncovered <reason>` on the line above the
// foreign_key block, and it is checked in BOTH directions: an uncovered constraint with no waiver
// fails, and a waiver on a constraint that is now covered fails too. Without the second half waivers
// accumulate as a list nobody re-reads, which is the state this gate exists to leave behind.
//
// Its negative control is TestForeignKeyCoverage_IndexDropped_IsReportedUncovered below, and the
// waiver parser's is TestFKWaiverMarker_Malformed_IsRejected.
func TestMigrate_FreshInstall_EveryForeignKeyIsCovered(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	handle := openRaw(t, freshInstall(t))

	keys := foreignKeysOf(t, handle)
	require.NotEmpty(t, keys, "the schema declares no foreign keys at all, so this test proved nothing")

	uncovered := uncoveredForeignKeys(t, handle, keys)

	body, err := os.ReadFile(schemaSource)
	require.NoError(t, err, "read %s", schemaSource)

	waivers, err := parseFKWaivers(string(body))
	require.NoError(t, err, "%s declares a waiver that waives nothing", schemaSource)

	for _, key := range slices.Sorted(maps.Keys(uncovered)) {
		fk := uncovered[key]

		waiver, ok := waivers[key]
		require.True(t, ok,
			"%s -> %s (ON DELETE %s) has no covering index: looking a child row up by its foreign-key "+
				"columns is a full scan of %s.\n"+
				"  SQLite enforces this constraint with that lookup, inside the write transaction and on "+
				"the connection every other request waits for.\n"+
				"  Add an index whose LEADING columns are %s to db/schema.hcl and run `make migration`, "+
				"or — if the parent row can genuinely never be deleted or re-keyed — declare it with\n"+
				"    %s <why an index here would never be read>\n"+
				"  on the line above the foreign_key block. A reason is required (#274).",
			fk.key(), fk.parent, fk.onDelete, fk.table, strings.Join(fk.columns, ", "), fkWaiverMarker)

		require.Regexp(t, fkWaiverReason, waiver.reason,
			"the %s waiver on %s at %s:%d has no reason. The marker on its own is the box ticked "+
				"without the thought; say why the child lookup can never happen.",
			fkWaiverMarker, waiver.name, schemaSource, waiver.line)
	}

	for _, key := range slices.Sorted(maps.Keys(waivers)) {
		waiver := waivers[key]

		_, stillUncovered := uncovered[key]
		require.True(t, stillUncovered,
			"the %s waiver on %s at %s:%d is STALE — %s is covered now, so the exception is a sentence "+
				"that has stopped being true.\n"+
				"  Delete the marker. A waiver list nobody re-reads is how the findings in #274 "+
				"accumulated in the first place.",
			fkWaiverMarker, waiver.name, schemaSource, waiver.line, key)
	}

	t.Logf("%d foreign keys: %d covered, %d waived (%s)",
		len(keys), len(keys)-len(uncovered), len(uncovered),
		strings.Join(slices.Sorted(maps.Keys(waivers)), ", "))
}

// TestForeignKeyCoverage_IndexDropped_IsReportedUncovered is the negative control for the gate above.
//
// It drops ix_role_permission_permission — #271's index, the worked example the whole class came from
// — on a throwaway copy of a fresh install, and requires the coverage check to notice. Without this,
// "every foreign key is covered" is an assertion nobody has ever watched fail, and a detection bug
// (matching the wrong plan verb, say, or a table name by prefix) would read as a clean schema.
//
// It also pins the half that is easy to get wrong in the other direction: SQLite reports a full walk
// of a usable-looking index as "SCAN <table> USING COVERING INDEX <x>", which contains an index name
// and is not coverage at all.
func TestForeignKeyCoverage_IndexDropped_IsReportedUncovered(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	const target = "role_permission(permission_key)"

	handle := openRaw(t, freshInstall(t))

	before := uncoveredForeignKeys(t, handle, foreignKeysOf(t, handle))
	require.NotContains(t, before, target,
		"%s is already uncovered on a fresh install, so dropping its index proves nothing", target)

	_, err := handle.ExecContext(t.Context(), `DROP INDEX ix_role_permission_permission`)
	require.NoError(t, err, "drop the index under test on this throwaway database")

	after := uncoveredForeignKeys(t, handle, foreignKeysOf(t, handle))
	require.Contains(t, after, target,
		"the coverage check did not notice that %s lost its only usable index. Every other assertion "+
			"this gate makes is worth exactly as much as this one.", target)
}

// TestFKWaiverMarker_Malformed_IsRejected drives the waiver parser through the ways a marker can be
// written and mean nothing.
//
// The parser is the half of the gate that reads a text file rather than a database, so it is the half
// that can quietly resolve to an empty map and report a schema with no exceptions in it. Each case
// below is a waiver an author would believe they had written.
func TestFKWaiverMarker_Malformed_IsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		source  string
		wantErr string
	}{
		{
			name: "well formed",
			source: `table "thing" {
  // dkp:fk-uncovered the parent is append-only and can never be deleted
  foreign_key "thing_batch" {
    columns     = [column.batch_id]
    ref_columns = [table.ledger_batch.column.id]
  }
}
`,
		},
		{
			name: "attached to a column rather than a foreign key",
			source: `table "thing" {
  // dkp:fk-uncovered the parent is append-only and can never be deleted
  column "batch_id" {
    null = true
  }
}
`,
			wantErr: "not attached to a foreign_key block",
		},
		{
			name: "two markers in a row",
			source: `table "thing" {
  // dkp:fk-uncovered the parent is append-only and can never be deleted
  // dkp:fk-uncovered the parent is append-only and can never be deleted
  foreign_key "thing_batch" {
    columns = [column.batch_id]
  }
}
`,
			wantErr: "two",
		},
		{
			name: "dangling at the end of the file",
			source: `table "thing" {
  // dkp:fk-uncovered the parent is append-only and can never be deleted
`,
			wantErr: "dangling",
		},
		{
			name: "on a foreign key that declares no columns",
			source: `table "thing" {
  // dkp:fk-uncovered the parent is append-only and can never be deleted
  foreign_key "thing_batch" {
    ref_columns = [table.ledger_batch.column.id]
  }
}
`,
			wantErr: "declares no columns",
		},
		{
			name: "outside any table block",
			source: `  // dkp:fk-uncovered the parent is append-only and can never be deleted
  foreign_key "thing_batch" {
    columns = [column.batch_id]
  }
`,
			wantErr: "outside any table",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			waivers, err := parseFKWaivers(tc.source)

			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Len(t, waivers, 1, "the well-formed waiver did not resolve to a constraint")
				require.Contains(t, waivers, "thing(batch_id)")
				require.Regexp(t, fkWaiverReason, waivers["thing(batch_id)"].reason)

				return
			}

			require.Error(t, err, "this waiver waives nothing and the parser accepted it")
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestFKWaiverMarker_WithoutAReason_FailsTheGate is the reason requirement, which the parser records
// and the gate enforces. A bare marker PARSES — it is attached to a real constraint — and is refused
// where it is used, which is what lets the failure message name the constraint it fails on.
func TestFKWaiverMarker_WithoutAReason_FailsTheGate(t *testing.T) {
	t.Parallel()

	waivers, err := parseFKWaivers(`table "thing" {
  // dkp:fk-uncovered
  foreign_key "thing_batch" {
    columns = [column.batch_id]
  }
}
`)
	require.NoError(t, err)
	require.Len(t, waivers, 1)
	require.NotRegexp(t, fkWaiverReason, waivers["thing(batch_id)"].reason,
		"a bare marker must not satisfy the reason requirement: the box ticked without the thought is "+
			"exactly what the marker exists to prevent")
}

// freshInstall applies every real migration to an empty database and returns its path.
func freshInstall(tb testing.TB) string {
	tb.Helper()

	dataDir := tb.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	runner, err := migrate.New(migrationDir(tb), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v1.0.0", AutoMigrate: true,
	})
	require.NoError(tb, err)
	require.NoError(tb, runner.Migrate(tb.Context()), "the migration set must apply to an empty database")

	return dbPath
}

// uncoveredForeignKeys is the property itself: the constraints whose child lookup SQLite would answer
// with a full table scan, keyed by foreignKey.key().
func uncoveredForeignKeys(tb testing.TB, handle *sql.DB, keys []foreignKey) map[string]foreignKey {
	tb.Helper()

	out := map[string]foreignKey{}

	for _, fk := range keys {
		if fullScanOf(childLookupPlan(tb, handle, fk), fk.table) {
			out[fk.key()] = fk
		}
	}

	return out
}

// foreignKeysOf reads every foreign key in the database, child table by child table.
//
// PRAGMA foreign_key_list reports one row per COLUMN of a composite constraint, grouped by id and
// ordered by seq within it, so the columns are reassembled in the constraint's own order — the order
// an index has to lead with for SQLite to use it.
func foreignKeysOf(tb testing.TB, handle *sql.DB) []foreignKey {
	tb.Helper()

	var out []foreignKey

	for _, table := range productTables(tb, handle) {
		rows, err := handle.QueryContext(tb.Context(), fmt.Sprintf("PRAGMA foreign_key_list(%q)", table))
		require.NoError(tb, err, "read foreign keys of %s", table)

		byID := map[int]*foreignKey{}

		var order []int

		for rows.Next() {
			var (
				id, seq                           int
				parent, onUpdate, onDelete, match string
				from, to                          sql.NullString
			)

			require.NoError(tb, rows.Scan(&id, &seq, &parent, &from, &to, &onUpdate, &onDelete, &match),
				"scan a foreign_key_list row for %s", table)
			require.True(tb, from.Valid,
				"foreign_key_list reports a NULL child column for %s, which SQLite only does for a "+
					"malformed constraint", table)

			if byID[id] == nil {
				byID[id] = &foreignKey{table: table, parent: parent, onDelete: onDelete}
				order = append(order, id)
			}

			byID[id].columns = append(byID[id].columns, from.String)
		}

		require.NoError(tb, rows.Err())
		require.NoError(tb, rows.Close())

		for _, id := range order {
			out = append(out, *byID[id])
		}
	}

	return out
}

// productTables is every table this product owns: goose's bookkeeping is goose's schema, not ours.
func productTables(tb testing.TB, handle *sql.DB) []string {
	tb.Helper()

	rows, err := handle.QueryContext(tb.Context(),
		`SELECT name FROM sqlite_schema
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version'
		 ORDER BY name`)
	require.NoError(tb, err, "list tables")

	defer func() { require.NoError(tb, rows.Close()) }()

	var tables []string

	for rows.Next() {
		var name string
		require.NoError(tb, rows.Scan(&name))
		tables = append(tables, name)
	}

	require.NoError(tb, rows.Err())
	require.NotEmpty(tb, tables, "no product tables — did the migrations apply at all?")

	return tables
}

// childLookupPlan is how SQLite says it would find the child rows of one constraint.
//
// The statement is the lookup the enforcement is MADE of, not the DELETE itself: several of these
// parents are never deleted by the product at all, so a test that issued the delete would be
// asserting against a path it had to fabricate first. `= ?` with a bound parameter is what SQLite's
// own child scan hands the planner.
func childLookupPlan(tb testing.TB, handle *sql.DB, fk foreignKey) []string {
	tb.Helper()

	var (
		where []string
		args  []any
	)

	for _, column := range fk.columns {
		where = append(where, fmt.Sprintf("%q = ?", column))
		args = append(args, "")
	}

	query := fmt.Sprintf("EXPLAIN QUERY PLAN SELECT 1 FROM %q WHERE %s",
		fk.table, strings.Join(where, " AND "))

	rows, err := handle.QueryContext(tb.Context(), query, args...)
	require.NoError(tb, err, "plan the child lookup for %s", fk.key())

	defer func() { require.NoError(tb, rows.Close()) }()

	var plan []string

	for rows.Next() {
		var id, parent, notUsed int

		var detail string

		require.NoError(tb, rows.Scan(&id, &parent, &notUsed, &detail))
		plan = append(plan, detail)
	}

	require.NoError(tb, rows.Err())
	require.NotEmpty(tb, plan, "EXPLAIN QUERY PLAN returned nothing for %s; the query never ran", fk.key())

	return plan
}

// fullScanOf reports whether the plan reads the whole of table.
//
// "SCAN <table>" and "SCAN <table> USING COVERING INDEX <x>" are both full scans and both appear
// here — the second is the one that reads like coverage and is not: it walks an index end to end
// because that index cannot be SEARCHed by the column asked for. Only SEARCH is a lookup.
//
// The name is matched to a word boundary so that a table whose name prefixes another's cannot be
// mistaken for it.
func fullScanOf(plan []string, table string) bool {
	const scan = "SCAN "

	for _, detail := range plan {
		if !strings.HasPrefix(detail, scan) {
			continue
		}

		if rest := detail[len(scan):]; rest == table || strings.HasPrefix(rest, table+" ") {
			return true
		}
	}

	return false
}

// parseFKWaivers reads every `// dkp:fk-uncovered` declaration out of a db/schema.hcl, keyed the way
// foreignKey.key() keys a constraint.
//
// A marker that does not resolve to a foreign_key block is an ERROR rather than an ignored comment: a
// waiver that sits above the wrong thing waives nothing, silently, while the author who typed it
// believes otherwise. It returns errors rather than failing a testing.TB so that its own malformed
// cases can be driven through it and asserted on.
//
// A marker is tracked by its LINE rather than by its reason, so that a bare `// dkp:fk-uncovered`
// with nothing after it is still a pending waiver rather than a comment the parser walks past — the
// reason requirement is the gate's, and it can only refuse what reached it.
func parseFKWaivers(source string) (map[string]fkWaiver, error) {
	var (
		out       = map[string]fkWaiver{}
		table     string
		pending   string
		pendingAt int
		current   *fkWaiver
	)

	for i, raw := range strings.Split(source, "\n") {
		line, trimmed := i+1, strings.TrimSpace(raw)

		if m := hclTableStart.FindStringSubmatch(raw); m != nil {
			if pendingAt != 0 {
				return nil, fmt.Errorf("line %d: a %s marker is left dangling by the end of table %q",
					pendingAt, fkWaiverMarker, table)
			}

			table = m[1]
		}

		switch {
		case current != nil:
			if m := hclColumnsAssign.FindStringSubmatch(trimmed); m != nil {
				for _, ref := range hclColumnRef.FindAllStringSubmatch(m[1], -1) {
					current.columns = append(current.columns, ref[1])
				}
			}

			if trimmed == "}" {
				if len(current.columns) == 0 {
					return nil, fmt.Errorf(
						"line %d: the foreign_key %q this waiver excepts declares no columns",
						current.line, current.name)
				}

				out[current.key()] = *current
				current = nil
			}

		case strings.HasPrefix(trimmed, fkWaiverMarker):
			if pendingAt != 0 {
				return nil, fmt.Errorf("line %d: two %s markers in a row; the first waives nothing",
					line, fkWaiverMarker)
			}

			pending = strings.TrimSpace(strings.TrimPrefix(trimmed, fkWaiverMarker))
			pendingAt = line

		case pendingAt == 0:
			// Not inside a waiver. Nothing to do.

		case trimmed == "" || strings.HasPrefix(trimmed, "//"):
			// A blank line, or further commentary between the marker and the block it excepts.

		default:
			m := hclForeignKeyStart.FindStringSubmatch(trimmed)
			if m == nil {
				return nil, fmt.Errorf(
					"line %d: the %s marker at line %d is not attached to a foreign_key block; the next "+
						"declaration is %q. A waiver above the wrong block waives nothing",
					line, fkWaiverMarker, pendingAt, trimmed)
			}

			if table == "" {
				return nil, fmt.Errorf("line %d: a foreign_key outside any table block", line)
			}

			current = &fkWaiver{table: table, name: m[1], reason: pending, line: pendingAt}
			pending, pendingAt = "", 0
		}
	}

	if pendingAt != 0 {
		return nil, fmt.Errorf("line %d: a %s marker is left dangling at the end of the file",
			pendingAt, fkWaiverMarker)
	}

	if current != nil {
		return nil, fmt.Errorf("line %d: the foreign_key %q is left unclosed", current.line, current.name)
	}

	return out, nil
}
