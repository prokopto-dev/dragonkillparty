package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// DBTX is the handle the low-level transaction primitive runs against.
//
// It is deliberately the shape sqlc generates against, so that Tx can construct `sqlitegen.New(tx)`
// and hand the callback a store.Queries — the form .claude/rules/store-and-sql.md documents —
// without the callback ever seeing a *sql.Tx.
//
// It is an interface rather than *sql.Tx because "never pass a *sql.Tx into a domain package"
// (.claude/rules/store-and-sql.md) — but note that DBTX is not a licence either. Its four methods
// are exactly what the SQL002 gate greps for, so a domain package that called one would fail
// `make lint-repo`. Inside this package it is the seam; outside it, it is a violation.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var _ DBTX = (*sql.Tx)(nil)

// Tx runs fn inside a single write transaction and commits it, or rolls back and returns the error
// fn returned. Every mutation in the product goes through here; there are no exceptions and no
// second way to open a transaction.
//
// The callback receives a store.Queries, not a raw handle. That is the signature change tx.go had
// reserved for PR 5 (the reservation is in git history): PR 5 writes the first query-backed
// mutation — PATCH /api/v1/guild — which is the first caller that has a typed query to run inside a
// transaction. A domain package therefore never touches DBTX or *sql.Tx; it sees the same Queries
// interface inside a transaction that Q() gives it outside one, and the two dialects' generated
// implementations both satisfy it.
//
// The transaction is always on the WRITE pool, which is capped at one connection. Two callers
// therefore queue rather than race, and because the pool's DSN carries _txlock=immediate the write
// lock is taken at BEGIN — before fn has done any work — so a busy database is a wait at the door
// rather than a half-finished transaction that has to be unwound.
//
// Reads that are not part of a mutation must NOT come through here: a read inside a write
// transaction holds the single writer for the duration of the read. Use Q() for those.
//
// On panic, the transaction is rolled back and the panic is re-raised unchanged. Swallowing it
// would convert a programming error into a silently-empty database, which is the failure mode this
// product can least afford.
func (s *Store) Tx(ctx context.Context, fn func(context.Context, Queries) error) error {
	return s.txRaw(ctx, func(ctx context.Context, tx DBTX) error {
		return fn(ctx, sqlitegen.New(tx))
	})
}

// ReadTx runs fn inside a single READ transaction on the read pool, so that every statement fn
// issues observes ONE consistent snapshot of the database.
//
// It exists because Q() cannot promise that. Q() hands out a Queries bound to the read POOL, so
// consecutive statements may land on different connections and each one sees whatever was committed
// when it began. For a single query that is correct and cheap. For a job that reads many things and
// then compares them against each other it is a correctness bug, and a subtle one: the reads are
// each individually right and the comparison between them is wrong.
//
// The caller that made this necessary is `dkp verify-ledger` (issue #198), and its failure is worth
// stating because every future multi-read job has it. The replay walks a pool's batches, then reads
// the chain head, then reads the cached balances. A batch committing between the walk and the head
// read advances the head while the walk's last hash does not, and the verifier reports a
// `ledger_head_mismatch` on a perfectly healthy ledger — during raid night, which is exactly when
// commits happen and exactly when nobody needs a false corruption alarm.
//
// WAL is what makes this cheap. A read transaction takes a snapshot at its first statement and holds
// it until it ends; readers never block the writer and the writer never blocks them, so a replay and
// a raid-night award proceed side by side and simply disagree about the future. The costs are real
// but small and bounded: it pins one connection out of max(4, NumCPU) for the duration, and the WAL
// cannot be checkpointed past the snapshot while it is held — which is a reason to keep read
// transactions short (a full-profile replay is a few seconds) and not a reason to avoid them.
//
// DEFERRED, not immediate: the read pool's DSN deliberately omits _txlock=immediate (see
// .claude/rules/store-and-sql.md — a reader taking the write lock serialises every read against the
// writer), so BEGIN takes no lock and the snapshot is established by fn's first statement.
//
// It is a READ door and the type system does not enforce that: fn receives the same Queries a
// mutation gets, so it *could* call an insert. It must not — the write would land on the read pool,
// outside the single-writer discipline every seq allocator in the product depends on. Mutations go
// through Tx. A caller that needs both is a caller that needs Tx.
func (s *Store) ReadTx(ctx context.Context, fn func(context.Context, Queries) error) error {
	tx, err := s.read.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin read transaction: %w", err)
	}

	// Rolled back on every path, including the success one: a read transaction has nothing to
	// commit, and Rollback is how a snapshot is released. The panic path is handled the same way as
	// txRaw's, and for the same reason — a leaked read transaction holds a connection and a WAL
	// snapshot for the life of the process.
	defer func() {
		p := recover()

		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			slog.ErrorContext(ctx, "roll back read transaction", "error", rbErr, "path", s.path)
		}

		if p != nil {
			panic(p)
		}
	}()

	if err := fn(ctx, sqlitegen.New(tx)); err != nil {
		return fmt.Errorf("read transaction: %w", err)
	}

	return nil
}

// txRaw is the transaction primitive: it owns the locking, the rollback, the panic handling and the
// commit, and it runs fn against the raw transaction handle.
//
// It is unexported because a raw DBTX must never leave this package — Tx is the only public door,
// and it constructs a Queries before handing control out. txRaw exists as a separate function for
// two reasons: SetMetaValue runs an upsert through the generated Queries but the store's own
// machinery tests need statements no Queries method covers (a scratch table), and keeping the
// mechanics in one place means the commit/rollback/panic guarantees are written and tested once
// rather than once per caller.
func (s *Store) txRaw(ctx context.Context, fn func(context.Context, DBTX) error) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write transaction: %w", err)
	}

	// One defer covers all three exits. `settled` is set only after Commit returns nil, so the
	// error path, the panic path and a failed Commit all reach Rollback. Rolling back an already
	// committed or already rolled-back transaction returns sql.ErrTxDone, which is why that one
	// error is not worth reporting.
	settled := false

	defer func() {
		p := recover()

		if !settled {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				// Logged rather than returned: on the panic path there is no error to return to,
				// and on the error path fn's error is the one the caller needs to see. Never
				// discarded — a rollback that fails means the connection is in an unknown state
				// and somebody debugging a stuck writer needs this line.
				slog.ErrorContext(ctx, "rollback write transaction", "error", rbErr, "path", s.path)
			}
		}

		if p != nil {
			panic(p)
		}
	}()

	if err := fn(ctx, tx); err != nil {
		return fmt.Errorf("write transaction: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write transaction: %w", err)
	}

	settled = true

	return nil
}
