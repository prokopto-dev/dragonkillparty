package ledger_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestSnapshot_TenThousandEntries_MatchesFold asserts balance_snapshot, upserted additively in the
// same transaction as the entries, equals a naive Go fold over all entries (PR 9 acceptance criterion
// 5) — and equals BalanceAsOfSeq, which is the source of truth the snapshot caches. It inserts 10,000
// entries across many batches, splits them over a handful of accounts, and after each batch upserts
// the batch's per-account delta into the snapshot. At the end the cached amount_cp and entry_count for
// every account must match the fold computed in Go.
//
// The additive upsert (amount_cp += excluded, entry_count += excluded) is the property that lets
// /standings read one indexed row instead of summing the ledger; getting it wrong (a replace instead
// of an add, or the wrong PK) is exactly the drift this test exists to catch.
func TestSnapshot_TenThousandEntries_MatchesFold(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	poolID := ledger.DefaultPoolID
	const kind = "dkp"

	// A small fixed set of accounts, so the snapshot has real running totals to accumulate rather than
	// 10,000 rows of one entry each. The system accounts are handy, addressable, and already seeded.
	accounts := []core.ULID{
		ledger.AccountIDGuildBank,
		ledger.AccountIDResidue,
		ledger.AccountIDWriteOff,
		ledger.AccountIDImportOpening,
	}

	const (
		totalEntries    = 10_000
		entriesPerBatch = 50
	)

	fold := make(map[core.ULID]int64)   // account -> summed amount_cp
	counts := make(map[core.ULID]int64) // account -> entry count

	entry := 0
	seq := int64(0)

	for entry < totalEntries {
		seq++

		batchDelta := make(map[string]int64)   // account -> this batch's summed amount
		batchCounts := make(map[string]int64)  // account -> this batch's entry count
		batchEntries := make(map[string]int64) // account -> a single merged entry amount to insert

		for i := 0; i < entriesPerBatch && entry < totalEntries; i++ {
			acct := accounts[entry%len(accounts)]
			// A varied, non-zero amount. Alternating sign so totals are not monotonic.
			amt := int64((entry%7)+1) * 10
			if entry%2 == 0 {
				amt = -amt
			}
			// amount_cp <> 0 is a CHECK; the +1 above guarantees it.

			batchDelta[acct.String()] += amt
			batchCounts[acct.String()]++
			batchEntries[acct.String()] += amt

			fold[acct] += amt
			counts[acct]++
			entry++
		}

		// Insert one merged entry per account for this batch (the fold does not care how many physical
		// rows carry the amount, only that the sum matches; merging keeps the row count sane while
		// still exercising 10k logical entries and the additive upsert per batch).
		insertMergedBatch(t, s, poolID.String(), seq, batchEntries)

		// Upsert each account's delta for this batch, additively, in the same logical step a commit
		// service would. entry_count is the number of logical entries this batch contributed.
		for acctStr, delta := range batchDelta {
			err := ledger.UpsertBalanceSnapshot(t.Context(), s.Q(), ledger.SnapshotDelta{
				PoolID:      poolID,
				AccountID:   core.ULID(acctStr),
				BalanceKind: kind,
				AmountCp:    core.Centipoints(delta),
				AsOfSeq:     seq,
				EntryCount:  batchCounts[acctStr],
				UpdatedAt:   core.Micros(1_704_067_200_000_000 + seq),
			})
			require.NoError(t, err)
		}
	}

	// The snapshot must equal the fold, per account, for both the amount and the count.
	for _, acct := range accounts {
		amount, count := readSnapshot(t, s, poolID.String(), acct.String(), kind)
		require.Equal(t, fold[acct], amount,
			"snapshot amount for %s must equal the Go fold", acct)
		require.Equal(t, counts[acct], count,
			"snapshot entry_count for %s must equal the number of entries folded", acct)

		// And the snapshot must equal BalanceAsOfSeq — the source of truth it caches.
		bal, err := ledger.BalanceAsOfSeq(t.Context(), s.Q(), poolID, acct, kind, seq)
		require.NoError(t, err)
		require.Equal(t, core.Centipoints(fold[acct]), bal,
			"the ledger sum for %s must equal the fold (and thus the snapshot)", acct)
	}
}

// insertMergedBatch inserts one batch and one entry per account, through store.ExecForTest. entries
// maps account -> the entry's amount (already summed for this batch). Every amount is non-zero by
// construction of the caller.
func insertMergedBatch(tb testing.TB, s *store.Store, poolID string, seq int64, entries map[string]int64) {
	tb.Helper()

	batchID := core.ULID(padID("SNAPB", seq))
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
	for acct, amt := range entries {
		i++
		entryID := core.ULID(padID("SNAPE", seq*1000+i))
		s.ExecForTest(tb,
			`INSERT INTO ledger_entry (id, batch_id, pool_id, seq, account_id, balance_kind, amount_cp)
			 VALUES (?, ?, ?, ?, ?, 'dkp', ?)`,
			entryID.String(), batchID.String(), poolID, seq, acct, amt)
	}
}

// readSnapshot returns the cached amount_cp and entry_count for one (pool, account, kind).
func readSnapshot(tb testing.TB, s *store.Store, poolID, accountID, kind string) (amount, count int64) {
	tb.Helper()

	require.NoError(tb,
		s.QueryRowForTest(tb,
			`SELECT amount_cp, entry_count FROM balance_snapshot
			 WHERE pool_id = ? AND account_id = ? AND balance_kind = ?`,
			poolID, accountID, kind).Scan(&amount, &count),
		"snapshot row for %s must exist", accountID)

	return amount, count
}
