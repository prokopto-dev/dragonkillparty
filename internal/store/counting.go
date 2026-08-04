package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Op is what a recorded statement did.
type Op string

const (
	OpQuery Op = "query"
	OpExec  Op = "exec"
)

// Statement is one recorded database round trip, with its SQL text kept verbatim.
//
// The text is retained rather than only counted because a budget failure that says "you used 9
// statements, the budget is 4" is a puzzle, and one that prints the nine queries in order is a
// diagnosis. PR 9's EXPLAIN QUERY PLAN goldens read the text from here too, so normalising or
// truncating it would break them.
type Statement struct {
	Op   Op
	SQL  string
	Args int
}

// Counter records every statement executed through the pools it is attached to.
//
// This is the N+1 tripwire and the highest-value piece of test infrastructure in an agent-heavy
// codebase (docs/design/04-testing.md): fully deterministic, no runner noise, and it fires the
// instant a regression appears rather than when somebody notices the page is slow.
//
// Safe for concurrent use — the read pool is concurrent by design.
type Counter struct {
	mu    sync.Mutex
	stmts []Statement
}

// NewCounter returns an empty Counter, ready to be passed to WithStatementCounter.
func NewCounter() *Counter { return &Counter{} }

func (c *Counter) record(op Op, query string, args int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stmts = append(c.stmts, Statement{Op: op, SQL: query, Args: args})
}

// Count returns how many statements have been recorded.
func (c *Counter) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.stmts)
}

// Statements returns a copy of everything recorded, in execution order.
func (c *Counter) Statements() []Statement {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Statement, len(c.stmts))
	copy(out, c.stmts)

	return out
}

// Reset discards everything recorded so far. Useful to exclude fixture setup from a budget.
func (c *Counter) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stmts = nil
}

// String renders the recorded statements as a numbered listing, for a failure message.
func (c *Counter) String() string {
	return format(c.Statements())
}

func format(stmts []Statement) string {
	var b strings.Builder

	for i, s := range stmts {
		fmt.Fprintf(&b, "  %2d. [%s] %s\n", i+1, s.Op, strings.Join(strings.Fields(s.SQL), " "))
	}

	return b.String()
}

// --- driver interposition -----------------------------------------------------------------------
//
// The wrapper sits between database/sql and modernc.org/sqlite. It counts at TWO layers, because
// database/sql reaches the driver by two different routes and a wrapper that watched only one
// would undercount exactly the code that matters:
//
//   - conn level: db.QueryContext / db.ExecContext / tx.ExecContext go straight to the connection's
//     QueryerContext / ExecerContext.
//   - stmt level: db.PrepareContext returns a driver statement, and sqlc-generated code that
//     prepares once and executes many times never touches the conn-level methods again.
//
// Statements are recorded on EXECUTION, never on Prepare. One prepared statement executed 280 times
// in a loop is 280 statements, and that loop is precisely the N+1 this exists to catch.

// fullConn is every optional interface modernc.org/sqlite's connection implements, asserted
// together so a wrapped connection is never weaker than the one it wraps.
//
// The assertion is not defensive padding. Silently dropping driver.SessionResetter would leave
// transaction state on a connection returned to the pool; dropping driver.Validator would hand out
// a connection the driver knows is dead. Both produce intermittent, misattributed failures weeks
// later. A driver upgrade that removes one of these should be a loud error at the first Connect,
// which is what this gives.
type fullConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}

// fullStmt is the same idea for prepared statements.
type fullStmt interface {
	driver.Stmt
	driver.StmtExecContext
	driver.StmtQueryContext
}

type countingConnector struct {
	driver.Connector

	counter *Counter
}

func (c countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	inner, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	full, ok := inner.(fullConn)
	if !ok {
		return nil, errors.Join(
			fmt.Errorf("sqlite connection %T no longer implements the full driver interface set: "+
				"the statement counter must not silently degrade it", inner),
			inner.Close(),
		)
	}

	return countingConn{inner: full, counter: c.counter}, nil
}

type countingConn struct {
	inner   fullConn
	counter *Counter
}

func (c countingConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	inner, err := c.inner.PrepareContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("prepare: %w", err)
	}

	full, ok := inner.(fullStmt)
	if !ok {
		return nil, errors.Join(
			fmt.Errorf("sqlite statement %T no longer implements StmtExecContext and "+
				"StmtQueryContext: prepared statements would go uncounted", inner),
			inner.Close(),
		)
	}

	return countingStmt{inner: full, counter: c.counter, query: query}, nil
}

func (c countingConn) Close() error { return c.inner.Close() }

func (c countingConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.inner.BeginTx(ctx, opts)
}

func (c countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	res, err := c.inner.ExecContext(ctx, query, args)
	c.recordUnlessSkipped(OpExec, query, len(args), err)

	return res, err
}

func (c countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.inner.QueryContext(ctx, query, args)
	c.recordUnlessSkipped(OpQuery, query, len(args), err)

	return rows, err
}

func (c countingConn) Ping(ctx context.Context) error { return c.inner.Ping(ctx) }

func (c countingConn) ResetSession(ctx context.Context) error { return c.inner.ResetSession(ctx) }

func (c countingConn) IsValid() bool { return c.inner.IsValid() }

// recordUnlessSkipped records a statement unless the driver declined it.
//
// driver.ErrSkip means "I did not run this, ask me again through Prepare". database/sql then
// retries the same SQL down the prepared-statement path, which this wrapper also counts — so
// recording an ErrSkip would count one statement twice. modernc.org/sqlite does not currently
// return ErrSkip, and this is here so it stays correct if that changes.
func (c countingConn) recordUnlessSkipped(op Op, query string, args int, err error) {
	if errors.Is(err, driver.ErrSkip) {
		return
	}

	c.counter.record(op, query, args)
}

type countingStmt struct {
	inner   fullStmt
	counter *Counter
	query   string
}

func (s countingStmt) Close() error  { return s.inner.Close() }
func (s countingStmt) NumInput() int { return s.inner.NumInput() }

// Exec and Query satisfy driver.Stmt, which still requires them. database/sql only reaches for
// them when the Context variants are absent, and they are not — so rather than call the deprecated
// inner methods these forward to the Context forms, which keeps the recording in one place.
func (s countingStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(args))
}

func (s countingStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(args))
}

// named converts positional driver.Values to the NamedValue form, ordinals 1-based as
// database/sql numbers them.
func named(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}

	return out
}

func (s countingStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	s.counter.record(OpExec, s.query, len(args))

	return s.inner.ExecContext(ctx, args)
}

func (s countingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	s.counter.record(OpQuery, s.query, len(args))

	return s.inner.QueryContext(ctx, args)
}
