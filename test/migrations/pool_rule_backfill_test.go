package migrations_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/migrate"
)

// 000006's backfill, tested on the upgrade path rather than on a fresh install. Phase 1, #213.
//
// THE FAILURE THIS EXISTS FOR was found in review of the PR that added the migration. Its first cut
// wrote EVERY legacy pool.strategy_id into earn_strategy_id, which is wrong for three of the four
// shipped strategies: `fixed_price` was a perfectly valid singular pool strategy and declares
// RuleSpend, so an upgraded fixed-price pool resolved to ErrWrongRuleKind and could not be read at
// all. Nothing caught it, because a FRESH install has exactly one pool and that pool's strategy is
// 'zero_sum' — the one id whose placement the bug did not change.
//
// That is the "works on a fresh install, breaks on upgrade" class, which .claude/rules/migrations.md
// calls the most damaging bug class for this audience. The only way to see it is to put rows in the
// table BEFORE the migration runs, which is what this file does: apply the set truncated to 000005,
// seed a pool per legacy strategy id, then apply 000006 and read the slots back.
//
// RAW SQL AND NOT internal/strategy, for the reason at the top of harness_test.go: a migration test
// that asked the domain package where a rule belongs would be asserting that today's Go and today's
// SQL agree with each other, which is not the question. The question is which column the migration
// wrote. TestPoolConfig_Resolve_* on the Go side owns the other half.

// poolBeforeUpgrade is one pool as it exists on a database that has not yet run 000006: an id, a
// name, and the singular strategy columns.
type poolBeforeUpgrade struct {
	id         string
	strategyID string

	// wantSlot is the column 000006 must move strategyID into, or "" for an id that no shipped
	// strategy answers to and which must therefore be left unconfigured.
	wantSlot string
}

// TestMigrate_PoolRuleBackfill_PlacesEachLegacyStrategyInItsOwnSlot is the upgrade path, per id.
func TestMigrate_PoolRuleBackfill_PlacesEachLegacyStrategyInItsOwnSlot(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	// One row per shipped strategy, plus the two cases that are not a shipped strategy at all:
	// 'zero_sum', which is what 000003 seeds the default pool with, and an id from a build nobody
	// has. Both must be left unconfigured rather than written somewhere that cannot resolve.
	pools := []poolBeforeUpgrade{
		{id: "00000000000000000000POOL01", strategyID: "tick", wantSlot: "earn_strategy_id"},
		{id: "00000000000000000000POOL02", strategyID: "fixed_price", wantSlot: "spend_strategy_id"},
		{id: "00000000000000000000POOL03", strategyID: "cap", wantSlot: "over_time_strategy_id"},
		{id: "00000000000000000000POOL04", strategyID: "start_points", wantSlot: "over_time_strategy_id"},
		{id: "00000000000000000000POOL05", strategyID: "zero_sum", wantSlot: ""},
		{id: "00000000000000000000POOL06", strategyID: "epgp", wantSlot: ""},
	}

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	// (a) The world as it was: every migration up to and including 000005, which is the last one
	// before the three rule slots exist.
	runner, err := migrate.New(migrationDirUpTo(t, 5), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v0.5.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()))

	// (b) Guilds that had already configured a pool.
	withRawFK(t, dbPath, func(handle *sql.DB) {
		for _, p := range pools {
			_, err := handle.ExecContext(t.Context(),
				`INSERT INTO pool (id, name, name_norm, strategy_id, strategy_version,
				                   strategy_config_json, balance_kinds, created_at, updated_at)
				 VALUES (?, ?, ?, ?, '0.1.0', ?, 'dkp', 1704067200000000, 1704067200000000)`,
				p.id, "Pool "+p.strategyID, "pool"+p.strategyID, p.strategyID,
				`{"marker":"`+p.strategyID+`"}`)
			require.NoError(t, err, "seed pool for %s", p.strategyID)
		}
	})

	// (c) The upgrade.
	runner, err = migrate.New(migrationDirUpTo(t, 6), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v0.6.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()),
		"000006 must apply to a database that already holds configured pools")

	withRawFK(t, dbPath, func(handle *sql.DB) {
		for _, p := range pools {
			t.Run(p.strategyID, func(t *testing.T) {
				slots := readSlots(t, handle, p.id)

				for _, column := range []string{"earn_strategy_id", "spend_strategy_id", "over_time_strategy_id"} {
					want := ""
					if column == p.wantSlot {
						want = p.strategyID
					}

					require.Equal(t, want, slots[column],
						"a %s pool must land in %q and nowhere else; putting a rule in a slot it does "+
							"not answer makes PoolConfig.Resolve refuse the whole pool with "+
							"ErrWrongRuleKind, and an unknown id makes it refuse with "+
							"ErrUnknownStrategy — either way the guild's pool is unreadable after an "+
							"upgrade that worked perfectly on a fresh install",
						p.strategyID, p.wantSlot)
				}

				// The CONFIG travels with the id, into the matching slot and no other. A backfill
				// that moved the id and left the document behind would silently reset every knob the
				// guild had set.
				config := ""
				if p.wantSlot != "" {
					config = `{"marker":"` + p.strategyID + `"}`
				}

				for _, column := range []string{"earn_config_json", "spend_config_json", "over_time_config_json"} {
					want := "{}"
					if p.wantSlot != "" && column == configColumnFor(p.wantSlot) {
						want = config
					}

					require.Equal(t, want, slots[column],
						"the config document follows its id into the same slot")
				}

				// The superseded column is untouched: it is read by nothing, and rewriting it would
				// destroy the record of what the pool used to be before anybody had reviewed the
				// backfill's placement.
				require.Equal(t, p.strategyID, slots["strategy_id"],
					"the singular column is superseded, not rewritten")
			})
		}
	})
}

// TestMigrate_PoolRuleBackfill_LeavesAConfiguredPoolAlone is the idempotency half.
//
// Migration-on-boot can be interrupted, restored from the pre-migration snapshot and run again, so a
// backfill that is not idempotent corrupts on the second attempt. This one guards on all three slots
// still being empty, so a pool an officer has configured since — or one the first attempt already
// placed — is left exactly as it is rather than reset to whatever the superseded column says.
func TestMigrate_PoolRuleBackfill_LeavesAConfiguredPoolAlone(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("applies real migrations to a real database; run `make test` or `make check`")
	}

	const poolID = "00000000000000000000POOL07"

	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "dkp.db")

	runner, err := migrate.New(migrationDirUpTo(t, 6), migrate.Config{
		DBPath: dbPath, DataDir: dataDir, BinaryVersion: "v0.6.0", AutoMigrate: true,
	})
	require.NoError(t, err)
	require.NoError(t, runner.Migrate(t.Context()))

	withRawFK(t, dbPath, func(handle *sql.DB) {
		// A pool composed the way the product intends, whose superseded column still says something
		// else entirely. Re-running the backfill must not touch it.
		_, err := handle.ExecContext(t.Context(),
			`INSERT INTO pool (id, name, name_norm, strategy_id, strategy_version,
			                   strategy_config_json, earn_strategy_id, earn_config_json,
			                   spend_strategy_id, spend_config_json, balance_kinds,
			                   created_at, updated_at)
			 VALUES (?, 'Velious Main', 'veliousmain', 'tick', '0.1.0', '{"stale":true}',
			         'tick', '{"tick_award_cp":1000}', 'fixed_price', '{"default_price_cp":500}',
			         'dkp', 1704067200000000, 1704067200000000)`,
			poolID)
		require.NoError(t, err)

		// The backfill's three statements, replayed exactly as 000006 runs them.
		replayPoolRuleBackfill(t, handle)

		slots := readSlots(t, handle, poolID)
		require.Equal(t, "tick", slots["earn_strategy_id"])
		require.Equal(t, `{"tick_award_cp":1000}`, slots["earn_config_json"],
			"a re-run must not overwrite a configured slot with the superseded column's document")
		require.Equal(t, "fixed_price", slots["spend_strategy_id"])
		require.Equal(t, "", slots["over_time_strategy_id"],
			"and must not fill an empty slot on a pool that is already composed")
	})
}

// configColumnFor maps a rule slot's id column to its config column.
func configColumnFor(idColumn string) string {
	switch idColumn {
	case "earn_strategy_id":
		return "earn_config_json"
	case "spend_strategy_id":
		return "spend_config_json"
	default:
		return "over_time_config_json"
	}
}

// readSlots reads one pool's seven strategy columns.
func readSlots(tb testing.TB, handle *sql.DB, poolID string) map[string]string {
	tb.Helper()

	var strategyID, earnID, earnCfg, spendID, spendCfg, overID, overCfg string

	err := handle.QueryRowContext(context.Background(),
		`SELECT strategy_id, earn_strategy_id, earn_config_json, spend_strategy_id,
		        spend_config_json, over_time_strategy_id, over_time_config_json
		 FROM pool WHERE id = ?`, poolID).
		Scan(&strategyID, &earnID, &earnCfg, &spendID, &spendCfg, &overID, &overCfg)
	require.NoError(tb, err, "read pool %s", poolID)

	return map[string]string{
		"strategy_id":           strategyID,
		"earn_strategy_id":      earnID,
		"earn_config_json":      earnCfg,
		"spend_strategy_id":     spendID,
		"spend_config_json":     spendCfg,
		"over_time_strategy_id": overID,
		"over_time_config_json": overCfg,
	}
}

// replayPoolRuleBackfill runs 000006's three UPDATE statements again, against a database that has
// already applied it.
//
// It re-states the SQL rather than re-executing the file, and the duplication is the point: a
// migration that has run is not re-runnable through goose, so the only way to assert the property
// the statements claim ("idempotent, safe on a populated database") is to run them a second time.
// The guard clause is what is under test, so it is spelled out here exactly as the migration spells
// it -- a copy that drifted would assert nothing about the file.
func replayPoolRuleBackfill(tb testing.TB, handle *sql.DB) {
	tb.Helper()

	const empty = ` AND earn_strategy_id = '' AND spend_strategy_id = '' AND over_time_strategy_id = ''`

	for _, stmt := range []string{
		`UPDATE pool SET earn_strategy_id = strategy_id, earn_config_json = strategy_config_json
		 WHERE strategy_id IN ('tick')` + empty,
		`UPDATE pool SET spend_strategy_id = strategy_id, spend_config_json = strategy_config_json
		 WHERE strategy_id IN ('fixed_price')` + empty,
		`UPDATE pool SET over_time_strategy_id = strategy_id, over_time_config_json = strategy_config_json
		 WHERE strategy_id IN ('cap', 'start_points')` + empty,
	} {
		_, err := handle.ExecContext(context.Background(), stmt)
		require.NoError(tb, err, "replay the backfill")
	}
}
