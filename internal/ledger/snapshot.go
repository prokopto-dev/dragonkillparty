package ledger

import (
	"context"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// SnapshotDelta is one account's contribution to balance_snapshot from a single batch: the SUM and
// COUNT of that batch's entries for one (pool, account, balance_kind), plus the seq the snapshot is
// now current as of. UpsertBalanceSnapshot ADDS it to the existing cached row.
type SnapshotDelta struct {
	PoolID      core.ULID
	AccountID   core.ULID
	BalanceKind string
	AmountCp    core.Centipoints
	AsOfSeq     int64
	EntryCount  int64
	UpdatedAt   core.Micros
}

// UpsertBalanceSnapshot maintains the balance cache ADDITIVELY, keyed on
// (pool_id, account_id, balance_kind). On conflict the amount and the entry count are ADDED to the
// existing row and the as-of-seq and updated-at advance to the new head — so a caller passes this
// batch's delta and the running total accumulates. It matches a naive Go fold over all entries
// (asserted by TestSnapshot_TenThousandEntries_MatchesFold), which is the property that lets
// /standings read the snapshot instead of the ledger.
//
// balance_snapshot is a CACHE and is treated as one: it is never the source of truth (that is always
// the sum over ledger_entry, via BalanceAsOfSeq), it is rebuildable, and it is verified nightly by
// ledger.Verify. It is NOT droppable, though — ADR-0023 measured the alternative at 22 seconds a page
// — so "rebuildable" here means a rebuild is possible, not that running without it is. This helper
// must run inside the same store.Tx as the batch write (PR 10) so the cache and the log move
// together; PR 9 ships and tests the helper itself.
func UpsertBalanceSnapshot(ctx context.Context, q store.Queries, d SnapshotDelta) error {
	err := q.UpsertBalanceSnapshot(ctx, sqlitegen.UpsertBalanceSnapshotParams{
		PoolID:      d.PoolID.String(),
		AccountID:   d.AccountID.String(),
		BalanceKind: d.BalanceKind,
		AmountCp:    int64(d.AmountCp),
		AsOfSeq:     d.AsOfSeq,
		EntryCount:  d.EntryCount,
		UpdatedAt:   int64(d.UpdatedAt),
	})
	if err != nil {
		return fmt.Errorf("upsert balance snapshot for %s in pool %s kind %q: %w",
			d.AccountID, d.PoolID, d.BalanceKind, err)
	}

	return nil
}
