package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// Queries is the contract both dialects satisfy — the hand-written interface that keeps the
// post-1.0 Postgres port cheap (.claude/rules/store-and-sql.md).
//
// It has two methods today because there are two queries today. It grows one method per query, in
// the same change as the query, forever: a sqlite implementation that gains a method the contract
// does not know about is a Postgres target silently rotting.
type Queries interface {
	GetMetaValue(ctx context.Context, key string) (string, error)
	UpsertMetaValue(ctx context.Context, arg sqlitegen.UpsertMetaValueParams) error
}

// The compile-time proof. It costs nothing and `go build` checks it on every save.
//
// The pggen half — `var _ Queries = (*pggen.Queries)(nil)` under a CI-only build tag — arrives with
// the Postgres target after 1.0. Declaring it now would mean committing an empty generated package
// to satisfy a compiler, which is a worse lie than the missing line.
var _ Queries = (*sqlitegen.Queries)(nil)

// MetaValue reads one row from dkp_meta.
//
// It returns ErrNotFound both when the key is absent and when dkp_meta itself does not exist yet.
// Collapsing those is deliberate: the only caller is the boot path, running against a database
// that may be brand new, and "there is no recorded schema version" is the same answer in both
// cases. A caller that needs to tell them apart has a bug in its boot ordering, not a need for a
// second sentinel.
func (s *Store) MetaValue(ctx context.Context, key string) (string, error) {
	value, err := sqlitegen.New(s.read).GetMetaValue(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
			return "", fmt.Errorf("meta %s: %w", key, ErrNotFound)
		}

		return "", fmt.Errorf("read meta %s: %w", key, err)
	}

	return value, nil
}

// SetMetaValue writes one row to dkp_meta, through the write pool and inside a transaction like
// every other mutation in the product.
func (s *Store) SetMetaValue(ctx context.Context, key, value string, updatedAt int64) error {
	return s.Tx(ctx, func(ctx context.Context, tx DBTX) error {
		return sqlitegen.New(tx).UpsertMetaValue(ctx, sqlitegen.UpsertMetaValueParams{
			Key:       key,
			Value:     value,
			UpdatedAt: updatedAt,
		})
	})
}

// isMissingTable reports whether err is SQLite's "no such table".
//
// String matching, and it is ugly. modernc.org/sqlite returns a *sqlite.Error carrying the numeric
// result code, but SQLITE_ERROR (1) is the code for every parse and binding failure, so matching on
// it would swallow a typo'd column as "the table does not exist yet" — which at boot means silently
// deciding a populated database is unmigrated. The message is the only part of this that is
// specific, so the message is what gets matched, and it is matched in exactly one place.
func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
