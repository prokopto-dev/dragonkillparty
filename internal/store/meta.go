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
// It grows one method per query, in the same change as the query, forever: a sqlite implementation
// that gains a method the contract does not know about is a Postgres target silently rotting. It is
// the interface a domain package sees — through Q() for a read and through Tx for a mutation — so
// no service ever holds a *sql.DB or a *sql.Tx (law 2).
type Queries interface {
	GetGuild(ctx context.Context) (sqlitegen.Guild, error)
	InsertGuild(ctx context.Context, arg sqlitegen.InsertGuildParams) (sqlitegen.Guild, error)
	UpdateGuild(ctx context.Context, arg sqlitegen.UpdateGuildParams) (sqlitegen.Guild, error)
	GetMetaValue(ctx context.Context, key string) (string, error)
	UpsertMetaValue(ctx context.Context, arg sqlitegen.UpsertMetaValueParams) error

	// The ledger reads and helpers (Phase 0 PR 9). READ and HELPER only: the batch/entry commit
	// service is PR 10, so there is no batch or entry INSERT on this contract yet. BalanceAsOfSeq is
	// the definitional balance query, served from the covering index ix_entry_balance;
	// MaxPoolSeq/NextPoolSeq are the per-pool sequence head and allocator (NextPoolSeq is safe only
	// inside Tx); UpsertBalanceSnapshot maintains the balance cache additively;
	// GetAccount/GetSystemAccount resolve accounts by id and by system_key.
	BalanceAsOfSeq(ctx context.Context, arg sqlitegen.BalanceAsOfSeqParams) (int64, error)
	MaxPoolSeq(ctx context.Context, poolID string) (int64, error)
	NextPoolSeq(ctx context.Context, poolID string) (int64, error)
	UpsertBalanceSnapshot(ctx context.Context, arg sqlitegen.UpsertBalanceSnapshotParams) error
	GetAccount(ctx context.Context, id string) (sqlitegen.Account, error)
	GetSystemAccount(ctx context.Context, systemKey *string) (sqlitegen.Account, error)

	// The standings pair and the account writer (Phase 1, issue #190). The two standings queries
	// answer the SAME question by the two available routes — the cache and the definitional SUM over
	// the log — and they are both on the contract because the gap between them at 520k entries is
	// what decided whether balance_snapshot survives (V5, answered by ADR-0023: 13 pages against the
	// cache, 10,412 against the SUM, so it survives and is load-bearing). Keeping the slow arm generated and pinned by
	// an EXPLAIN golden is what makes that comparison repeatable rather than a number in a PR body.
	//
	// InsertAccount is the person half of the account table; the four system accounts are seeded by
	// the migration. Its first caller is internal/seed, which cannot hang 520k entries on four rows.
	StandingsFromSnapshot(ctx context.Context, arg sqlitegen.StandingsFromSnapshotParams) ([]sqlitegen.StandingsFromSnapshotRow, error)
	StandingsFromLedger(ctx context.Context, arg sqlitegen.StandingsFromLedgerParams) ([]sqlitegen.StandingsFromLedgerRow, error)
	InsertAccount(ctx context.Context, arg sqlitegen.InsertAccountParams) error

	// The ledger WRITE path (Phase 0 PR 10a). These five are called from exactly one place —
	// ledger.Service.Commit — and all five run inside a single store.Tx together with
	// UpsertBalanceSnapshot and UpsertMetaValue, because a batch, its entries, the snapshot cache,
	// the audit row and the outbox event are one economic event and must not be separable.
	//
	// There is no UpdateLedgerBatch and no DeleteLedgerEntry on this contract, and there never will
	// be: the tables are append-only (canonical §10), the database triggers abort a mutation, and a
	// correction is a reversal batch. A method added here would be a method the Postgres target has
	// to implement, which is how an invariant becomes a portability requirement by accident.
	InsertLedgerBatch(ctx context.Context, arg sqlitegen.InsertLedgerBatchParams) error
	InsertLedgerEntry(ctx context.Context, arg sqlitegen.InsertLedgerEntryParams) error
	GetBatchByIdempotencyKey(ctx context.Context, arg sqlitegen.GetBatchByIdempotencyKeyParams) (sqlitegen.GetBatchByIdempotencyKeyRow, error)
	GetLedgerBatch(ctx context.Context, id string) (sqlitegen.GetLedgerBatchRow, error)
	NextAuditSeq(ctx context.Context) (int64, error)
	InsertAuditLog(ctx context.Context, arg sqlitegen.InsertAuditLogParams) error
	InsertOutboxEvent(ctx context.Context, arg sqlitegen.InsertOutboxEventParams) (int64, error)

	// The REPLAY reads (Phase 1, issue #198). `dkp verify-ledger` walks the whole ledger from
	// genesis: every pool's batch chain, the instance-wide audit chain, and a fold over every entry
	// compared against balance_snapshot. ledger.Verify is the only caller.
	//
	// All five are SELECTs, and all five are KEYSET-PAGED rather than whole-table reads (except
	// ListEntriesByBatch, whose page is one batch and is bounded by the domain). That is what keeps
	// the verifier's memory proportional to the roster instead of to the log — a `:many` over
	// 520,000 entries is 520,000 structs at once on the Raspberry Pi this product targets.
	//
	// There is no rebuild here, only reads. Recomputing balance_snapshot is a write, so the flag the
	// operations docs describe (`--rebuild`) needs an upsert this contract already has and a job that
	// is not this one — and under ADR-0023 that job matters more than it used to, because losing the
	// cache is a rebuild rather than a slower page.
	ListPoolIDs(ctx context.Context) ([]string, error)
	ListBatchesAfterSeq(ctx context.Context, arg sqlitegen.ListBatchesAfterSeqParams) ([]sqlitegen.LedgerBatch, error)
	ListEntriesByBatch(ctx context.Context, batchID string) ([]sqlitegen.LedgerEntry, error)
	ListSnapshotsAfter(ctx context.Context, arg sqlitegen.ListSnapshotsAfterParams) ([]sqlitegen.ListSnapshotsAfterRow, error)
	ListAuditRowsAfterSeq(ctx context.Context, arg sqlitegen.ListAuditRowsAfterSeqParams) ([]sqlitegen.AuditLog, error)

	// The permission-catalogue reconciliation (Phase 2 Wave 0b, issue #261). All four are called from
	// exactly one place — authz.Reconciler.Reconcile, on the boot path — and the three that write run
	// inside a single store.Tx, because a half-reconciled catalogue is a database whose FK to
	// permission(key) means something different from what the running binary implements.
	//
	// There is no DeletePermission here and there never will be: role_permission is FK-constrained to
	// permission(key), so removing a key a newer binary stopped shipping either fails against the
	// grants that reference it or silently strips capability from every role that held it. OrphanPermission
	// is the whole answer — the row is marked, never removed (docs/design/01-domain-model.md §5).
	ListPermissions(ctx context.Context) ([]sqlitegen.Permission, error)
	GetPermission(ctx context.Context, key string) (sqlitegen.Permission, error)
	UpsertPermission(ctx context.Context, arg sqlitegen.UpsertPermissionParams) error
	OrphanPermission(ctx context.Context, arg sqlitegen.OrphanPermissionParams) error

	// The built-in role seed, called from the same place and in the same transaction. There is no
	// UpdateRole and no DeleteRole: a built-in role is created once and then belongs to the guild
	// (docs/design/01-domain-model.md §5.1 calls this table "the seed, not a second catalogue"), and
	// rewriting its grants on a later boot would silently restore a permission an officer revoked.
	ListRoles(ctx context.Context) ([]sqlitegen.Role, error)
	InsertRole(ctx context.Context, arg sqlitegen.InsertRoleParams) error
	InsertRolePermission(ctx context.Context, arg sqlitegen.InsertRolePermissionParams) error

	// Identity and credentials (Phase 2 Wave 0d, issue #273). The two Resolve methods are the auth
	// hot path — one indexed lookup each, on ux_session_token and ux_api_token_prefix — and they are
	// the ONLY reads internal/auth performs per request. The two Touch methods are throttled writes
	// on the same rows, guarded in SQL so a burst on one credential produces at most one statement on
	// SQLite's single writer.
	//
	// NEITHER RESOLVE FILTERS the row it returns. Expiry, revocation, the account's state and the
	// session epoch all come back and internal/auth decides, because the middleware must tell those
	// apart in its logs while returning the same 401 to the caller — and "was this token used, and
	// when" is the only question worth asking during an incident.
	//
	// THERE IS NO DeleteSession, NO RevokeAPIToken AND NO UpdateAppUser here, and their absence is
	// the wave boundary rather than an oversight: sign-out, token mint/rotate/revoke and credential
	// edits are session-and-step-up operations (canonical §6's capability floor) that land with the
	// endpoints performing them. A mutation with no caller is a method the Postgres target has to
	// implement for nothing.
	InsertAppUser(ctx context.Context, arg sqlitegen.InsertAppUserParams) error
	InsertUserIdentity(ctx context.Context, arg sqlitegen.InsertUserIdentityParams) error
	InsertSession(ctx context.Context, arg sqlitegen.InsertSessionParams) error
	ResolveSession(ctx context.Context, tokenHash []byte) (sqlitegen.ResolveSessionRow, error)
	TouchSession(ctx context.Context, arg sqlitegen.TouchSessionParams) error
	InsertServiceAccount(ctx context.Context, arg sqlitegen.InsertServiceAccountParams) error
	InsertAPIToken(ctx context.Context, arg sqlitegen.InsertAPITokenParams) error
	ResolveAPIToken(ctx context.Context, prefix string) (sqlitegen.ResolveAPITokenRow, error)
	TouchAPIToken(ctx context.Context, arg sqlitegen.TouchAPITokenParams) error
}

// The compile-time proof. It costs nothing and `go build` checks it on every save.
//
// The pggen half — `var _ Queries = (*pggen.Queries)(nil)` under a CI-only build tag — arrives with
// the Postgres target after 1.0. Declaring it now would mean committing an empty generated package
// to satisfy a compiler, which is a worse lie than the missing line.
var _ Queries = (*sqlitegen.Queries)(nil)

// Q returns a Queries bound to the READ pool, for reads that are not part of a mutation.
//
// A read through Q() runs on the read pool — max(4, NumCPU) connections in WAL, so it never blocks
// the single writer — while a read inside Tx runs on the write connection and holds it. The rule is
// .claude/rules/store-and-sql.md's: a mutation and the reads it depends on go through Tx; a
// standalone read (GET /api/v1/guild) goes through Q(). Returning the interface rather than the
// concrete *sqlitegen.Queries keeps the dual-dialect seam intact — a caller cannot reach a
// sqlite-only method by accident.
func (s *Store) Q() Queries {
	return sqlitegen.New(s.read)
}

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
	return s.Tx(ctx, func(ctx context.Context, q Queries) error {
		return q.UpsertMetaValue(ctx, sqlitegen.UpsertMetaValueParams{
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
