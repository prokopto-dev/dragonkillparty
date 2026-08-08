package ledger

import (
	"context"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// MaxPoolSeq returns the current head sequence number for a pool: COALESCE(max(seq), 0) over
// ledger_batch, so an empty pool reports 0. It is the ?4 a CURRENT balance derives its as-of-seq
// from, and the value NextPoolSeq increments.
func MaxPoolSeq(ctx context.Context, q store.Queries, poolID core.ULID) (int64, error) {
	head, err := q.MaxPoolSeq(ctx, poolID.String())
	if err != nil {
		return 0, fmt.Errorf("max seq for pool %s: %w", poolID, err)
	}

	return head, nil
}

// NextPoolSeq allocates the next per-pool sequence number: COALESCE(max(seq), 0) + 1.
//
// It is SAFE ONLY INSIDE store.Tx. The write pool is opened _txlock=immediate with
// SetMaxOpenConns(1), so the write transaction is the only writer and max+1 cannot race; the unique
// index ux_batch_seq(pool_id, seq) is the guardrail if that single-writer property is ever lost. Pass
// the Queries that Tx handed you, NOT store.Q(): allocating on the read pool and then inserting on
// the write pool would let two allocations return the same number.
//
// This is dialect divergence #1 (db/RECIPES.md): max+1 is not safe on Postgres, where it becomes a
// locked counter row or an advisory lock. Do not copy this pattern to any other sequence.
//
// PR 9 ships the allocator and its concurrency test; the commit service that calls it while writing a
// batch is PR 10.
func NextPoolSeq(ctx context.Context, q store.Queries, poolID core.ULID) (int64, error) {
	next, err := q.NextPoolSeq(ctx, poolID.String())
	if err != nil {
		return 0, fmt.Errorf("next seq for pool %s: %w", poolID, err)
	}

	return next, nil
}
