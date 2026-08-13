package ledger

import (
	"context"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// BalanceAsOfSeq returns an account's balance in a pool, for one balance kind, as of a sequence
// number: COALESCE(sum(amount_cp), 0) over every ledger_entry with seq <= asOfSeq. This is the
// definitional balance (docs/design/01-domain-model.md §9.4) and the only correct way to read one.
//
// A balance is defined as of a SEQ, never a timestamp: timestamps tie and a backdated effective_at
// must not change what a past balance was. The query is served entirely from the covering index
// ix_entry_balance with no table access (the EXPLAIN QUERY PLAN golden asserts it), so this is an
// index-range sum, not a scan.
//
// It takes store.Queries, so it reads the same whether it is handed the read pool (store.Q()) or a
// transaction's queries (from Tx). PR 9 only ever calls it read-only; PR 10's verify-ledger job will
// call it inside the write transaction to check the snapshot against the fold.
func BalanceAsOfSeq(
	ctx context.Context,
	q store.Queries,
	poolID, accountID core.ULID,
	balanceKind string,
	asOfSeq int64,
) (core.Centipoints, error) {
	amount, err := q.BalanceAsOfSeq(ctx, sqlitegen.BalanceAsOfSeqParams{
		PoolID:      poolID.String(),
		AccountID:   accountID.String(),
		BalanceKind: balanceKind,
		Seq:         asOfSeq,
	})
	if err != nil {
		return 0, fmt.Errorf("balance of %s in pool %s kind %q as of seq %d: %w",
			accountID, poolID, balanceKind, asOfSeq, err)
	}

	return core.Centipoints(amount), nil
}

// CurrentBalance returns an account's balance as of the pool's current head seq. It is
// BalanceAsOfSeq with asOfSeq derived from MaxPoolSeq, and it is the balance a member sees today.
//
// Two reads rather than one, deliberately: the head is a per-pool value that a caller reading many
// accounts should fetch once (via MaxPoolSeq) and pass to BalanceAsOfSeq, so this convenience wrapper
// is for the single-account case. It never reads balance_snapshot: the snapshot is a cache, and the
// source of truth is always the sum over the log. That stays true under ADR-0023 — the cache being
// load-bearing for the standings PAGE does not make it the answer to a dispute.
func CurrentBalance(
	ctx context.Context,
	q store.Queries,
	poolID, accountID core.ULID,
	balanceKind string,
) (core.Centipoints, error) {
	head, err := MaxPoolSeq(ctx, q, poolID)
	if err != nil {
		return 0, err
	}

	return BalanceAsOfSeq(ctx, q, poolID, accountID, balanceKind, head)
}
