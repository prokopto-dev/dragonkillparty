package ledger_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The standings reads, at a size a human can check. Phase 1, issue #190.
//
// standings_perf_test.go measures these two queries over half a million entries; this file pins what
// they MEAN and what plan they run, over a handful of rows and in milliseconds. The split matters:
// a plan golden is worth having on every PR, and it does not need 520k rows to be true — an
// EXPLAIN QUERY PLAN depends on the schema and the indexes, not on how much data is behind them.

const (
	standingsSnapshotGolden = "../../test/golden/explain/standings_snapshot.txt"
	standingsLedgerGolden   = "../../test/golden/explain/standings_ledger.txt"
)

// TestStandings_Snapshot_OrdersByBalanceThenAccountID asserts the standings come back highest-first
// with a deterministic tiebreak.
//
// THE TIEBREAK IS THE POINT. Ties are not exotic in a DKP pool — a guild that starts everybody at
// the same number has 280 of them on day one, and a zero-sum split hands the same credit to
// everybody present — so "highest first" alone does not define an order. Without the account_id
// tiebreak the rows tied at a balance come back in whatever order the planner feels like, which is
// stable until the day it is not, and a page boundary that lands inside a tie then skips a member
// or shows them twice.
func TestStandings_Snapshot_OrdersByBalanceThenAccountID(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	poolID := ledger.DefaultPoolID

	// Three accounts tied at 100 and one clear leader, so the tiebreak has something to break.
	accounts := seedPersonAccounts(t, s, 4)
	amounts := map[core.ULID]int64{
		accounts[0]: 100,
		accounts[1]: 100,
		accounts[2]: 100,
		accounts[3]: 900,
	}

	seq := int64(1)
	for _, a := range accounts {
		insertBatch(t, s, poolID.String(), seq, map[string]int64{a.String(): amounts[a]})

		err := ledger.UpsertBalanceSnapshot(t.Context(), s.Q(), ledger.SnapshotDelta{
			PoolID:      poolID,
			AccountID:   a,
			BalanceKind: strategy.BalanceKindDKP,
			AmountCp:    core.Centipoints(amounts[a]),
			AsOfSeq:     seq,
			EntryCount:  1,
			UpdatedAt:   core.Micros(1_704_067_200_000_000 + seq),
		})
		require.NoError(t, err)

		seq++
	}

	got, err := ledger.StandingsFromSnapshot(t.Context(), s.Q(), poolID, strategy.BalanceKindDKP, 10)
	require.NoError(t, err)

	want := []ledger.Standing{
		{AccountID: accounts[3], AmountCp: 900, AsOfSeq: 4, EntryCount: 1},
		{AccountID: accounts[0], AmountCp: 100, AsOfSeq: 1, EntryCount: 1},
		{AccountID: accounts[1], AmountCp: 100, AsOfSeq: 2, EntryCount: 1},
		{AccountID: accounts[2], AmountCp: 100, AsOfSeq: 3, EntryCount: 1},
	}
	require.Equal(t, want, got,
		"standings are highest-first, and the three accounts tied at 100 come back in account_id order")

	// The definitional arm answers identically. It must: it is the same question asked of the log
	// rather than of the cache, and a page that disagreed with a dispute would be worse than no page.
	folded, err := ledger.StandingsFromLedger(
		t.Context(), s.Q(), poolID, strategy.BalanceKindDKP, seq-1, 10)
	require.NoError(t, err)

	require.Equal(t, len(want), len(folded))

	for i := range want {
		require.Equal(t, want[i].AccountID, folded[i].AccountID, "row %d", i)
		require.Equal(t, want[i].AmountCp, folded[i].AmountCp, "row %d", i)
		require.Equal(t, want[i].EntryCount, folded[i].EntryCount, "row %d", i)
	}
}

// TestStandings_Snapshot_Limit_BoundsThePage asserts the limit is honoured, and that the rows
// returned are the TOP of the order rather than an arbitrary subset.
func TestStandings_Snapshot_Limit_BoundsThePage(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	poolID := ledger.DefaultPoolID

	accounts := seedPersonAccounts(t, s, 5)

	for i, a := range accounts {
		seq := int64(i + 1)
		amount := int64((i + 1) * 100)

		insertBatch(t, s, poolID.String(), seq, map[string]int64{a.String(): amount})

		require.NoError(t, ledger.UpsertBalanceSnapshot(t.Context(), s.Q(), ledger.SnapshotDelta{
			PoolID:      poolID,
			AccountID:   a,
			BalanceKind: strategy.BalanceKindDKP,
			AmountCp:    core.Centipoints(amount),
			AsOfSeq:     seq,
			EntryCount:  1,
			UpdatedAt:   core.Micros(1_704_067_200_000_000 + seq),
		}))
	}

	got, err := ledger.StandingsFromSnapshot(t.Context(), s.Q(), poolID, strategy.BalanceKindDKP, 2)
	require.NoError(t, err)

	require.Len(t, got, 2)
	require.Equal(t, accounts[4], got[0].AccountID, "the highest balance leads the page")
	require.Equal(t, accounts[3], got[1].AccountID, "then the second highest")
}

// TestStandings_Snapshot_EmptyPool_ReturnsNoRows asserts an unseeded pool reports an empty
// standings rather than an error. A guild on its first day has no batches, and the page it sees
// then is a table with no rows in it, not a 500.
func TestStandings_Snapshot_EmptyPool_ReturnsNoRows(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	got, err := ledger.StandingsFromSnapshot(
		t.Context(), s.Q(), ledger.DefaultPoolID, strategy.BalanceKindDKP, 10)
	require.NoError(t, err)
	require.Empty(t, got)

	folded, err := ledger.StandingsFromLedger(
		t.Context(), s.Q(), ledger.DefaultPoolID, strategy.BalanceKindDKP, 0, 10)
	require.NoError(t, err)
	require.Empty(t, folded)
}

// TestStandings_SnapshotPlan_WalksTheStandingsIndex diffs the cache arm's plan against its golden and
// asserts the two properties that make it fast: it is served from ix_snapshot_standings, and there
// is NO temporary b-tree — the index walk produces `amount_cp DESC, account_id ASC` directly.
//
// The absence of the sort is the load-bearing half. balance_snapshot is WITHOUT ROWID, so the
// index's trailing key columns are the primary key, which is what makes the account_id tiebreak free
// rather than a materialise-and-sort. If that ever stops being true this test says so, and the
// standings page starts sorting 280 rows on every request without anybody noticing.
func TestStandings_SnapshotPlan_WalksTheStandingsIndex(t *testing.T) {
	t.Parallel()

	got := explainPlan(t, store.NewDB(t),
		`SELECT account_id, amount_cp, as_of_seq, entry_count
		 FROM balance_snapshot
		 WHERE pool_id = ? AND balance_kind = ?
		 ORDER BY amount_cp DESC, account_id ASC
		 LIMIT ?`,
		"p", "dkp", int64(280))

	require.Contains(t, got, "ix_snapshot_standings",
		"the standings read must be served from ix_snapshot_standings")
	require.NotContains(t, got, "SCAN balance_snapshot",
		"the standings read must not scan balance_snapshot")
	require.NotContains(t, got, "TEMP B-TREE",
		"the index walk must produce the standings order directly; a temp b-tree here means the "+
			"index no longer supports the account_id tiebreak and every page is a sort")

	requireGolden(t, standingsSnapshotGolden, got)
}

// TestStandings_LedgerPlan_IsCoveringAndSorts pins the definitional arm's plan — including the part
// that is bad news.
//
// It IS served from the covering index ix_entry_balance, so it never touches the ledger_entry table.
// It also USES A TEMP B-TREE for the ORDER BY, because the grouped sum cannot be ordered by its own
// aggregate until it has computed all of them. Both are asserted, and the second is asserted
// POSITIVELY rather than tolerated: it is a real cost of the definitional route, it is part of why
// balance_snapshot exists, and a change that removed it would be a change worth reading about
// rather than one that quietly relaxed a test.
func TestStandings_LedgerPlan_IsCoveringAndSorts(t *testing.T) {
	t.Parallel()

	got := explainPlan(t, store.NewDB(t),
		`SELECT account_id,
		        CAST(COALESCE(sum(amount_cp), 0) AS INTEGER) AS amount_cp,
		        CAST(count(*) AS INTEGER) AS entry_count
		 FROM ledger_entry
		 WHERE pool_id = ? AND balance_kind = ? AND seq <= ?
		 GROUP BY account_id
		 ORDER BY amount_cp DESC, account_id ASC
		 LIMIT ?`,
		"p", "dkp", int64(1), int64(280))

	require.Contains(t, got, "USING COVERING INDEX ix_entry_balance",
		"the definitional standings must be served from the covering balance index")
	require.NotContains(t, got, "SCAN ledger_entry",
		"the definitional standings must not scan the ledger_entry table")
	require.Contains(t, got, "TEMP B-TREE",
		"the definitional standings sorts its groups; if this stops being true, re-measure V5 — the "+
			"comparison between the two arms was made with this cost in it")

	requireGolden(t, standingsLedgerGolden, got)
}

// TestStandings_SnapshotProjection_WithoutTheDriftColumns_IsCovering records, executably, the one
// lever the V5 verdict leaves on the table.
//
// StandingsFromSnapshot selects as_of_seq and entry_count — the two columns that make a drift check
// possible — and neither is carried by ix_snapshot_standings, so the query walks the index and then
// probes the WITHOUT ROWID primary key. The perf suite measures what that costs: seven pages of the
// thirteen, about 14 ms of the 26 ms modelled on SD-card-class storage, against a budget of 150.
//
// It is not worth a migration at that price, and this test is why the claim can be made without one:
// the SAME index answers the page-render projection with no table access at all. If Phase 3 ever
// needs those fourteen milliseconds, the fix is to select two columns instead of four — no schema
// change, no wider index, no migration over a table that carries a cache.
func TestStandings_SnapshotProjection_WithoutTheDriftColumns_IsCovering(t *testing.T) {
	t.Parallel()

	got := explainPlan(t, store.NewDB(t),
		`SELECT account_id, amount_cp
		 FROM balance_snapshot
		 WHERE pool_id = ? AND balance_kind = ?
		 ORDER BY amount_cp DESC, account_id ASC
		 LIMIT ?`,
		"p", "dkp", int64(280))

	require.Contains(t, got, "USING COVERING INDEX ix_snapshot_standings",
		"selecting only the rendered columns must be answerable from the index alone")
	require.NotContains(t, got, "TEMP B-TREE", "and still without a sort")
}

// explainPlan runs EXPLAIN QUERY PLAN and returns the detail lines, newline-joined.
func explainPlan(tb testing.TB, s *store.Store, query string, args ...any) string {
	tb.Helper()

	rows := s.QueryForTest(tb, "EXPLAIN QUERY PLAN "+query, args...)
	defer func() { require.NoError(tb, rows.Close()) }()

	var details []string

	for rows.Next() {
		var (
			id, parent, notused int
			detail              string
		)

		require.NoError(tb, rows.Scan(&id, &parent, &notused, &detail))
		details = append(details, detail)
	}

	require.NoError(tb, rows.Err())

	return strings.Join(details, "\n")
}

// requireGolden compares got against a committed plan golden, honouring -update.
//
// -update is REFUSED under CI, for the reason TestBalance_ExplainQueryPlan_IsCoveringIndex gives:
// regenerating a plan golden must be a decision a human typed on a laptop, never a flag CI ran, or
// the guard rewrites itself green the moment standings gets slow.
func requireGolden(tb testing.TB, path, got string) {
	tb.Helper()

	if *updateGolden {
		if os.Getenv("CI") == "true" {
			tb.Fatal("refusing -update under CI: a plan golden CI can rewrite proves nothing")
		}

		require.NoError(tb, os.WriteFile(path, []byte(got+"\n"), 0o644))
		tb.Logf("wrote %s", path)
	}

	want, err := os.ReadFile(path)
	require.NoError(tb, err, "read the committed EXPLAIN golden at %s", path)
	require.Equal(tb, strings.TrimSpace(string(want)), got,
		"the query plan changed. If you meant it, re-run with -update on a laptop (never CI) and "+
			"confirm the new plan is still the one V5 was measured against.")
}
