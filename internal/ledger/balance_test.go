package ledger_test

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// updateGolden is the opt-in flag for regenerating the EXPLAIN QUERY PLAN golden. It is refused under
// CI (see TestBalance_ExplainQueryPlan_IsCoveringIndex): a plan golden that CI can rewrite is a plan
// golden that proves nothing, because the day the query stops using the covering index is the day it
// silently rewrites itself green.
var updateGolden = flag.Bool("update", false, "regenerate the EXPLAIN QUERY PLAN golden (refused under CI)")

const explainGolden = "../../test/golden/explain/ledger_balance.txt"

// insertBatch inserts one batch and a set of (account, amount) entries at the given seq, through
// store.ExecForTest (the raw SQL lives in internal/store, keeping law 2 honest). It returns nothing;
// the entries are what the balance and snapshot tests fold over. There is no commit service in PR 9,
// so the tests write the rows a service will later write.
func insertBatch(tb testing.TB, s *store.Store, poolID string, seq int64, entries map[string]int64) {
	tb.Helper()

	batchID := core.ULID(padID("BATCH", seq))
	var net int64
	for _, amt := range entries {
		net += amt
	}

	s.ExecForTest(tb,
		`INSERT INTO ledger_batch
		   (id, pool_id, seq, kind, strategy_id, strategy_version, source, actor_is_beneficiary,
		    effective_at, recorded_at, effective_day, entry_count, net_amount_cp, hash)
		 VALUES (?, ?, ?, 'award', 'zero_sum', '0.0.0', 'system', 0,
		         1704067200000000, 1704067200000000, '2024-01-01', ?, ?, X'00')`,
		batchID.String(), poolID, seq, len(entries), net)

	i := int64(0)
	for accountID, amt := range entries {
		i++
		entryID := core.ULID(padID("ENTRY", seq*1000+i))
		s.ExecForTest(tb,
			`INSERT INTO ledger_entry (id, batch_id, pool_id, seq, account_id, balance_kind, amount_cp)
			 VALUES (?, ?, ?, ?, ?, 'dkp', ?)`,
			entryID.String(), batchID.String(), poolID, seq, accountID, amt)
	}
}

// padID builds a deterministic, valid 26-char ULID from a prefix and a number. Test-only: real ids
// are minted by core.Generator.
//
// The digits are CROCKFORD base32, not strconv's. This used to be
// `strings.ToUpper(strconv.FormatInt(n, 32))`, whose alphabet is 0-9a-v — which contains I, L, O and
// U, the four letters Crockford excludes precisely so a human reading an id aloud cannot turn a 1
// into an I. Roughly one id in eight came out failing `core.ULID.Valid()` while the comment above it
// said it was valid. Nothing depended on it (these ids go into TEXT columns with no format CHECK),
// so it was a false claim rather than a bug — but a test helper that mints invalid ids is a helper
// that will eventually be used somewhere that checks.
//
// Order-preserving, which IS depended on: TestAllocate_Tiebreak_IsAccountIDAscending asserts the
// largest-remainder +1 lands on the lowest account ids, and it could not do so against ids whose
// lexical order did not follow their index. The fixed-width, most-significant-digit-first encoding
// below keeps that property.
func padID(prefix string, n int64) string {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	// Six digits: 32^6 is a billion, comfortably more than any test index, and fixed-width so the
	// tail sorts numerically rather than by length.
	const digits = 6

	body := []byte(strings.Repeat("0", digits))
	for i := range digits {
		body[digits-1-i] = crockford[n%int64(len(crockford))]
		n /= int64(len(crockford))
	}

	full := prefix + string(body)
	if len(full) > core.ULIDLength {
		full = full[:core.ULIDLength]
	}

	// Left-pad with '0' to 26 chars. '0' is a legal Crockford base32 digit and a legal leading char,
	// which keeps the encoded timestamp at zero and inside the overflow rule ParseStrict enforces.
	return strings.Repeat("0", core.ULIDLength-len(full)) + full
}

// TestPadID_MintsValidULIDs asserts the helper above produces ids core.ULID accepts, and that their
// order follows their index. Both properties are load-bearing and neither was checked before.
func TestPadID_MintsValidULIDs(t *testing.T) {
	t.Parallel()

	prev := ""

	for _, prefix := range []string{"ACCT", "BATCH", "ENTRY", "SNAPB", "SNAPE", "PERS"} {
		prev = ""

		for n := range int64(300) {
			id := padID(prefix, n)

			require.Len(t, id, core.ULIDLength, "%s/%d", prefix, n)
			require.True(t, core.ULID(id).Valid(),
				"%s/%d produced %q, which is not a valid Crockford ULID", prefix, n, id)
			require.Greater(t, id, prev, "%s ids must sort by index", prefix)

			prev = id
		}
	}
}

// TestBalance_SumMatchesFold asserts BalanceAsOfSeq equals a naive Go fold over the entries, with the
// as-of-seq cutoff respected (PR 9 acceptance criterion 3). It inserts three batches at seq 1..3 for
// one account, then checks the balance at each seq and beyond.
func TestBalance_SumMatchesFold(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	poolID := ledger.DefaultPoolID
	acct := ledger.AccountIDGuildBank

	// seq -> delta for this account.
	deltas := map[int64]int64{1: 100, 2: -30, 3: 250}
	insertBatch(t, s, poolID.String(), 1, map[string]int64{acct.String(): deltas[1]})
	insertBatch(t, s, poolID.String(), 2, map[string]int64{acct.String(): deltas[2]})
	insertBatch(t, s, poolID.String(), 3, map[string]int64{acct.String(): deltas[3]})

	cases := []struct {
		asOfSeq int64
		want    core.Centipoints
	}{
		{asOfSeq: 0, want: 0},   // before anything
		{asOfSeq: 1, want: 100}, // after batch 1
		{asOfSeq: 2, want: 70},  // 100 - 30
		{asOfSeq: 3, want: 320}, // 100 - 30 + 250
		{asOfSeq: 9, want: 320}, // beyond the head is still the head balance
	}

	for _, tc := range cases {
		got, err := ledger.BalanceAsOfSeq(t.Context(), s.Q(), poolID, acct, "dkp", tc.asOfSeq)
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "balance as of seq %d", tc.asOfSeq)
	}

	// CurrentBalance derives the head via MaxPoolSeq and must equal the balance at the head seq.
	current, err := ledger.CurrentBalance(t.Context(), s.Q(), poolID, acct, "dkp")
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(320), current, "current balance is the balance at the head seq")

	// A different balance_kind on the same account is a separate running total; here, zero.
	other, err := ledger.BalanceAsOfSeq(t.Context(), s.Q(), poolID, acct, "ep", 3)
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(0), other, "an unrelated balance_kind sums to zero")
}

// TestBalance_ExplainQueryPlan_IsCoveringIndex diffs the balance query's EXPLAIN QUERY PLAN against
// the committed golden and asserts it is served from ix_entry_balance as a COVERING INDEX with no
// table access (PR 9 acceptance criterion 3). A plan regression — a dropped index, a reordered
// column, a query rewrite that forces a table read — changes the plan and fails here.
//
// -update is REFUSED under CI: regenerating a plan golden must be a decision a human typed on a
// laptop, never a flag CI ran, or the guard rewrites itself green the moment standings gets slow.
func TestBalance_ExplainQueryPlan_IsCoveringIndex(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	rows := s.QueryForTest(t,
		`EXPLAIN QUERY PLAN
		 SELECT CAST(COALESCE(sum(amount_cp), 0) AS INTEGER) AS amount_cp
		 FROM ledger_entry
		 WHERE pool_id = ? AND account_id = ? AND balance_kind = ? AND seq <= ?`,
		"p", "a", "dkp", int64(1))
	defer func() { require.NoError(t, rows.Close()) }()

	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())

	got := strings.Join(details, "\n")

	// Substantive assertions, independent of the golden's exact bytes: the covering index is used and
	// nothing scans the table. These are what actually make standings fast; the golden pins the whole
	// plan so an unexpected change is loud.
	require.Contains(t, got, "USING COVERING INDEX ix_entry_balance",
		"the balance query must be served from the covering index ix_entry_balance")
	require.NotContains(t, got, "SCAN ledger_entry",
		"the balance query must not scan the ledger_entry table")

	if *updateGolden {
		if os.Getenv("CI") == "true" {
			t.Fatal("refusing -update under CI: a plan golden CI can rewrite proves nothing")
		}
		require.NoError(t, os.WriteFile(explainGolden, []byte(got+"\n"), 0o644))
		t.Logf("wrote %s", explainGolden)
	}

	want, err := os.ReadFile(explainGolden)
	require.NoError(t, err, "read the committed EXPLAIN golden at %s", explainGolden)
	require.Equal(t, strings.TrimSpace(string(want)), got,
		"the balance query plan changed. If you meant it, re-run with -update on a laptop (never CI) "+
			"and confirm the new plan still uses the covering index with no table access.")
}
