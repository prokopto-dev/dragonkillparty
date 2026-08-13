package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The command half of `dkp verify-ledger` (issue #198). internal/ledger/verify_test.go owns the
// question "does the replay detect a corrupted ledger"; this file owns the two questions only a
// command can answer — what an operator SEES, and what the process EXITS WITH.
//
// The exit code is the whole contract with the nightly job. `make verify-ledger` and
// nightly-verify.yml's `replay / seed.Perf` read nothing else, so a command that printed a perfect
// report and returned nil on a drifted ledger would be a green nightly for ever.

// runVerifyLedger drives the command through the real root, so flag parsing and wiring are exercised
// rather than the RunE body alone. It returns stdout and the error the command exited with.
func runVerifyLedger(tb testing.TB, args ...string) (string, error) {
	tb.Helper()

	var out bytes.Buffer

	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"verify-ledger"}, args...))

	// Executed on its own line: `return out.String(), cmd.Execute()` evaluates the operands left to
	// right, so the buffer would be read before the command had written a byte into it.
	err := cmd.Execute()

	return out.String(), err
}

// seedScratchLedger migrates a scratch database and seeds one raid into it, returning its path.
//
// Through the REAL commands, both of them. A fixture written by hand would prove the report renders;
// this proves the report renders over rows ledger.Service.Commit wrote, which is the only claim
// worth making.
func seedScratchLedger(tb testing.TB) string {
	tb.Helper()

	dbPath := filepath.Join(tb.TempDir(), "dkp.db")
	tb.Setenv(dbPathEnv, dbPath)

	for _, args := range [][]string{
		{"migrate"},
		{"seed", "--profile", "perf", "--raids", "1"},
	} {
		cmd := newRootCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		require.NoErrorf(tb, cmd.Execute(), "prepare the scratch database: %v", args)
	}

	return dbPath
}

// driftOneCachedBalance and driftEveryCachedBalance make the cache disagree with the log by one
// centipoint, which is the smallest drift there is and the one a member notices.
//
// An ordinary UPDATE, because balance_snapshot is an ordinary table: it is derived, so it carries
// none of the append-only triggers the two ledger tables do. That asymmetry is the point — the log
// cannot be edited even by a test, and the thing derived from it can, which is precisely the shape
// of the failure a nightly replay exists to catch.
//
// The raw statement runs through store.ExecForTest, so the .Exec call itself lives inside
// internal/store and law 2 stays literally true (gate SQL002); SQL003 allows the helper here because
// this is a _test.go file.
func driftOneCachedBalance(tb testing.TB, dbPath string) {
	driftCachedBalances(tb, dbPath,
		`UPDATE balance_snapshot SET amount_cp = amount_cp + 1
		 WHERE account_id = (SELECT min(account_id) FROM balance_snapshot)`)
}

func driftEveryCachedBalance(tb testing.TB, dbPath string) {
	driftCachedBalances(tb, dbPath, `UPDATE balance_snapshot SET amount_cp = amount_cp + 1`)
}

func driftCachedBalances(tb testing.TB, dbPath, query string) {
	tb.Helper()

	s, err := store.Open(tb.Context(), dbPath)
	require.NoError(tb, err)

	defer func() { require.NoError(tb, s.Close()) }()

	res := s.ExecForTest(tb, query)

	affected, err := res.RowsAffected()
	require.NoError(tb, err)
	require.Positive(tb, affected, "the fixture must actually drift something")
}

// TestVerifyLedger_NoDatabasePath_Fails asserts the command refuses rather than inventing a database
// file — which, for a verifier, would mean reporting a clean ledger for a database that does not
// exist.
func TestVerifyLedger_NoDatabasePath_Fails(t *testing.T) {
	t.Setenv(dbPathEnv, "")

	out, err := runVerifyLedger(t)
	require.Error(t, err)
	require.Contains(t, err.Error(), dbPathEnv)
	require.NotContains(t, out, "clean", "an unopened database is never a clean verdict")
}

// TestVerifyLedger_UnopenableDatabase_Fails is the same property one step later: the path is set but
// its directory does not exist.
func TestVerifyLedger_UnopenableDatabase_Fails(t *testing.T) {
	t.Setenv(dbPathEnv, filepath.Join(t.TempDir(), "nonexistent", "dkp.db"))

	out, err := runVerifyLedger(t)
	require.Error(t, err)
	require.NotContains(t, out, "clean")
}

// TestVerifyLedger_SeededLedger_ReportsCleanAndExitsZero is the acceptance criterion of issue #198,
// at the smallest scale that still runs the whole thing: migrate, seed through the real commit path,
// replay.
//
// The COUNTS are asserted alongside the verdict. "ledger verified clean" over an empty database is
// the failure this command exists to avoid printing, and it is indistinguishable from success unless
// the numbers are read.
func TestVerifyLedger_SeededLedger_ReportsCleanAndExitsZero(t *testing.T) {
	seedScratchLedger(t)

	out, err := runVerifyLedger(t)
	require.NoError(t, err, "a ledger written by the commit path must verify clean")

	require.Contains(t, out, "ledger verified clean")
	require.Contains(t, out, "replayed 1 pool(s)")
	require.Contains(t, out, "head seq")
	require.NotContains(t, out, "(no batches)", "the seeded pool has batches")
	require.NotContains(t, out, "(no rows)", "the seeded pool wrote audit rows")

	// The progress callback is on by default, because a full-profile replay is a three-minute
	// command and a silent one looks hung.
	require.Contains(t, out, "batches,")

	// And --quiet suppresses exactly that and nothing else.
	quiet, err := runVerifyLedger(t, "--quiet")
	require.NoError(t, err)
	require.Contains(t, quiet, "ledger verified clean")
	require.NotContains(t, quiet, "  pool 00000000000000000000DKPP00: ",
		"--quiet drops the progress lines")
}

// TestVerifyLedger_DriftedCache_ReportsAndExitsNonZero is the test the nightly job's value rests on.
//
// balance_snapshot is derived and is not append-only, so a single UPDATE is all it takes to simulate
// the drift this command exists to find — which is also exactly how the real thing would arrive,
// since drift means the cache and the log stopped agreeing while the log stayed intact. Under
// ADR-0023 that drift has no fallback to hide behind, which is why the exit code has to be right.
func TestVerifyLedger_DriftedCache_ReportsAndExitsNonZero(t *testing.T) {
	dbPath := seedScratchLedger(t)

	driftOneCachedBalance(t, dbPath)

	out, err := runVerifyLedger(t, "--quiet")
	require.Error(t, err, "drift must exit non-zero, or the nightly job is green for ever")
	require.Equal(t, errVerifyFailed, err)

	require.Contains(t, out, "finding(s):")
	require.Contains(t, out, string(ledger.FindingSnapshotAmountMismatch))
	require.Contains(t, out, "the log sums to")
	require.NotContains(t, out, "ledger verified clean")
}

// TestVerifyLedger_MaxFindings_CapsTheListAndSaysSo covers the flag and the no-silent-caps rule: a
// report that printed its cap and stopped, without saying it had stopped, reads as a complete list.
func TestVerifyLedger_MaxFindings_CapsTheListAndSaysSo(t *testing.T) {
	dbPath := seedScratchLedger(t)

	driftEveryCachedBalance(t, dbPath)

	capped, err := runVerifyLedger(t, "--quiet", "--max-findings", "1")
	require.Error(t, err)
	require.Contains(t, capped, "and")
	require.Contains(t, capped, "more; re-run with --max-findings=-1")

	all, err := runVerifyLedger(t, "--quiet", "--max-findings", "-1")
	require.Error(t, err)
	require.NotContains(t, all, "re-run with --max-findings=-1",
		"nothing was truncated, so nothing says it was")
	require.Greater(t,
		strings.Count(all, string(ledger.FindingSnapshotAmountMismatch)),
		strings.Count(capped, string(ledger.FindingSnapshotAmountMismatch)),
		"the uncapped run lists more findings than the capped one")
}
