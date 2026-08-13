package ledger

import (
	"context"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The standings read, both ways. Phase 1, issue #190.
//
// A standings page is every account's balance in one pool, highest first. There are exactly two
// ways to produce it and this file holds both, on purpose:
//
//	StandingsFromSnapshot   one indexed row per account, from the droppable cache
//	StandingsFromLedger     one grouped SUM over every entry in the pool, from the log
//
// They must agree. The log is the source of truth (docs/concepts/ledger.md: "a balance is not
// stored"), the cache is an optimisation, and the entire question item V5 of
// docs/development/verify-before-phase-0.md asks is whether the optimisation is load-bearing or
// merely nice. Keeping the slow arm as a first-class, generated, plan-pinned query rather than a
// string in a benchmark is what makes that question answerable again next year, on somebody else's
// hardware, without re-deriving the comparison.
//
// The slow arm is also not only a control: it is what a verification job reads. `dkp verify-ledger`
// and the nightly replay (ROADMAP Phase 1 item 9) recompute balances from the log and compare them
// against the cache, which is this query against that one.

// Standing is one row of a standings table: an account, its balance, and the two facts that make a
// drift check possible — how many entries went into it and the seq it is current as of.
//
// ONE type for both routes, because a standing is one concept (.claude/rules/go-idioms.md). The two
// readers fill it identically: EntryCount is the number of entries folded into the balance, and
// AsOfSeq is the sequence the balance is stated at. For the ledger route that is the seq the caller
// asked for, which is precisely what "as of" means; for the snapshot route it is the seq the cache
// last advanced to, which is the same number when the cache is current and is the evidence when it
// is not.
type Standing struct {
	AccountID  core.ULID
	AmountCp   core.Centipoints
	AsOfSeq    int64
	EntryCount int64
}

// StandingsFromSnapshot reads the standings from balance_snapshot: one row per account, walked out
// of ix_snapshot_standings in balance order.
//
// This is the read /standings will serve (Phase 3) and the one V5 budgets at 4 statements and
// 150 ms p99. It is a CACHE READ, and a caller must hold that in mind: the cache is maintained in
// the same transaction as every write and verified nightly against the fold, so it is correct — but
// when a balance is disputed, the answer that settles the dispute is the log's, which is what
// BalanceAsOfSeq and StandingsFromLedger return.
//
// limit bounds the page. There is no cursor parameter yet; see the query's comment in
// db/queries/ledger.sql for why the pagination contract waits for the endpoint that pages.
func StandingsFromSnapshot(
	ctx context.Context,
	q store.Queries,
	poolID core.ULID,
	balanceKind string,
	limit int64,
) ([]Standing, error) {
	rows, err := q.StandingsFromSnapshot(ctx, sqlitegen.StandingsFromSnapshotParams{
		PoolID:      poolID.String(),
		BalanceKind: balanceKind,
		Limit:       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read standings from the snapshot for pool %s kind %q: %w",
			poolID, balanceKind, err)
	}

	out := make([]Standing, len(rows))
	for i, r := range rows {
		out[i] = Standing{
			AccountID:  core.ULID(r.AccountID),
			AmountCp:   core.Centipoints(r.AmountCp),
			AsOfSeq:    r.AsOfSeq,
			EntryCount: r.EntryCount,
		}
	}

	return out, nil
}

// StandingsFromLedger computes the standings the definitional way: sum(amount_cp) over every entry
// in the pool with seq <= asOfSeq, grouped by account.
//
// It is the same arithmetic BalanceAsOfSeq performs for one account, performed for all of them in
// one statement. Use it to verify the cache, to answer a dispute, and to measure what the cache is
// worth. Do NOT use it to render a page: at guild scale it aggregates half a million index rows
// where the snapshot read touches a few hundred, and the whole point of the cache is that the
// difference is visible on the hardware an officer actually runs.
func StandingsFromLedger(
	ctx context.Context,
	q store.Queries,
	poolID core.ULID,
	balanceKind string,
	asOfSeq, limit int64,
) ([]Standing, error) {
	rows, err := q.StandingsFromLedger(ctx, sqlitegen.StandingsFromLedgerParams{
		PoolID:      poolID.String(),
		BalanceKind: balanceKind,
		Seq:         asOfSeq,
		Limit:       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("read standings from the ledger for pool %s kind %q as of seq %d: %w",
			poolID, balanceKind, asOfSeq, err)
	}

	out := make([]Standing, len(rows))
	for i, r := range rows {
		out[i] = Standing{
			AccountID:  core.ULID(r.AccountID),
			AmountCp:   core.Centipoints(r.AmountCp),
			AsOfSeq:    asOfSeq,
			EntryCount: r.EntryCount,
		}
	}

	return out, nil
}
