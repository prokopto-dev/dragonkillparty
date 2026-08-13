package ledger

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The replay. Phase 1, issue #198 — the engine behind `dkp verify-ledger` and the nightly job.
//
// It answers one question in two halves:
//
//	IS THE LOG WHAT IT SAYS IT IS?     Recompute every batch hash from the stored rows and every
//	                                   audit hash from the stored row, walk the prev_hash links, and
//	                                   compare the end of each chain against the head in dkp_meta.
//	IS THE CACHE STILL THE FOLD?       Sum every ledger_entry into (account, balance kind)
//	                                   accumulators and require balance_snapshot to equal them
//	                                   EXACTLY — amount, entry count and as-of seq — in BOTH
//	                                   directions.
//
// The second half is why this file exists at all. ADR-0023 measured the alternative — serving the
// standings page from the log is 10,412 pages against the cache's 13 — and concluded that
// balance_snapshot is LOAD-BEARING rather than droppable. There is no fallback that answers more
// slowly, so a drifted cache is not a degraded page, it is a wrong one, and this replay is the only
// thing that would ever notice. The ADR says so in its own consequences: "a job is now load-bearing
// … the job must fail loudly and visibly".
//
// So the comparison is per row, per column, in both directions. A cache is not "mostly right": one
// wrong account is one member being told a number they can disprove, and an extra cached row is as
// much a finding as a missing one.
//
// A DRIFT IS A FINDING; A BROKEN DATABASE IS AN ERROR. Verify returns an error only when it could
// not read — a cancelled context, a dropped table, an unparseable chain head. Everything it was able
// to read and found wrong comes back in Report.Findings, because those are two different things to
// an operator: one means "this tool cannot answer", the other means "here is the answer and it is
// bad". Collapsing them would make a database outage look like ledger corruption.
//
// WHAT IT DOES NOT DO. It does not write. The two ledger tables are append-only and the cache
// rebuild the operations docs describe (`dkp verify-ledger --rebuild`) is a write, so it is a
// separate job; this one is safe to run against a live instance because the worst it can do is read.
// Keeping the two apart is deliberate rather than incidental — a verifier that could repair what it
// found would be a verifier that could hide it, and the finding is the thing an operator must see.
// It also cannot detect a TOTAL, deliberate rewrite — an actor who edits rows and recomputes the
// chain leaves a self-consistent log, which is hashchain.go's stated limitation and is what
// published anchors (Phase 2) address. What a replay catches is the partial and the accidental: a
// half-restored backup, a migration that rebuilt a table wrongly, a cache that stopped tracking.
//
// THREE OF THE FOUR JOB RULES IN .claude/rules/decay-and-jobs.md §8 DO NOT BIND HERE, and it is
// worth naming which rather than looking like they were missed. "Commit in bounded chunks", "take a
// per-job lock per pool" and "be idempotent" are rules about the job that WRITES — the rebuild. This
// one opens no transaction, holds no write connection and mutates nothing, so there is nothing to
// chunk, nothing to serialise and nothing a retry could do twice. The fourth binds absolutely: fail
// loudly and visibly. An unreadable database stops the replay and exits non-zero, because a verifier
// that reported clean after a read it could not make is the worst output this code could produce.

// FindingKind names one way the ledger can fail to verify. It is a closed vocabulary rather than a
// free-text message so that a caller — the nightly job, `dkp doctor`, a future /ops panel — can
// count and group findings without parsing prose.
type FindingKind string

// The ledger-chain findings.
const (
	// FindingSeqGap is a pool whose batch seqs are not 1, 2, 3, … A gap means a batch is missing;
	// seq is what a balance is defined "as of", so a hole is not a cosmetic numbering problem.
	FindingSeqGap FindingKind = "seq_gap"

	// FindingPrevHashMismatch is a batch whose prev_hash is not the previous batch's hash — the link
	// itself, as distinct from the batch's own contents.
	FindingPrevHashMismatch FindingKind = "prev_hash_mismatch"

	// FindingBatchHashMismatch is a batch whose stored hash is not what its own columns and entries
	// hash to. This is the one that says the ROW was changed.
	FindingBatchHashMismatch FindingKind = "batch_hash_mismatch"

	// FindingEntryCountMismatch is a batch whose entry_count column disagrees with the number of
	// entries actually carrying its id.
	FindingEntryCountMismatch FindingKind = "entry_count_mismatch"

	// FindingNetAmountMismatch is a batch whose net_amount_cp column disagrees with the sum of its
	// entries. It is the cheap invariant the column exists for: a zero-sum award that minted a
	// centipoint has a non-zero net even though it committed.
	FindingNetAmountMismatch FindingKind = "net_amount_mismatch"

	// FindingEntryDenormMismatch is an entry whose denormalised pool_id or seq disagrees with its
	// batch's. Those two columns are what let BalanceAsOfSeq answer from ix_entry_balance with no
	// join, so a disagreement means the balance index and the batch tell different stories.
	FindingEntryDenormMismatch FindingKind = "entry_denorm_mismatch"

	// FindingLedgerHeadMismatch is a pool whose dkp_meta 'ledger_head:<pool>' is not the hash of its
	// last batch — including a head that is missing when the pool has batches, or present when it
	// has none.
	FindingLedgerHeadMismatch FindingKind = "ledger_head_mismatch"
)

// The balance_snapshot findings. Three columns are compared, not one: a cache that had folded the
// right total from the wrong number of entries, or that had stopped advancing its as-of seq, would
// pass a sum-only comparison and be wrong in a way a member eventually finds. These are the findings
// ADR-0023 raised the stakes on — the page has no other source for these numbers.
const (
	// FindingSnapshotAmountMismatch is a cached amount_cp that is not the sum of the account's
	// entries. This is the one a member sees.
	FindingSnapshotAmountMismatch FindingKind = "snapshot_amount_mismatch"

	// FindingSnapshotEntryCountMismatch is a cached entry_count that is not the number of entries
	// folded into that balance.
	FindingSnapshotEntryCountMismatch FindingKind = "snapshot_entry_count_mismatch"

	// FindingSnapshotAsOfSeqMismatch is a cached as_of_seq that is not the highest seq to have
	// touched the account. A stale as-of seq is how a cache that stopped updating still looks right.
	FindingSnapshotAsOfSeqMismatch FindingKind = "snapshot_as_of_seq_mismatch"

	// FindingSnapshotMissing is an account with entries in the log and no cached row. The standings
	// page would report that member as absent rather than as wrong, which is worse.
	FindingSnapshotMissing FindingKind = "snapshot_missing"

	// FindingSnapshotOrphan is a cached row with no entries behind it: a balance the log does not
	// support at all.
	FindingSnapshotOrphan FindingKind = "snapshot_orphan"
)

// The audit-chain findings. The audit chain is instance-wide and independent of the ledger's, so its
// findings are named separately — an operator has to be able to tell "somebody edited the money"
// from "somebody edited the record of who touched it".
const (
	// FindingAuditSeqGap is a hole in the audit log's instance-wide sequence. Gaplessness is what
	// gives the chain an ordering to hash over, and pruning writes a marker rather than a silence.
	FindingAuditSeqGap FindingKind = "audit_seq_gap"

	// FindingAuditPrevHashMismatch is an audit row whose prev_hash is not the previous row's hash.
	FindingAuditPrevHashMismatch FindingKind = "audit_prev_hash_mismatch"

	// FindingAuditHashMismatch is an audit row whose stored hash is not what its columns hash to.
	FindingAuditHashMismatch FindingKind = "audit_hash_mismatch"

	// FindingAuditHeadMismatch is a dkp_meta 'audit_head' that is not the hash of the last audit row.
	FindingAuditHeadMismatch FindingKind = "audit_head_mismatch"
)

// Finding is one detected problem, located precisely enough to act on: which pool, which seq, which
// account. The empty fields are the ones that do not apply — an audit finding has no pool, a chain
// finding has no account — and a caller renders what is set.
type Finding struct {
	Kind        FindingKind
	PoolID      core.ULID
	Seq         int64
	BatchID     core.ULID
	AccountID   core.ULID
	BalanceKind string

	// Detail is the human half: the two values that disagree, in the order want-then-got. It is
	// never the machine half — that is Kind — so a caller may reword it freely.
	Detail string
}

// String renders a finding as one line, which is what the CLI prints and what a nightly issue
// quotes. The location prefix is built from whichever fields are set.
func (f Finding) String() string {
	loc := ""

	switch {
	case f.AccountID != "":
		loc = fmt.Sprintf("pool %s account %s kind %q: ", f.PoolID, f.AccountID, f.BalanceKind)
	case f.PoolID != "":
		loc = fmt.Sprintf("pool %s seq %d: ", f.PoolID, f.Seq)
	case f.Seq != 0:
		loc = fmt.Sprintf("audit seq %d: ", f.Seq)
	}

	return fmt.Sprintf("%s%s%s", loc, f.Kind, ": "+f.Detail)
}

// PoolReport is what the replay saw in one pool. The counts are the evidence that the verification
// was not vacuous: a clean report over zero batches means something very different from a clean
// report over twenty thousand, and only one of them is reassuring.
type PoolReport struct {
	PoolID    core.ULID
	Batches   int64
	Entries   int64
	Snapshots int64

	// HeadSeq is the last seq walked, and Head is the hex chain head recomputed from the rows —
	// empty for a pool with no batches. Head is what a future anchor comparison publishes.
	HeadSeq int64
	Head    string
}

// AuditReport is the same for the instance-wide audit chain.
type AuditReport struct {
	Rows    int64
	HeadSeq int64
	Head    string
}

// Report is a whole verification: what was read, and what was wrong with it.
type Report struct {
	Pools    []PoolReport
	Audit    AuditReport
	Findings []Finding

	// FindingCount is how many problems were DETECTED, which is not always how many are in
	// Findings — see MaxFindings. A truncated report that reported only the length would understate
	// the damage by exactly the amount that mattered.
	FindingCount int64
}

// Clean reports that the replay found nothing wrong.
func (r Report) Clean() bool { return r.FindingCount == 0 }

// Truncated reports that more problems were found than were retained.
func (r Report) Truncated() bool { return r.FindingCount > int64(len(r.Findings)) }

// Batches, Entries and Snapshots total the per-pool counts, for a caller that wants the headline
// rather than the breakdown.
func (r Report) Batches() int64 {
	return sumPools(r.Pools, func(p PoolReport) int64 { return p.Batches })
}

func (r Report) Entries() int64 {
	return sumPools(r.Pools, func(p PoolReport) int64 { return p.Entries })
}

func (r Report) Snapshots() int64 {
	return sumPools(r.Pools, func(p PoolReport) int64 { return p.Snapshots })
}

// sumPools totals one field across the pools.
func sumPools(pools []PoolReport, field func(PoolReport) int64) int64 {
	var total int64
	for _, p := range pools {
		total += field(p)
	}

	return total
}

// VerifyOptions tunes a replay. The zero value is the correct default for every field.
type VerifyOptions struct {
	// PageSize is how many batches, snapshot rows or audit rows are read at a time. Zero means
	// DefaultVerifyPageSize.
	//
	// It is the knob that keeps the verifier's memory proportional to the ROSTER rather than to the
	// log: a 520,000-entry ledger is walked a page of batches at a time, and the only thing that
	// grows with the guild is one accumulator per (account, balance kind).
	PageSize int64

	// MaxFindings caps how many findings are RETAINED. Zero means DefaultVerifyMaxFindings; a
	// negative value retains all of them.
	//
	// A cap rather than an unbounded slice, because the failure this exists to catch can be
	// systematic: a migration that rebuilt ledger_entry wrongly produces one finding per account and
	// a restore from the wrong file produces one per batch. Report.FindingCount always counts every
	// one, so the cap costs detail and never costs the verdict.
	MaxFindings int

	// Progress is called once per page of batches, for a command that must not look hung during a
	// three-minute replay. Nil disables it. It is called from the goroutine that called Verify, so
	// an implementation that blocks stops the replay.
	Progress func(Progress)
}

// Progress is one report of how far a replay has got. There is no total: deriving one would mean
// trusting max(seq) to be the batch count, which is exactly what the replay is checking.
type Progress struct {
	PoolID  core.ULID
	Batches int64
	Entries int64
}

// The option defaults.
const (
	// DefaultVerifyPageSize is a page of batches. 512 batches is roughly 13,000 entries at guild
	// scale — big enough that the per-page round trip is noise against the per-batch entry reads,
	// small enough that a page is tens of megabytes rather than the whole ledger.
	DefaultVerifyPageSize = 512

	// DefaultVerifyMaxFindings is how many problems are printed before the report says "and N more".
	// A hundred lines is more than an operator will read and enough to see the shape of the damage.
	DefaultVerifyMaxFindings = 100
)

// Verify replays the whole ledger from genesis and returns what it found.
//
// It takes store.Queries rather than a *store.Store, so it reads the same whether it is handed the
// read pool (store.Q()) or a transaction's queries. The CLI hands it the read pool: the replay is
// read-only and holding the single write connection for three minutes would block every raid-night
// write for the duration, which is the trade .claude/rules/store-and-sql.md names for long jobs.
//
// Every pool is walked, not just the default one: the ledger chain is PER POOL, so verifying one
// pool would report a clean ledger while another pool's chain was broken.
func Verify(ctx context.Context, q store.Queries, opts VerifyOptions) (Report, error) {
	v := &verifier{
		q:           q,
		pageSize:    orDefaultInt64(opts.PageSize, DefaultVerifyPageSize),
		maxFindings: orDefaultInt(opts.MaxFindings, DefaultVerifyMaxFindings),
		progress:    opts.Progress,
	}

	poolIDs, err := q.ListPoolIDs(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list pools: %w", err)
	}

	for _, id := range poolIDs {
		if err := v.pool(ctx, core.ULID(id)); err != nil {
			return Report{}, err
		}
	}

	if err := v.auditChain(ctx); err != nil {
		return Report{}, err
	}

	return v.report, nil
}

// verifier carries the state one replay accumulates: the report being built, and the paging and
// retention limits. It exists so the per-pool and per-chain steps can be separate methods without
// threading six parameters through each of them.
type verifier struct {
	q           store.Queries
	pageSize    int64
	maxFindings int
	progress    func(Progress)
	report      Report
}

// find records one problem. Every finding goes through here, so the cap and the total count cannot
// diverge — a caller that appended to report.Findings directly would produce a report whose
// FindingCount was a lie.
func (v *verifier) find(f Finding) {
	v.report.FindingCount++

	if v.maxFindings >= 0 && len(v.report.Findings) >= v.maxFindings {
		return
	}

	v.report.Findings = append(v.report.Findings, f)
}

// snapKey identifies one cached balance: the key balance_snapshot is stored under, minus the pool,
// which is fixed for the duration of a pool's walk.
type snapKey struct {
	accountID   core.ULID
	balanceKind string
}

// foldAcc is one account's balance rebuilt from the log — the three columns balance_snapshot caches,
// recomputed from the entries that produced them.
//
// maxSeq is a MAX rather than a running "last seen", and the fold does not order by seq, because
// neither needs to: addition is commutative and so is max, so the accumulator is order-independent.
// The log's ORDERING is what the hash chain covers, and it is covered there rather than twice.
type foldAcc struct {
	amountCp   core.Centipoints
	entryCount int64
	maxSeq     int64
}

// pool walks one pool: its batch chain, its entries, and its cached balances.
//
// ONE PASS FOR BOTH HALVES. The chain check needs every entry of every batch in id order, and the
// fold needs every entry exactly once — so the entries are read once and used twice, rather than
// walked once for the hashes and again for the balances. At 520,000 entries the second walk would
// double the most expensive part of the job to recompute a number already in hand.
func (v *verifier) pool(ctx context.Context, poolID core.ULID) error {
	pr := PoolReport{PoolID: poolID}
	fold := make(map[snapKey]foldAcc)

	var (
		prev     []byte // the previous batch's hash; nil before the first
		wantSeq  = int64(1)
		cursor   = int64(0)
		lastHash []byte
	)

	for {
		batches, err := v.q.ListBatchesAfterSeq(ctx, sqlitegen.ListBatchesAfterSeqParams{
			PoolID: poolID.String(),
			Seq:    cursor,
			Limit:  v.pageSize,
		})
		if err != nil {
			return fmt.Errorf("list batches of pool %s after seq %d: %w", poolID, cursor, err)
		}

		if len(batches) == 0 {
			break
		}

		for _, row := range batches {
			batch := batchFromRow(row)

			entries, err := v.batchEntries(ctx, batch)
			if err != nil {
				return err
			}

			v.checkBatch(batch, entries, prev, wantSeq)
			v.foldEntries(fold, entries)

			pr.Batches++
			pr.Entries += int64(len(entries))
			pr.HeadSeq = batch.Seq

			prev, lastHash = batch.Hash, batch.Hash
			wantSeq = batch.Seq + 1
			cursor = batch.Seq
		}

		if v.progress != nil {
			v.progress(Progress{PoolID: poolID, Batches: pr.Batches, Entries: pr.Entries})
		}
	}

	if lastHash != nil {
		pr.Head = encodeHead(lastHash)
	}

	v.checkHead(ctx, MetaLedgerHeadKey(poolID), lastHash, Finding{
		Kind: FindingLedgerHeadMismatch, PoolID: poolID, Seq: pr.HeadSeq,
	})

	snapshots, err := v.checkSnapshots(ctx, poolID, fold)
	if err != nil {
		return err
	}

	pr.Snapshots = snapshots
	v.report.Pools = append(v.report.Pools, pr)

	return nil
}

// batchEntries reads one batch's entries. The rows come back in id order from the query, and are
// sorted again by BatchHash — the hash's input order is part of its definition and must not depend
// on the caller having read them in any particular order.
func (v *verifier) batchEntries(ctx context.Context, batch BatchRow) ([]EntryRow, error) {
	rows, err := v.q.ListEntriesByBatch(ctx, batch.ID.String())
	if err != nil {
		return nil, fmt.Errorf("list entries of batch %s in pool %s: %w", batch.ID, batch.PoolID, err)
	}

	entries := make([]EntryRow, len(rows))
	for i, r := range rows {
		entries[i] = entryFromRow(r)
	}

	return entries, nil
}

// checkBatch is the per-batch half of the chain verification: the link, the hash, and the two
// summary columns the batch carries about its own entries.
//
// It records findings and returns nothing. A batch that fails one check is still walked — the next
// batch's prev_hash is compared against this one's STORED hash, not against the recomputed one, so a
// single corrupted row produces one finding rather than one per batch after it.
func (v *verifier) checkBatch(batch BatchRow, entries []EntryRow, prev []byte, wantSeq int64) {
	at := Finding{PoolID: batch.PoolID, Seq: batch.Seq, BatchID: batch.ID}

	if batch.Seq != wantSeq {
		f := at
		f.Kind = FindingSeqGap
		f.Detail = fmt.Sprintf("expected seq %d, found %d — %d batch(es) missing",
			wantSeq, batch.Seq, batch.Seq-wantSeq)
		v.find(f)
	}

	if !bytes.Equal(batch.PrevHash, prev) {
		f := at
		f.Kind = FindingPrevHashMismatch
		f.Detail = fmt.Sprintf("prev_hash is %s, the previous batch's hash is %s",
			describeHash(batch.PrevHash), describeHash(prev))
		v.find(f)
	}

	// Hashed against the batch's OWN stored prev_hash, not against the previous batch's hash. The
	// two are different questions — "was this row edited" and "is it still linked to the one before
	// it" — and a batch whose prev_hash was rewritten should report a broken link once, not a broken
	// link and a broken hash.
	want, err := BatchHash(batch.PrevHash, batch, entries)
	if err != nil {
		// Unreachable in practice: the inputs are strings and integers read out of a STRICT table,
		// and encoding/json cannot fail on them. Recorded as a finding rather than swallowed,
		// because a hash that could not be computed is emphatically not a hash that matched.
		f := at
		f.Kind = FindingBatchHashMismatch
		f.Detail = fmt.Sprintf("recompute the hash: %v", err)
		v.find(f)
	} else if !bytes.Equal(want, batch.Hash) {
		f := at
		f.Kind = FindingBatchHashMismatch
		f.Detail = fmt.Sprintf("stored hash is %s, the rows hash to %s",
			describeHash(batch.Hash), describeHash(want))
		v.find(f)
	}

	if batch.EntryCount != int64(len(entries)) {
		f := at
		f.Kind = FindingEntryCountMismatch
		f.Detail = fmt.Sprintf("entry_count is %d, the batch has %d entries",
			batch.EntryCount, len(entries))
		v.find(f)
	}

	net, ok := sumEntries(entries)
	switch {
	case !ok:
		f := at
		f.Kind = FindingNetAmountMismatch
		f.Detail = "the entries sum to more than int64 can hold"
		v.find(f)
	case net != batch.NetAmountCp:
		f := at
		f.Kind = FindingNetAmountMismatch
		f.Detail = fmt.Sprintf("net_amount_cp is %d, the entries sum to %d", batch.NetAmountCp, net)
		v.find(f)
	}

	for _, e := range entries {
		if e.PoolID == batch.PoolID && e.Seq == batch.Seq {
			continue
		}

		f := at
		f.Kind = FindingEntryDenormMismatch
		f.AccountID = e.AccountID
		f.BalanceKind = e.BalanceKind
		f.Detail = fmt.Sprintf("entry %s carries pool %s seq %d, its batch is pool %s seq %d",
			e.ID, e.PoolID, e.Seq, batch.PoolID, batch.Seq)
		v.find(f)
	}
}

// foldEntries adds one batch's entries to the running per-account accumulators. This is the
// definitional balance — COALESCE(sum(amount_cp), 0) grouped by (account, balance kind) — computed
// in Go over the whole log, which is exactly what balance_snapshot claims to cache.
func (v *verifier) foldEntries(fold map[snapKey]foldAcc, entries []EntryRow) {
	for _, e := range entries {
		k := snapKey{accountID: e.AccountID, balanceKind: e.BalanceKind}

		acc := fold[k]
		acc.amountCp += e.AmountCp
		acc.entryCount++
		acc.maxSeq = max(acc.maxSeq, e.Seq)
		fold[k] = acc
	}
}

// checkSnapshots compares the fold against balance_snapshot in both directions and returns how many
// cached rows were read.
//
// The cache is paged and the fold is in memory, so the comparison is: for every cached row, look up
// the fold and compare three columns; delete what matched so that whatever REMAINS in the fold at
// the end is an account the cache has no row for. Two directions, one pass, and no second map.
func (v *verifier) checkSnapshots(
	ctx context.Context, poolID core.ULID, fold map[snapKey]foldAcc,
) (int64, error) {
	var (
		count  int64
		cursor snapKey
	)

	for {
		rows, err := v.q.ListSnapshotsAfter(ctx, sqlitegen.ListSnapshotsAfterParams{
			PoolID:            poolID.String(),
			CursorAccountID:   cursor.accountID.String(),
			CursorBalanceKind: cursor.balanceKind,
			PageLimit:         v.pageSize,
		})
		if err != nil {
			return 0, fmt.Errorf("list cached balances of pool %s after %s/%q: %w",
				poolID, cursor.accountID, cursor.balanceKind, err)
		}

		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			k := snapKey{accountID: core.ULID(row.AccountID), balanceKind: row.BalanceKind}

			v.checkSnapshotRow(poolID, k, row, fold)
			delete(fold, k)

			count++
			cursor = k
		}
	}

	// Whatever the cache had no row for. Sorted, because a map's iteration order is randomised and a
	// report whose lines moved between runs could not be diffed — and because a truncated report
	// must retain the same findings every time, not a hundred arbitrary ones.
	missing := make([]snapKey, 0, len(fold))
	for k := range fold {
		missing = append(missing, k)
	}

	slices.SortFunc(missing, func(a, b snapKey) int {
		if a.accountID != b.accountID {
			return cmp.Compare(a.accountID, b.accountID)
		}

		return cmp.Compare(a.balanceKind, b.balanceKind)
	})

	for _, k := range missing {
		acc := fold[k]
		v.find(Finding{
			Kind:        FindingSnapshotMissing,
			PoolID:      poolID,
			Seq:         acc.maxSeq,
			AccountID:   k.accountID,
			BalanceKind: k.balanceKind,
			Detail: fmt.Sprintf("no cached balance; the log holds %d entries summing to %d",
				acc.entryCount, acc.amountCp),
		})
	}

	return count, nil
}

// checkSnapshotRow compares one cached row against the fold, column by column.
func (v *verifier) checkSnapshotRow(
	poolID core.ULID, k snapKey, row sqlitegen.ListSnapshotsAfterRow, fold map[snapKey]foldAcc,
) {
	at := Finding{PoolID: poolID, AccountID: k.accountID, BalanceKind: k.balanceKind}

	acc, ok := fold[k]
	if !ok {
		f := at
		f.Kind = FindingSnapshotOrphan
		f.Detail = fmt.Sprintf("cached balance of %d as of seq %d, but the log has no entries for it",
			row.AmountCp, row.AsOfSeq)
		v.find(f)

		return
	}

	if core.Centipoints(row.AmountCp) != acc.amountCp {
		f := at
		f.Kind = FindingSnapshotAmountMismatch
		f.Seq = acc.maxSeq
		f.Detail = fmt.Sprintf("cached %d, the log sums to %d", row.AmountCp, acc.amountCp)
		v.find(f)
	}

	if row.EntryCount != acc.entryCount {
		f := at
		f.Kind = FindingSnapshotEntryCountMismatch
		f.Seq = acc.maxSeq
		f.Detail = fmt.Sprintf("cached entry_count %d, the log holds %d entries",
			row.EntryCount, acc.entryCount)
		v.find(f)
	}

	if row.AsOfSeq != acc.maxSeq {
		f := at
		f.Kind = FindingSnapshotAsOfSeqMismatch
		f.Seq = acc.maxSeq
		f.Detail = fmt.Sprintf("cached as_of_seq %d, the last seq to touch this balance is %d",
			row.AsOfSeq, acc.maxSeq)
		v.find(f)
	}
}

// auditChain walks the instance-wide audit log: gapless seq from 1, prev_hash links, recomputed
// hashes, and the head in dkp_meta.
//
// A SECOND CHAIN, not a second pool. audit_log records who did what and is not derivable from the
// ledger, so it is verified on its own terms — an instance whose money is intact and whose record of
// who moved it is not has a different problem from one whose money is wrong, and an operator has to
// be able to tell which they have.
func (v *verifier) auditChain(ctx context.Context) error {
	var (
		prev     []byte
		wantSeq  = int64(1)
		cursor   = int64(0)
		lastHash []byte
	)

	for {
		rows, err := v.q.ListAuditRowsAfterSeq(ctx, sqlitegen.ListAuditRowsAfterSeqParams{
			Seq:   cursor,
			Limit: v.pageSize,
		})
		if err != nil {
			return fmt.Errorf("list audit rows after seq %d: %w", cursor, err)
		}

		if len(rows) == 0 {
			break
		}

		for _, r := range rows {
			row := auditFromRow(r)

			v.checkAuditRow(row, prev, wantSeq)

			v.report.Audit.Rows++
			v.report.Audit.HeadSeq = row.Seq

			prev, lastHash = row.Hash, row.Hash
			wantSeq = row.Seq + 1
			cursor = row.Seq
		}
	}

	if lastHash != nil {
		v.report.Audit.Head = encodeHead(lastHash)
	}

	v.checkHead(ctx, MetaAuditHeadKey(), lastHash, Finding{
		Kind: FindingAuditHeadMismatch, Seq: v.report.Audit.HeadSeq,
	})

	return nil
}

// checkAuditRow is the per-row half of the audit chain verification.
func (v *verifier) checkAuditRow(row AuditRow, prev []byte, wantSeq int64) {
	at := Finding{Seq: row.Seq}

	if row.Seq != wantSeq {
		f := at
		f.Kind = FindingAuditSeqGap
		f.Detail = fmt.Sprintf("expected seq %d, found %d — %d row(s) missing",
			wantSeq, row.Seq, row.Seq-wantSeq)
		v.find(f)
	}

	if !bytes.Equal(row.PrevHash, prev) {
		f := at
		f.Kind = FindingAuditPrevHashMismatch
		f.Detail = fmt.Sprintf("prev_hash is %s, the previous row's hash is %s",
			describeHash(row.PrevHash), describeHash(prev))
		v.find(f)
	}

	want, err := AuditHash(row.PrevHash, row)
	if err != nil {
		// Unreachable for the same reason checkBatch's branch is, and recorded for the same reason.
		f := at
		f.Kind = FindingAuditHashMismatch
		f.Detail = fmt.Sprintf("recompute the hash: %v", err)
		v.find(f)
	} else if !bytes.Equal(want, row.Hash) {
		f := at
		f.Kind = FindingAuditHashMismatch
		f.Detail = fmt.Sprintf("stored hash is %s, the row hashes to %s",
			describeHash(row.Hash), describeHash(want))
		v.find(f)
	}
}

// checkHead compares a chain head in dkp_meta against the hash of the last row walked, and records
// at (with its Detail filled in) when they disagree.
//
// Four cases, and three of them are findings. A head that matches is the good one; a head that is
// absent for a chain with rows means the mirror was never written; a head that is present for an
// empty chain means rows were removed under it; and a head that is present and different is the
// interesting one — the rows and their attestation disagree.
//
// An UNPARSEABLE head is an error rather than a finding, matching readHead's own rule: a corrupted
// meta row is not evidence about the ledger, it is a reason this tool cannot answer.
func (v *verifier) checkHead(ctx context.Context, key string, last []byte, at Finding) {
	stored, err := readHead(ctx, v.q, key)
	if err != nil {
		f := at
		f.Detail = fmt.Sprintf("read %s: %v", key, err)
		v.find(f)

		return
	}

	if bytes.Equal(stored, last) {
		return
	}

	f := at

	switch {
	case stored == nil:
		f.Detail = fmt.Sprintf("no %s recorded; the chain ends at %s", key, describeHash(last))
	case last == nil:
		f.Detail = fmt.Sprintf("%s is %s but the chain has no rows", key, describeHash(stored))
	default:
		f.Detail = fmt.Sprintf("%s is %s, the chain ends at %s",
			key, describeHash(stored), describeHash(last))
	}

	v.find(f)
}

// sumEntries totals a batch's entries, reporting false on int64 overflow rather than wrapping. A
// wrapped total compared against a stored column would report a MATCH on two wrong numbers often
// enough to be worth the branch.
func sumEntries(entries []EntryRow) (core.Centipoints, bool) {
	var total core.Centipoints

	for _, e := range entries {
		sum, ok := addCentipoints(total, e.AmountCp)
		if !ok {
			return 0, false
		}

		total = sum
	}

	return total, true
}

// describeHash renders a hash for a human: the hex head, or the word for its absence. NULL and a
// 32-byte value of zeroes are different things and must not print the same.
func describeHash(h []byte) string {
	if h == nil {
		return "NULL"
	}

	return encodeHead(h)
}

// batchFromRow maps a generated ledger_batch row into the package's BatchRow, which is the shape
// BatchHash is defined against.
func batchFromRow(r sqlitegen.LedgerBatch) BatchRow {
	return BatchRow{
		ID:                 core.ULID(r.ID),
		PoolID:             core.ULID(r.PoolID),
		Seq:                r.Seq,
		Kind:               r.Kind,
		StrategyID:         r.StrategyID,
		StrategyVersion:    r.StrategyVersion,
		ConfigSnapshotJSON: r.ConfigSnapshotJson,
		RngSeed:            r.RngSeed,
		Source:             r.Source,
		SourceRef:          r.SourceRef,
		ActorUserID:        ulidPtr(r.ActorUserID),
		ActorTokenID:       ulidPtr(r.ActorTokenID),
		ActorIsBeneficiary: r.ActorIsBeneficiary,
		Reason:             r.Reason,
		ReversesBatchID:    ulidPtr(r.ReversesBatchID),
		EffectiveAt:        core.Micros(r.EffectiveAt),
		RecordedAt:         core.Micros(r.RecordedAt),
		EffectiveDay:       r.EffectiveDay,
		IdempotencyKey:     r.IdempotencyKey,
		EntryCount:         r.EntryCount,
		NetAmountCp:        core.Centipoints(r.NetAmountCp),
		PrevHash:           r.PrevHash,
		Hash:               r.Hash,
	}
}

// entryFromRow maps a generated ledger_entry row into the package's EntryRow.
func entryFromRow(r sqlitegen.LedgerEntry) EntryRow {
	return EntryRow{
		ID:           core.ULID(r.ID),
		BatchID:      core.ULID(r.BatchID),
		PoolID:       core.ULID(r.PoolID),
		Seq:          r.Seq,
		AccountID:    core.ULID(r.AccountID),
		CharacterID:  ulidPtr(r.CharacterID),
		BalanceKind:  r.BalanceKind,
		AmountCp:     core.Centipoints(r.AmountCp),
		ItemID:       ulidPtr(r.ItemID),
		ItemAwardID:  ulidPtr(r.ItemAwardID),
		RaidID:       ulidPtr(r.RaidID),
		TickID:       ulidPtr(r.TickID),
		MetadataJSON: r.MetadataJson,
	}
}

// auditFromRow maps a generated audit_log row into the package's AuditRow, which is the shape
// AuditHash is defined against.
func auditFromRow(r sqlitegen.AuditLog) AuditRow {
	return AuditRow{
		ID:            core.ULID(r.ID),
		Seq:           r.Seq,
		At:            core.Micros(r.At),
		ActorKind:     r.ActorKind,
		ActorLabel:    r.ActorLabel,
		Action:        r.Action,
		ResourceKind:  r.ResourceKind,
		ResourceID:    r.ResourceID,
		Outcome:       r.Outcome,
		LedgerBatchID: ulidPtr(r.LedgerBatchID),
		PrevHash:      r.PrevHash,
		Hash:          r.Hash,
	}
}

// ulidPtr is ulidPtrString's inverse: the optional TEXT column a generated row carries, as the
// optional ULID the package's row shapes use. nil in, nil out — a NULL column must not become "".
func ulidPtr(s *string) *core.ULID {
	if s == nil {
		return nil
	}

	u := core.ULID(*s)

	return &u
}

// orDefaultInt64 and orDefaultInt substitute a default for an unset option.
func orDefaultInt64(v, fallback int64) int64 {
	if v <= 0 {
		return fallback
	}

	return v
}

func orDefaultInt(v, fallback int) int {
	if v == 0 {
		return fallback
	}

	return v
}
