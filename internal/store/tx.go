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
