package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/seed"
)

// TestSeed_ResolveProfile_KnownAndUnknown covers the flag-to-profile mapping.
//
// The two unimplemented profiles are the interesting cases. `--profile demo` must SAY that demo
// arrives in Phase 3, not quietly hand back the 520,000-entry performance dataset — a developer who
// got that would spend an afternoon working out why their dev guild had 3,400 raids in it.
func TestSeed_ResolveProfile_KnownAndUnknown(t *testing.T) {
	t.Parallel()

	t.Run("perf is the profile that exists", func(t *testing.T) {
		t.Parallel()

		p, err := resolveProfile(profilePerf, 0)
		require.NoError(t, err)
		require.Equal(t, seed.Perf(), p, "no flag means the profile's own numbers")
	})

	t.Run("raids overrides the depth and not the roster", func(t *testing.T) {
		t.Parallel()

		p, err := resolveProfile(profilePerf, 12)
		require.NoError(t, err)
		require.Equal(t, 12, p.Raids)
		require.Equal(t, seed.Perf().Accounts, p.Accounts)
	})

	t.Run("a zero raid count keeps the profile's own", func(t *testing.T) {
		t.Parallel()

		p, err := resolveProfile(profilePerf, 0)
		require.NoError(t, err)
		require.Equal(t, seed.Perf().Raids, p.Raids)
	})

	// A negative override must FAIL, and the failure mode it replaces is why this test is here
	// rather than in the table below: `raids > 0` alone let -20 fall through to the default, so a
	// hand slipping on a hyphen while asking for a small dataset produced the full 3,400-raid,
	// ~520,000-entry one instead — into an append-only table, with no way back.
	t.Run("a negative raid count is refused, not ignored", func(t *testing.T) {
		t.Parallel()

		for _, raids := range []int{-1, -20, -3400} {
			_, err := resolveProfile(profilePerf, raids)
			require.Error(t, err, "--raids=%d must be refused", raids)
			require.Contains(t, err.Error(), "must not be negative")
		}
	})

	// And it is refused for every profile, because the check runs before the profile switch — a
	// route that reported the profile problem first would leave the flag unvalidated on the branch
	// that eventually implements that profile.
	t.Run("a negative raid count is refused for every profile", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{profilePerf, profileSmall, profileDemo, "nonsense"} {
			_, err := resolveProfile(name, -1)
			require.Error(t, err, "profile %q with --raids=-1", name)
			require.Contains(t, err.Error(), "must not be negative",
				"the flag is malformed whatever the profile is")
		}
	})

	for _, tc := range []struct {
		name    string
		profile string
		want    string
	}{
		{"small names its phase", profileSmall, "Phase 2"},
		{"demo names its phase", profileDemo, "Phase 3"},
		{"an unknown profile lists the known ones", "nonsense", "known profiles are"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := resolveProfile(tc.profile, 0)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestSeed_NoDatabasePath_Fails asserts the command refuses rather than inventing a database file.
// Seeding is not undoable, so guessing where to put half a million rows is the wrong instinct.
func TestSeed_NoDatabasePath_Fails(t *testing.T) {
	t.Setenv(dbPathEnv, "")

	var out bytes.Buffer

	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"seed"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), dbPathEnv)
}

// TestSeed_UnknownProfile_FailsBeforeTouchingTheDatabase asserts the profile is resolved first.
// The database path is deliberately a directory that does not exist: if the command got as far as
// opening it, the error would say so, and the flag error is what must surface instead.
func TestSeed_UnknownProfile_FailsBeforeTouchingTheDatabase(t *testing.T) {
	t.Setenv(dbPathEnv, filepath.Join(t.TempDir(), "nonexistent", "dkp.db"))

	var out bytes.Buffer

	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"seed", "--profile", "nonsense"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown profile")
}

// TestSeed_NegativeRaids_FailsBeforeTouchingTheDatabase is the command-level half of the negative
// override: not just that resolveProfile refuses it, but that the refusal happens before a database
// is opened, let alone written to.
//
// DKP_DB_PATH points at a directory that does not exist, so a command that got as far as opening the
// database would fail with a different, recognisable error. Both spellings are covered because
// pflag accepts them both and a guard that only caught one would be a guard somebody walks around
// by typing a space.
func TestSeed_NegativeRaids_FailsBeforeTouchingTheDatabase(t *testing.T) {
	for _, args := range [][]string{
		{"seed", "--raids=-1"},
		{"seed", "--raids", "-20"},
		{"seed", "--profile", "perf", "--raids=-3400"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			t.Setenv(dbPathEnv, filepath.Join(t.TempDir(), "nonexistent", "dkp.db"))

			var out bytes.Buffer

			cmd := newRootCmd()
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(args)

			err := cmd.Execute()
			require.Error(t, err)
			require.Contains(t, err.Error(), "must not be negative")
			require.NotContains(t, err.Error(), "nonexistent",
				"the flag must be rejected before the database is opened")
		})
	}
}

// TestSeed_SmallRun_WritesAndReports drives the whole command against a real database file: migrate,
// then seed, then read the summary it printed.
//
// One raid, so it costs milliseconds. What it proves is the wiring — that the flags reach the
// profile, the store opens, the generator runs against a migrated database, and the summary reports
// what was written — none of which internal/seed's own tests can check, because they never build a
// command.
func TestSeed_SmallRun_WritesAndReports(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dkp.db")
	t.Setenv(dbPathEnv, dbPath)

	migrate := newRootCmd()
	migrate.SetOut(&bytes.Buffer{})
	migrate.SetErr(&bytes.Buffer{})
	migrate.SetArgs([]string{"migrate"})
	require.NoError(t, migrate.Execute(), "migrate the scratch database")

	var out bytes.Buffer

	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"seed", "--profile", "perf", "--raids", "1"})

	require.NoError(t, cmd.Execute())

	require.Contains(t, out.String(), "seeded perf:")
	require.Contains(t, out.String(), "280 accounts")

	// A second run must refuse: the ledger is append-only, so a top-up cannot be taken back out.
	second := newRootCmd()
	second.SetOut(&bytes.Buffer{})
	second.SetErr(&bytes.Buffer{})
	second.SetArgs([]string{"seed", "--profile", "perf", "--raids", "1"})

	require.ErrorIs(t, second.Execute(), seed.ErrPoolNotEmpty)
}

// TestSeed_LogLevel_QuietUnlessVerbose pins the reason the default is quiet: a full run commits
// twenty thousand batches, and internal/ledger logs one INFO line per commit.
func TestSeed_LogLevel_QuietUnlessVerbose(t *testing.T) {
	t.Parallel()

	require.Greater(t, seedLogLevel(false), seedLogLevel(true),
		"the default level must be higher — quieter — than --verbose")
}
