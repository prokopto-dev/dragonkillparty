package ledger_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The negative fixtures for `dkp verify-ledger`. Phase 1, issue #198.
//
// A VERIFIER NOBODY HAS SEEN GO RED IS A VERIFIER NOBODY KNOWS WORKS, and the failure mode is
// specific and quiet: a replay that reads nothing, or that recomputes a hash from the same struct it
// just built and compares it with itself, reports a clean ledger for ever. So every check
// internal/ledger/verify.go claims to perform has a case here that makes it fire, and every case
// asserts the EXACT SET of finding kinds — not "contains" — because a fixture that produced its
// intended finding plus three unintended ones would be evidence about a different bug.
//
// THE LEDGER IS CORRUPTED AT WRITE TIME, not afterwards, and that is forced rather than chosen: the
// append-only triggers abort an UPDATE or a DELETE on ledger_batch, ledger_entry and audit_log
// (TestTriggers_MutatingLedger_Raises), so a test cannot edit a row into a bad state even to prove a
// detector works. The chain builder below therefore assembles the rows in Go — computing the real
// hashes with the package's own exported ledger.BatchHash and ledger.AuditHash — with two hook
// points, and only then inserts them:
//
//	beforeSeal   edits the rows BEFORE the chain is hashed over them, so the result is a ledger
//	             that is internally consistent apart from the one thing under test. This is what a
//	             half-restored backup or a botched table rebuild produces.
//	afterSeal    edits them AFTER, so the content and its attestation disagree. This is what an
//	             EDITED row looks like, and it is the case the hash chain exists for.
//
// balance_snapshot and dkp_meta are not append-only — the first is derived and the second is mutable
// instance state — so their fixtures are ordinary writes, which is also how their real drift
// arrives.

// verifyChain is a hand-built ledger for one pool: batches, their entries, the audit rows, the
// cached balances the entries imply, and the two chain heads. Everything in it is consistent until a
// test says otherwise.
type verifyChain struct {
	poolID   core.ULID
	accounts []core.ULID

	batches []ledger.BatchRow
	entries [][]ledger.EntryRow // parallel to batches
	audit   []ledger.AuditRow

	// snaps is what balance_snapshot should hold, folded from entries at seal time and keyed the way
	// the table is.
	snaps map[verifySnapKey]verifySnapRow

	// heads is dkp_meta, keyed by the real key ('ledger_head:<pool>', 'audit_head').
	heads map[string]string

	// forcePrev overrides a batch's prev_hash at seal time, for the fixture that breaks a LINK
	// rather than a row: the batch is then hashed over the wrong prev, so its own hash still
	// matches its own columns and exactly one finding fires.
	forcePrev map[int][]byte
}

// verifySnapKey and verifySnapRow are one cached balance, in the test's own shapes so a fixture can
// perturb a single column of a single row.
type verifySnapKey struct {
	accountID   core.ULID
	balanceKind string
}

type verifySnapRow struct {
	amountCp   int64
	asOfSeq    int64
	entryCount int64
}

// verifyBalanceKind is the balance kind every fixture writes. One kind, because the fixtures are
// about the chain and the cache rather than about multi-kind arithmetic, which balance_test.go
// covers.
const verifyBalanceKind = "dkp"

// newVerifyChain builds a consistent three-batch ledger over two accounts, unsealed.
//
// Three batches rather than one, because two of the properties under test only exist between
// batches: prev_hash links the second to the first, and a seq gap needs something to be a gap
// between. Two accounts, because a fold that is right for one account and wrong for the other is
// exactly the drift a per-row comparison exists to catch.
func newVerifyChain(tb testing.TB, poolID core.ULID, accounts []core.ULID) *verifyChain {
	tb.Helper()

	require.Len(tb, accounts, 2, "the fixture's arithmetic below assumes exactly two accounts")

	c := &verifyChain{
		poolID:    poolID,
		accounts:  accounts,
		heads:     map[string]string{},
		forcePrev: map[int][]byte{},
	}

	const batches = 3

	for i := range batches {
		seq := int64(i + 1)
		// A debit on one account and a credit on the other, alternating, so no account's running
		// total is monotonic and every batch is a legal zero-sum award.
		amount := core.Centipoints(100 * (seq + 1))
		payer, payee := accounts[i%2], accounts[(i+1)%2]

		entries := []ledger.EntryRow{
			c.entry(seq, 1, payer, -amount),
			c.entry(seq, 2, payee, amount),
		}

		c.batches = append(c.batches, ledger.BatchRow{
			ID:                 core.ULID(padID("VBAT", seq)),
			PoolID:             poolID,
			Seq:                seq,
			Kind:               kinds.KindAward,
			StrategyID:         "zero_sum",
			StrategyVersion:    "0.0.0",
			ConfigSnapshotJSON: "{}",
			Source:             kinds.SourceSystem,
			ActorIsBeneficiary: 0,
			Reason:             fmt.Sprintf("fixture batch %d", seq),
			EffectiveAt:        core.FromTime(fixedNow),
			RecordedAt:         core.FromTime(fixedNow),
			EffectiveDay:       "2024-06-01",
			EntryCount:         int64(len(entries)),
			NetAmountCp:        0,
		})
		c.entries = append(c.entries, entries)

		c.audit = append(c.audit, ledger.AuditRow{
			ID:           core.ULID(padID("VAUD", seq)),
			Seq:          seq,
			At:           core.FromTime(fixedNow),
			ActorKind:    auditkinds.ActorSystem,
			ActorLabel:   "fixture",
			Action:       ledger.DefaultAuditAction,
			ResourceKind: "ledger_batch",
			Outcome:      auditkinds.OutcomeSuccess,
		})
	}

	return c
}

// entry builds one entry of a batch. n distinguishes the entries within a batch, and the id is
// fixed-width so id order — which is the order the batch hash is computed in — is stable.
func (c *verifyChain) entry(seq, n int64, account core.ULID, amount core.Centipoints) ledger.EntryRow {
	return ledger.EntryRow{
		ID:           core.ULID(padID("VENT", seq*10+n)),
		BatchID:      core.ULID(padID("VBAT", seq)),
		PoolID:       c.poolID,
		Seq:          seq,
		AccountID:    account,
		BalanceKind:  verifyBalanceKind,
		AmountCp:     amount,
		MetadataJSON: "{}",
	}
}

// seal computes everything derived: the prev_hash links, every batch and audit hash, the cached
// balances the entries fold to, and the two chain heads.
//
// It runs AFTER a fixture's beforeSeal hook, so a perturbed row is attested as perturbed — which is
// what makes a link fixture produce a link finding and nothing else.
func (c *verifyChain) seal(tb testing.TB) {
	tb.Helper()

	var prev []byte

	for i := range c.batches {
		if forced, ok := c.forcePrev[i]; ok {
			c.batches[i].PrevHash = forced
		} else {
			c.batches[i].PrevHash = prev
		}

		hash, err := ledger.BatchHash(c.batches[i].PrevHash, c.batches[i], c.entries[i])
		require.NoError(tb, err)

		c.batches[i].Hash = hash
		prev = hash
	}

	if len(c.batches) > 0 {
		c.heads[ledger.MetaLedgerHeadKey(c.poolID)] = hex.EncodeToString(prev)
	}

	prev = nil

	for i := range c.audit {
		c.audit[i].PrevHash = prev

		hash, err := ledger.AuditHash(prev, c.audit[i])
		require.NoError(tb, err)

		c.audit[i].Hash = hash
		prev = hash
	}

	if len(c.audit) > 0 {
		c.heads[ledger.MetaAuditHeadKey()] = hex.EncodeToString(prev)
	}

	c.snaps = map[verifySnapKey]verifySnapRow{}

	for _, entries := range c.entries {
		for _, e := range entries {
			k := verifySnapKey{accountID: e.AccountID, balanceKind: e.BalanceKind}

			row := c.snaps[k]
			row.amountCp += int64(e.AmountCp)
			row.entryCount++
			row.asOfSeq = max(row.asOfSeq, e.Seq)
			c.snaps[k] = row
		}
	}
}

// write inserts the whole fixture. Every column is named explicitly, for the reason the product's
// own insert does: a value the database defaulted is a value the hash did not cover.
func (c *verifyChain) write(tb testing.TB, s *store.Store) {
	tb.Helper()

	for i, b := range c.batches {
		s.ExecForTest(tb,
			`INSERT INTO ledger_batch
			   (id, pool_id, seq, kind, strategy_id, strategy_version, config_snapshot_json, rng_seed,
			    source, source_ref, actor_user_id, actor_token_id, actor_is_beneficiary, reason,
			    reverses_batch_id, effective_at, recorded_at, effective_day, idempotency_key,
			    entry_count, net_amount_cp, prev_hash, hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, NULL, NULL, NULL, ?, ?, NULL, ?, ?, ?, NULL, ?, ?, ?, ?)`,
			b.ID.String(), b.PoolID.String(), b.Seq, b.Kind, b.StrategyID, b.StrategyVersion,
			b.ConfigSnapshotJSON, b.Source, b.ActorIsBeneficiary, b.Reason,
			int64(b.EffectiveAt), int64(b.RecordedAt), b.EffectiveDay,
			b.EntryCount, int64(b.NetAmountCp), b.PrevHash, b.Hash)

		for _, e := range c.entries[i] {
			s.ExecForTest(tb,
				`INSERT INTO ledger_entry
				   (id, batch_id, pool_id, seq, account_id, character_id, balance_kind, amount_cp,
				    item_id, item_award_id, raid_id, tick_id, metadata_json)
				 VALUES (?, ?, ?, ?, ?, NULL, ?, ?, NULL, NULL, NULL, NULL, ?)`,
				e.ID.String(), e.BatchID.String(), e.PoolID.String(), e.Seq, e.AccountID.String(),
				e.BalanceKind, int64(e.AmountCp), e.MetadataJSON)
		}
	}

	for _, a := range c.audit {
		s.ExecForTest(tb,
			`INSERT INTO audit_log
			   (id, seq, at, actor_kind, actor_label, action, resource_kind, resource_id,
			    outcome, ledger_batch_id, prev_hash, hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, NULL, ?, ?)`,
			a.ID.String(), a.Seq, int64(a.At), a.ActorKind, a.ActorLabel, a.Action,
			a.ResourceKind, a.Outcome, a.PrevHash, a.Hash)
	}

	for k, row := range c.snaps {
		s.ExecForTest(tb,
			`INSERT INTO balance_snapshot
			   (pool_id, account_id, balance_kind, amount_cp, as_of_seq, entry_count, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.poolID.String(), k.accountID.String(), k.balanceKind,
			row.amountCp, row.asOfSeq, row.entryCount, int64(core.FromTime(fixedNow)))
	}

	for key, value := range c.heads {
		s.ExecForTest(tb,
			`INSERT INTO dkp_meta (key, value, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, int64(core.FromTime(fixedNow)))
	}
}

// setSeq moves a batch and its entries to a new seq together. The seq is denormalised onto every
// entry, so moving one without the other would produce a denormalisation finding on top of whatever
// the fixture was actually testing.
func (c *verifyChain) setSeq(i int, seq int64) {
	c.batches[i].Seq = seq
	for j := range c.entries[i] {
		c.entries[i][j].Seq = seq
	}
}

// buildVerifyFixture writes a fixture into a fresh database and returns it.
func buildVerifyFixture(tb testing.TB, beforeSeal, afterSeal func(*verifyChain)) *store.Store {
	tb.Helper()

	s := store.NewDB(tb)
	accounts := seedPersonAccounts(tb, s, 2)

	c := newVerifyChain(tb, ledger.DefaultPoolID, accounts)

	if beforeSeal != nil {
		beforeSeal(c)
	}

	c.seal(tb)

	if afterSeal != nil {
		afterSeal(c)
	}

	c.write(tb, s)

	return s
}

// findingKinds returns the kinds a report holds, in report order.
func findingKinds(r ledger.Report) []ledger.FindingKind {
	kinds := make([]ledger.FindingKind, len(r.Findings))
	for i, f := range r.Findings {
		kinds[i] = f.Kind
	}

	return kinds
}

// TestVerify_ConsistentLedger_IsClean is the control every case below depends on.
//
// Without it, a fixture that produced its expected finding would prove nothing: the builder could be
// writing a ledger that fails verification for reasons of its own, and every "this fixture is
// detected" assertion would be reading the builder's bugs rather than the tamper's. It also asserts
// the COUNTS, because a verifier that read nothing is clean too.
func TestVerify_ConsistentLedger_IsClean(t *testing.T) {
	t.Parallel()

	s := buildVerifyFixture(t, nil, nil)

	report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{})
	require.NoError(t, err)
	require.True(t, report.Clean(), "a consistent fixture must verify clean: %v", report.Findings)
	require.False(t, report.Truncated())

	require.Len(t, report.Pools, 1)
	require.Equal(t, ledger.DefaultPoolID, report.Pools[0].PoolID)
	require.Equal(t, int64(3), report.Batches(), "every batch was walked")
	require.Equal(t, int64(6), report.Entries(), "every entry was folded")
	require.Equal(t, int64(2), report.Snapshots(), "both cached balances were compared")
	require.Equal(t, int64(3), report.Audit.Rows, "every audit row was walked")
	require.Equal(t, int64(3), report.Pools[0].HeadSeq)
	require.Len(t, report.Pools[0].Head, 2*32, "the pool head is a hex SHA-256")
	require.Len(t, report.Audit.Head, 2*32, "the audit head is a hex SHA-256")
}

// TestVerify_CorruptedLedger_NamesTheExactFailure is the negative-fixture table: one case per check
// verify.go performs, each asserting the exact set of findings and the location of the first one.
func TestVerify_CorruptedLedger_NamesTheExactFailure(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		beforeSeal func(*verifyChain)
		afterSeal  func(*verifyChain)
		want       []ledger.FindingKind
		detail     string
	}{
		{
			name: "an edited batch no longer hashes to its stored hash",
			// The canonical tamper: a row somebody changed. The chain is otherwise intact — the
			// links still hold and the head still matches the stored hash — so the ONLY thing that
			// says the ledger was touched is the recomputation, which is the whole argument for
			// having a hash chain at all.
			afterSeal: func(c *verifyChain) { c.batches[1].Reason = "adjusted after the fact" },
			want:      []ledger.FindingKind{ledger.FindingBatchHashMismatch},
			detail:    "the rows hash to",
		},
		{
			name:      "an edited entry no longer hashes to its batch's stored hash",
			afterSeal: func(c *verifyChain) { c.entries[2][0].MetadataJSON = `{"note":"edited"}` },
			want:      []ledger.FindingKind{ledger.FindingBatchHashMismatch},
			detail:    "stored hash is",
		},
		{
			name: "a broken prev_hash link is reported once, not as a broken hash",
			// The batch IS hashed over the wrong prev, so its own hash matches its own columns.
			// That separation matters: "this row was edited" and "this row is no longer linked to
			// the one before it" are different events with different causes.
			beforeSeal: func(c *verifyChain) { c.forcePrev[1] = make([]byte, 32) },
			want:       []ledger.FindingKind{ledger.FindingPrevHashMismatch},
			detail:     "the previous batch's hash is",
		},
		{
			name:       "a missing batch leaves a gap in the pool's seq",
			beforeSeal: func(c *verifyChain) { c.setSeq(2, 5) },
			want:       []ledger.FindingKind{ledger.FindingSeqGap},
			detail:     "expected seq 3, found 5",
		},
		{
			name:       "entry_count disagreeing with the entries is a finding",
			beforeSeal: func(c *verifyChain) { c.batches[0].EntryCount = 7 },
			want:       []ledger.FindingKind{ledger.FindingEntryCountMismatch},
			detail:     "entry_count is 7, the batch has 2 entries",
		},
		{
			name: "net_amount_cp disagreeing with the entries is a finding",
			// The cheap invariant the column exists for: a zero-sum batch that minted a centipoint
			// has a non-zero net even though it committed.
			beforeSeal: func(c *verifyChain) { c.batches[0].NetAmountCp = 1 },
			want:       []ledger.FindingKind{ledger.FindingNetAmountMismatch},
			detail:     "net_amount_cp is 1, the entries sum to 0",
		},
		{
			name: "entries that sum past int64 are a finding, not a wrapped total",
			// The overflow branch, and it matters because of what the alternative would report. A
			// wrapped sum is an ordinary-looking int64, so a batch whose entries overflow would be
			// compared against its stored net_amount_cp as though both were real numbers — and two
			// wrong numbers agree often enough to be worth the branch. The commit path's
			// NoAmountOverflow invariant is what stops this being written in the first place; this
			// is the replay noticing that something wrote it anyway.
			beforeSeal: func(c *verifyChain) {
				c.entries[0][0].AmountCp = math.MaxInt64
				c.entries[0][1].AmountCp = 1
			},
			want:   []ledger.FindingKind{ledger.FindingNetAmountMismatch},
			detail: "more than int64 can hold",
		},
		{
			name: "an entry whose denormalised seq is not its batch's is a finding",
			// pool_id and seq on the entry are what let BalanceAsOfSeq answer from ix_entry_balance
			// with no join. A disagreement means the balance index and the batch tell different
			// stories, and the index is what every balance in the product is read from.
			beforeSeal: func(c *verifyChain) { c.entries[1][1].Seq = 99 },
			want:       []ledger.FindingKind{ledger.FindingEntryDenormMismatch},
			detail:     "its batch is pool",
		},
		{
			name:      "a ledger head that is not the last batch's hash is a finding",
			afterSeal: func(c *verifyChain) { c.heads[ledger.MetaLedgerHeadKey(c.poolID)] = strings.Repeat("ab", 32) },
			want:      []ledger.FindingKind{ledger.FindingLedgerHeadMismatch},
			detail:    "the chain ends at",
		},
		{
			name:      "a missing ledger head is a finding",
			afterSeal: func(c *verifyChain) { delete(c.heads, ledger.MetaLedgerHeadKey(c.poolID)) },
			want:      []ledger.FindingKind{ledger.FindingLedgerHeadMismatch},
			detail:    "no ledger_head:",
		},
		{
			name: "a drifted cached balance is a finding",
			// One centipoint. The cache is not allowed to be nearly right: this is the number a
			// member reads off the standings page and can disprove from their own statement.
			afterSeal: func(c *verifyChain) { c.bumpSnapshot(0, 1, 0, 0) },
			want:      []ledger.FindingKind{ledger.FindingSnapshotAmountMismatch},
			detail:    "the log sums to",
		},
		{
			name:      "a drifted cached entry_count is a finding",
			afterSeal: func(c *verifyChain) { c.bumpSnapshot(0, 0, 0, 1) },
			want:      []ledger.FindingKind{ledger.FindingSnapshotEntryCountMismatch},
			detail:    "the log holds 3 entries",
		},
		{
			name: "a cached balance that stopped advancing its as_of_seq is a finding",
			// The subtle one, and the reason three columns are compared rather than one: a cache
			// that stopped updating still holds a correct-looking total for every account nobody
			// has awarded since.
			afterSeal: func(c *verifyChain) { c.bumpSnapshot(0, 0, -1, 0) },
			want:      []ledger.FindingKind{ledger.FindingSnapshotAsOfSeqMismatch},
			detail:    "the last seq to touch this balance is",
		},
		{
			name:      "an account with entries and no cached balance is a finding",
			afterSeal: func(c *verifyChain) { c.dropSnapshot(0) },
			want:      []ledger.FindingKind{ledger.FindingSnapshotMissing},
			detail:    "no cached balance; the log holds",
		},
		{
			name: "a cached balance with no entries behind it is a finding",
			// The other direction. A cache is not only allowed to be missing rows — it can hold a
			// balance the log does not support at all, which is what a partial restore produces.
			afterSeal: func(c *verifyChain) {
				c.snaps[verifySnapKey{accountID: ledger.AccountIDGuildBank, balanceKind: verifyBalanceKind}] = verifySnapRow{amountCp: 4200, asOfSeq: 3, entryCount: 1}
			},
			want:   []ledger.FindingKind{ledger.FindingSnapshotOrphan},
			detail: "the log has no entries for it",
		},
		{
			name:      "an edited audit row no longer hashes to its stored hash",
			afterSeal: func(c *verifyChain) { c.audit[1].ActorLabel = "somebody else" },
			want:      []ledger.FindingKind{ledger.FindingAuditHashMismatch},
			detail:    "the row hashes to",
		},
		{
			name:       "a broken audit prev_hash link is a finding",
			beforeSeal: func(c *verifyChain) { c.audit = c.audit[:1] },
			afterSeal:  func(c *verifyChain) { c.audit[0].PrevHash = make([]byte, 32) },
			want: []ledger.FindingKind{
				// The hash goes too, and that is correct rather than noise: prev_hash is an input to
				// the hash, so a row whose link was rewritten after sealing no longer hashes to
				// what is stored either. The fixture that separates the two is the LEDGER link case
				// above, which rewrites the link before sealing.
				ledger.FindingAuditPrevHashMismatch,
				ledger.FindingAuditHashMismatch,
			},
			detail: "the previous row's hash is",
		},
		{
			name:       "a missing audit row leaves a gap in the instance-wide seq",
			beforeSeal: func(c *verifyChain) { c.audit[2].Seq = 9 },
			want:       []ledger.FindingKind{ledger.FindingAuditSeqGap},
			detail:     "expected seq 3, found 9",
		},
		{
			name:      "an audit head that is not the last row's hash is a finding",
			afterSeal: func(c *verifyChain) { c.heads[ledger.MetaAuditHeadKey()] = strings.Repeat("cd", 32) },
			want:      []ledger.FindingKind{ledger.FindingAuditHeadMismatch},
			detail:    "the chain ends at",
		},
		{
			name: "an audit head recorded for a chain with no rows is a finding",
			beforeSeal: func(c *verifyChain) {
				c.audit = nil
			},
			afterSeal: func(c *verifyChain) { c.heads[ledger.MetaAuditHeadKey()] = strings.Repeat("ef", 32) },
			want:      []ledger.FindingKind{ledger.FindingAuditHeadMismatch},
			detail:    "but the chain has no rows",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := buildVerifyFixture(t, tc.beforeSeal, tc.afterSeal)

			report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{})
			require.NoError(t, err, "a corrupted ledger is a finding, never an error")

			require.False(t, report.Clean(), "the fixture must not verify clean")
			require.Equal(t, tc.want, findingKinds(report),
				"the report must hold exactly the expected findings, in order:\n%s",
				renderFindings(report))
			require.Equal(t, int64(len(tc.want)), report.FindingCount)
			require.Contains(t, report.Findings[0].Detail, tc.detail,
				"the finding must name the two values that disagree")
			require.Contains(t, report.Findings[0].String(), string(tc.want[0]),
				"a rendered finding names its kind")
		})
	}
}

// bumpSnapshot adds deltas to one cached balance, identified by its index in account order. It is
// how a fixture makes the CACHE wrong while leaving the log alone — which is the drift the nightly
// job exists to find, and the only kind that is repairable without a reversal.
func (c *verifyChain) bumpSnapshot(account int, amount, asOfSeq, entryCount int64) {
	k := verifySnapKey{accountID: c.accounts[account], balanceKind: verifyBalanceKind}

	row := c.snaps[k]
	row.amountCp += amount
	row.asOfSeq += asOfSeq
	row.entryCount += entryCount
	c.snaps[k] = row
}

// dropSnapshot removes one cached balance entirely.
func (c *verifyChain) dropSnapshot(account int) {
	delete(c.snaps, verifySnapKey{accountID: c.accounts[account], balanceKind: verifyBalanceKind})
}

// renderFindings is the failure message's body: every finding, one per line, as the CLI prints them.
func renderFindings(r ledger.Report) string {
	var b strings.Builder

	for _, f := range r.Findings {
		b.WriteString("  " + f.String() + "\n")
	}

	return b.String()
}

// TestVerify_SecondPool_IsBothWalkedAndReportedSeparately covers the multi-pool case.
//
// The ledger hash chain is PER POOL, so a verifier that walked only the default pool would report a
// clean ledger while another pool's chain was broken — and it would do so on precisely the instance
// that had grown past one pool, which is to say the one with the most history to lose.
func TestVerify_SecondPool_IsBothWalkedAndReportedSeparately(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	accounts := seedPersonAccounts(t, s, 2)

	second := core.ULID(padID("POOL", 2))
	s.ExecForTest(t,
		`INSERT INTO pool (id, name, name_norm, strategy_id, strategy_version, balance_kinds,
		                   created_at, updated_at)
		 VALUES (?, 'Second', 'second', 'zero_sum', '0.0.0', 'dkp', ?, ?)`,
		second.String(), int64(core.FromTime(fixedNow)), int64(core.FromTime(fixedNow)))

	// The default pool is consistent; the second one has a drifted cache.
	first := newVerifyChain(t, ledger.DefaultPoolID, accounts)
	first.seal(t)
	first.write(t, s)

	other := newVerifyChain(t, second, accounts)
	// Distinct ids, so the two pools' rows do not collide on the primary key.
	for i := range other.batches {
		other.batches[i].ID = core.ULID(padID("WBAT", other.batches[i].Seq))
		for j := range other.entries[i] {
			other.entries[i][j].ID = core.ULID(padID("WENT", other.batches[i].Seq*10+int64(j)))
			other.entries[i][j].BatchID = other.batches[i].ID
		}
	}

	// The audit chain is instance-wide, so the second pool contributes none: the first fixture
	// already wrote it, and a second copy would be a duplicate seq rather than a longer chain.
	other.audit = nil
	other.seal(t)
	other.bumpSnapshot(1, -50, 0, 0)
	other.write(t, s)

	report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{})
	require.NoError(t, err)

	require.Len(t, report.Pools, 2, "both pools are walked")
	require.Equal(t, int64(6), report.Batches(), "three batches in each pool")

	require.Equal(t, []ledger.FindingKind{ledger.FindingSnapshotAmountMismatch}, findingKinds(report))
	require.Equal(t, second, report.Findings[0].PoolID,
		"the finding must name the pool it is in, or an operator cannot act on it")

	// And the two pools' heads differ, which is the property that makes a per-pool chain a per-pool
	// chain rather than one chain read twice.
	heads := []string{report.Pools[0].Head, report.Pools[1].Head}
	require.NotEqual(t, heads[0], heads[1])
}

// TestVerify_EmptyLedger_IsCleanAndSaysSo covers the fresh-install case: a migrated database with a
// seeded pool, no batches and no audit rows.
//
// It is clean, and the counts say why. That distinction is the reason Report carries counts at all —
// "verified clean" over an empty database is a true statement about nothing, and an operator who has
// just restored the wrong file needs to be able to see which of the two they are looking at.
func TestVerify_EmptyLedger_IsCleanAndSaysSo(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{})
	require.NoError(t, err)
	require.True(t, report.Clean())

	require.Len(t, report.Pools, 1, "the migration seeds one pool")
	require.Zero(t, report.Batches())
	require.Zero(t, report.Entries())
	require.Zero(t, report.Snapshots())
	require.Zero(t, report.Audit.Rows)
	require.Empty(t, report.Pools[0].Head, "a pool with no batches has no chain head")
	require.Empty(t, report.Audit.Head)
}

// TestVerify_ManyFindings_AreCappedAndCounted covers the retention cap.
//
// The failure a replay finds is often systematic rather than isolated — a table rebuilt wrongly is
// one finding per account, a restore from the wrong file is one per batch — so the report caps what
// it RETAINS. What it must never cap is the count: a report that printed its cap and said nothing
// else would read as "there were three problems" on a ledger with three hundred.
func TestVerify_ManyFindings_AreCappedAndCounted(t *testing.T) {
	t.Parallel()

	// Every batch edited, so every batch's hash fails: three findings from three batches.
	s := buildVerifyFixture(t, nil, func(c *verifyChain) {
		for i := range c.batches {
			c.batches[i].Reason = "edited"
		}
	})

	report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{MaxFindings: 1})
	require.NoError(t, err)

	require.Len(t, report.Findings, 1, "only the cap is retained")
	require.Equal(t, int64(3), report.FindingCount, "every finding is still counted")
	require.True(t, report.Truncated())

	// And a negative cap retains all of them, which is what --max-findings=-1 asks for.
	all, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{MaxFindings: -1})
	require.NoError(t, err)
	require.Len(t, all.Findings, 3)
	require.False(t, all.Truncated())
}

// TestVerify_SmallPages_ReachTheSameVerdict is the paging property.
//
// The verifier pages every read so that its memory is proportional to the roster rather than to the
// log (a `:many` over 520,000 entries is 520,000 structs at once). A cursor that skipped or repeated
// a row would change the verdict, and at the default page size of 512 no fixture in this package is
// big enough to turn a page at all — so the paging is exercised by shrinking the page instead of by
// growing the ledger.
func TestVerify_SmallPages_ReachTheSameVerdict(t *testing.T) {
	t.Parallel()

	s := buildVerifyFixture(t, nil, func(c *verifyChain) { c.bumpSnapshot(1, 3, 0, 0) })

	for _, pageSize := range []int64{1, 2, 512} {
		t.Run(fmt.Sprintf("page size %d", pageSize), func(t *testing.T) {
			t.Parallel()

			report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{PageSize: pageSize})
			require.NoError(t, err)

			require.Equal(t, int64(3), report.Batches(), "every batch is walked exactly once")
			require.Equal(t, int64(6), report.Entries())
			require.Equal(t, int64(2), report.Snapshots())
			require.Equal(t, int64(3), report.Audit.Rows)
			require.Equal(t,
				[]ledger.FindingKind{ledger.FindingSnapshotAmountMismatch}, findingKinds(report))
		})
	}
}

// TestVerify_MissingSnapshots_AreReportedInADeterministicOrder covers the one place the report's
// order does not come from a query's ORDER BY.
//
// Accounts the cache has no row for are whatever is LEFT in the fold once every cached row has been
// matched off, and that is a Go map — whose iteration order Go randomises on purpose. Unsorted, this
// report would list the same findings in a different order on every run, which breaks two things
// that matter more than tidiness: an operator cannot diff last night's output against tonight's, and
// a report truncated at --max-findings would retain an arbitrary hundred rather than the same
// hundred. Two balance kinds on one account, because the tiebreak between them is the half of the
// ordering a single-kind fixture never reaches.
func TestVerify_MissingSnapshots_AreReportedInADeterministicOrder(t *testing.T) {
	t.Parallel()

	s := buildVerifyFixture(t,
		func(c *verifyChain) {
			// A second balance kind on both accounts, so the leftover set has four members whose
			// order needs both halves of the comparison to settle.
			c.entries[0] = append(c.entries[0],
				c.entry(1, 3, c.accounts[0], -70),
				c.entry(1, 4, c.accounts[1], 70))
			c.entries[0][2].BalanceKind = "ep"
			c.entries[0][3].BalanceKind = "ep"
			c.batches[0].EntryCount = int64(len(c.entries[0]))
		},
		func(c *verifyChain) { c.snaps = nil },
	)

	var first ledger.Report

	for run := range 5 {
		report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{})
		require.NoError(t, err)

		require.Equal(t, int64(4), report.FindingCount,
			"two accounts times two balance kinds, none of them cached")
		require.Equal(t, []ledger.FindingKind{
			ledger.FindingSnapshotMissing, ledger.FindingSnapshotMissing,
			ledger.FindingSnapshotMissing, ledger.FindingSnapshotMissing,
		}, findingKinds(report))

		require.True(t, slices.IsSortedFunc(report.Findings, func(a, b ledger.Finding) int {
			if a.AccountID != b.AccountID {
				return strings.Compare(a.AccountID.String(), b.AccountID.String())
			}

			return strings.Compare(a.BalanceKind, b.BalanceKind)
		}), "findings are ordered by account then balance kind")

		if run == 0 {
			first = report

			continue
		}

		require.Equal(t, first.Findings, report.Findings,
			"the same database must produce the same report every time it is replayed")
	}
}

// TestVerify_ReadFailure_IsAnErrorNotACleanReport is the distinction the whole exit code rests on.
//
// A replay that could not read must fail LOUDLY. The alternative — treating an unreadable table as
// an empty one — produces the single worst output this command could have: "ledger verified clean",
// exit 0, from a nightly job, over a database it never opened. Every read the verifier makes is
// covered here, each failed on its own by removing the table it needs, which is a real driver error
// at exactly one call site while every other read still works.
func TestVerify_ReadFailure_IsAnErrorNotACleanReport(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		drop string
	}{
		{name: "the pool list", drop: "pool"},
		{name: "the batch page", drop: "ledger_batch"},
		{name: "a batch's entries", drop: "ledger_entry"},
		{name: "the cached balances", drop: "balance_snapshot"},
		{name: "the audit page", drop: "audit_log"},
		{name: "the chain heads", drop: "dkp_meta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := buildVerifyFixture(t, nil, nil)

			// Dropped AFTER the fixture is written, so the schema is whole for everything except the
			// one read under test. Foreign keys are turned off first — and only for the drop — for a
			// reason that is a property of the fixture rather than of the verifier: the tables are
			// populated by this point, so SQLite's implicit delete would abort on the rows in
			// ledger_entry and balance_snapshot before the table went away, and the test would be
			// asserting that a DROP failed rather than that a read did. The pragma is per
			// connection, the write pool holds exactly one, and the database is this subtest's own.
			s.ExecForTest(t, `PRAGMA foreign_keys = OFF`)
			s.ExecForTest(t, `DROP TABLE `+tc.drop)

			report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{})

			if tc.drop == "dkp_meta" {
				// The ONE exception, and it is deliberate rather than an oversight. A head that
				// cannot be read is recorded as a head finding, because the chain rows themselves
				// were all read and verified — the report is still worth having, it just cannot
				// attest the heads. It is never CLEAN, which is the property that matters.
				require.NoError(t, err)
				require.False(t, report.Clean(), "an unreadable head is never a clean verdict")
				require.Equal(t, []ledger.FindingKind{
					ledger.FindingLedgerHeadMismatch, ledger.FindingAuditHeadMismatch,
				}, findingKinds(report))

				return
			}

			require.Error(t, err, "an unreadable %s must fail the replay, not empty it", tc.drop)
			require.True(t, report.Clean(),
				"a failed replay returns the zero report; a caller must read the error, not the verdict")
		})
	}
}

// TestVerify_CancelledContext_StopsTheReplay is the other read failure, and the one that actually
// happens: an operator pressing Ctrl-C, or a job's deadline expiring, part-way through a three-minute
// replay of half a million entries.
func TestVerify_CancelledContext_StopsTheReplay(t *testing.T) {
	t.Parallel()

	s := buildVerifyFixture(t, nil, nil)

	_, err := ledger.Verify(cancelledContext(t), s.Q(), ledger.VerifyOptions{})
	require.ErrorIs(t, err, context.Canceled)
}

// TestVerify_Progress_ReportsMonotonicCounts covers the callback a multi-minute replay needs.
func TestVerify_Progress_ReportsMonotonicCounts(t *testing.T) {
	t.Parallel()

	s := buildVerifyFixture(t, nil, nil)

	var seen []ledger.Progress

	report, err := ledger.Verify(t.Context(), s.Q(), ledger.VerifyOptions{
		PageSize: 1,
		Progress: func(p ledger.Progress) { seen = append(seen, p) },
	})
	require.NoError(t, err)
	require.True(t, report.Clean())

	require.Len(t, seen, 3, "one call per page of batches")
	require.True(t, slices.IsSortedFunc(seen, func(a, b ledger.Progress) int {
		return int(a.Batches - b.Batches)
	}), "progress counts never go backwards: %v", seen)
	require.Equal(t, ledger.DefaultPoolID, seen[0].PoolID)
	require.Equal(t, int64(3), seen[len(seen)-1].Batches)
	require.Equal(t, int64(6), seen[len(seen)-1].Entries)
}
