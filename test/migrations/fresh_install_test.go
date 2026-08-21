package migrations_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// fingerprintGolden is the committed expected value.
//
// It lives under test/golden/ rather than test/migrations/ deliberately, and the choice has a
// review consequence. docs/development/first-ten-prs.md contradicts itself on this path — its
// files-touched block says test/migrations/golden/ and its acceptance criterion says test/golden/ —
// and test/golden/ is the one that is CODEOWNERS-protected (.github/CODEOWNERS:65) and gated by
// .claude/hooks/guard-protected-paths.sh. A fingerprint exists to make a silent schema change
// loud; parking it somewhere an agent can rewrite unnoticed to turn this test green defeats the
// only thing it does.
const fingerprintGolden = "../golden/migrations/fresh_install_fingerprint.txt"

// TestMigrate_FreshInstall_MatchesFingerprint pins the schema an empty database ends up with.
//
// The fingerprint covers every row in sqlite_schema, INCLUDING TRIGGERS, and that is the load-
// bearing detail rather than a completeness flourish. Item V6 in
// docs/development/verify-before-phase-0.md is now answered: Atlas cannot express triggers at all
// in the community edition, and a 12-step table rebuild issues DROP TABLE without recreating any
// trigger attached to the old table — silently, with no warning and no diff to review. This test is
// the mechanism that notices, and PR 9's append-only ledger guarantee depends on it existing before
// the ledger triggers do.
//
// If this test fails after a schema change, the schema changed. Updating the golden is the correct
// response ONLY once you have read the new fingerprint's source — `make test-migrations` prints the
// actual — and satisfied yourself that every difference is one you meant. There is deliberately no
// -update flag: regenerating a golden must be a decision somebody typed, not a flag somebody ran.
func TestMigrate_FreshInstall_MatchesFingerprint(t *testing.T) {
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
	require.NoError(t, runner.Migrate(t.Context()), "the migration set must apply to an empty database")

	want, err := os.ReadFile(fingerprintGolden)
	require.NoError(t, err, "read the committed fingerprint at %s", fingerprintGolden)

	got := schemaFingerprint(t, openRaw(t, dbPath))

	require.Equal(t, strings.TrimSpace(string(want)), got,
		"the schema a fresh install produces has changed.\n"+
			"  If you changed db/schema.hcl and ran `make migration`, this is expected: read the new\n"+
			"  migration, confirm every difference is one you meant, and write %s\n"+
			"  containing:\n    %s\n"+
			"  If you did NOT change the schema, something changed it for you — a dependency bump that\n"+
			"  moved SQLite's DDL rendering, or a migration that was edited after it was generated.",
		fingerprintGolden, got)
}

// TestMigrate_FreshInstall_EveryTableIsStrict asserts canonical conventions §8 mechanically.
//
// docs/design/01-domain-model.md:24 says "Atlas emits STRICT; a schema test asserts every table has
// it" — this is that test. STRICT is what makes PRAGMA integrity_check verify column CONTENT types,
// which is the cheapest guard the project has against a value like "350.00" reaching a centipoint
// column, and it is the property the entire integer-money invariant leans on at the storage layer.
//
// goose's bookkeeping table is exempt: it is goose's schema, not this product's.
func TestMigrate_FreshInstall_EveryTableIsStrict(t *testing.T) {
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

	rows, err := openRaw(t, dbPath).QueryContext(t.Context(),
		`SELECT name, sql FROM sqlite_schema
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version'`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	tables := 0

	for rows.Next() {
		var name, ddl string
		require.NoError(t, rows.Scan(&name, &ddl))

		tables++

		require.Contains(t, strings.ToUpper(normaliseDDL(ddl)), "STRICT",
			"table %q is not STRICT. STRICT permits only INT, INTEGER, REAL, TEXT, BLOB and ANY, and "+
				"it is what makes integrity_check validate column content types — see canonical §8.", name)
	}

	require.NoError(t, rows.Err())
	require.Positive(t, tables, "no product tables found; this test would pass vacuously")
}

// TestMigrate_FreshInstall_NoGuildIDColumn is ADR-0004 as a test rather than a convention.
//
// Single-guild-per-instance is a locked decision. A guild_id column added "for later" is how a
// codebase acquires a tenancy model nobody designed: every query grows a filter that is always the
// same value, and the first one to forget it is a cross-guild data leak in a product whose entire
// pitch is that officers can trust it with their history.
func TestMigrate_FreshInstall_NoGuildIDColumn(t *testing.T) {
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

	rows, err := openRaw(t, dbPath).QueryContext(t.Context(),
		`SELECT name, sql FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	for rows.Next() {
		var name, ddl string
		require.NoError(t, rows.Scan(&name, &ddl))
		require.NotContains(t, strings.ToLower(ddl), "guild_id",
			"table %q has a guild_id column. There is exactly one guild per instance (ADR-0004, "+
				"canonical §9); scope comes from the request principal.", name)
	}

	require.NoError(t, rows.Err())
}

// TestMigrate_FreshInstall_RolePermissionByPermissionKey_IsIndexed is issue #271, asserted the way
// the defect was found: as a query plan rather than as the presence of an index name.
//
// role_permission.permission_key is the child of the foreign key that makes a permission row
// undeletable — ON DELETE NO ACTION, deliberately, because a cascade would silently strip capability
// from every role that held the key. SQLite enforces that by looking up the child rows for the parent
// it was asked to delete, and the table's only key is PRIMARY KEY (role_id, permission_key), which it
// cannot use to find rows by the second column. So the protection was a full scan of the grants table,
// taken while the boot path's single write connection holds the lock.
//
// Asserting the plan rather than `SELECT ... FROM sqlite_schema WHERE name = 'ix_...'` is what makes
// this test about the property: an index that exists and is not chosen buys nothing, and the day an
// index is renamed or its column order changes, the name assertion goes green on a table scan.
//
// The statement below is the lookup SQLite performs for that enforcement, not the DELETE itself: a
// permission row is never deleted by this product (see permission.orphaned_at), so the query that
// belongs in a test is the read the constraint is made of. It is also the role editor's "which roles
// hold this key?" read, which is the other caller that would have scanned.
//
// THE GENERAL CASE IS NOW A GATE, and this test is deliberately not folded into it.
// TestMigrate_FreshInstall_EveryForeignKeyIsCovered (#274) walks every foreign key in the schema and
// requires a covering index or a reasoned waiver; it does not know or care WHICH index answers, which
// is what makes it able to cover keys nobody has thought about. This one names ix_role_permission_permission,
// because the authorization safety boundary is worth an assertion that says out loud which index is
// holding it up.
func TestMigrate_FreshInstall_RolePermissionByPermissionKey_IsIndexed(t *testing.T) {
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

	rows, err := openRaw(t, dbPath).QueryContext(t.Context(),
		`EXPLAIN QUERY PLAN SELECT 1 FROM role_permission WHERE permission_key = ?`, "roster.read")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	var plan []string

	for rows.Next() {
		var id, parent, notUsed int

		var detail string

		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		plan = append(plan, detail)
	}

	require.NoError(t, rows.Err())
	require.NotEmpty(t, plan, "EXPLAIN QUERY PLAN returned nothing; the query never ran")

	joined := strings.Join(plan, " | ")

	require.NotContains(t, joined, "SCAN role_permission",
		"looking up grants by permission key is a full table scan (#271). That scan is how SQLite "+
			"enforces role_permission's ON DELETE NO ACTION on permission(key) — the authorization "+
			"safety boundary — and it runs inside the write transaction. Plan was: %s", joined)
	require.Contains(t, joined, "ix_role_permission_permission",
		"the lookup does not use ix_role_permission_permission. Plan was: %s", joined)
}
