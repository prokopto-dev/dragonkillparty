package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// The four system-account keys (docs/design/01-domain-model.md §6.1). They are the ledger-addressable
// non-human targets that make zero-sum splits, rot handling and write-offs expressible, and they keep
// the Conserved invariant verifiable from outside the package (.claude/rules/ledger-and-strategy.md):
//
//   - Residue      absorbs the unallocatable remainder of a largest-remainder split.
//   - GuildBank    receives solo-kill and no-attendee awards per the pool's solo_policy.
//   - WriteOff     receives the debit for a rotted item nobody bought.
//   - ImportOpening is the counter-account for opening balances brought in by the EQdkp importer.
//
// These strings are identical to the account.system_key CHECK in db/schema.hcl and to the seed rows
// in db/migrations-sqlite/000003_ledger.sql. Adding a fifth is a schema change (a new CHECK value),
// not a constant.
const (
	SystemKeyResidue       = "residue"
	SystemKeyGuildBank     = "guild_bank"
	SystemKeyWriteOff      = "write_off"
	SystemKeyImportOpening = "import_opening"
)

// The deterministic ULIDs the migration seeds these rows with, and the default pool's id. This block
// is the SINGLE SOURCE OF TRUTH shared with the seed migration: the same literals appear verbatim in
// db/migrations-sqlite/000003_ledger.sql, and a test (internal/ledger/account_test.go and the raw-SQL
// variant in test/migrations) asserts each seeded row is addressable at exactly this id. They are
// deterministic rather than minted so the fresh-install fingerprint and the "addressable by id" tests
// are stable across every fresh install.
//
// Each is a valid 26-character Crockford base32 ULID with a zero timestamp prefix and a readable,
// legal-alphabet tail (no I, L, O or U). They are constants, never generated at runtime.
const (
	// DefaultPoolID is the one pool the migration seeds. PR 9 ships a single default pool; multi-pool
	// creation is a later phase.
	DefaultPoolID = core.ULID("00000000000000000000DKPP00")

	AccountIDResidue       = core.ULID("0000000000000000DKPACCTRES")
	AccountIDGuildBank     = core.ULID("0000000000000000DKPACCTBNK")
	AccountIDWriteOff      = core.ULID("0000000000000000DKPACCTWRF")
	AccountIDImportOpening = core.ULID("0000000000000000DKPACCTMPN")
)

// SystemAccountIDs maps each system_key to the deterministic id its seed row carries. It exists so a
// caller (or a test) can iterate the four accounts without restating the pairing, and so a mismatch
// between a key and its id is caught in one place rather than four.
func SystemAccountIDs() map[string]core.ULID {
	return map[string]core.ULID{
		SystemKeyResidue:       AccountIDResidue,
		SystemKeyGuildBank:     AccountIDGuildBank,
		SystemKeyWriteOff:      AccountIDWriteOff,
		SystemKeyImportOpening: AccountIDImportOpening,
	}
}

// Account is the balance holder (docs/design/01-domain-model.md §6.1): a person's account, or one of
// the four system accounts with no person. It is the ledger package's own shape rather than the raw
// generated row, so the nullable columns read as typed optionals and a caller never touches
// internal/store/sqlitegen.
type Account struct {
	ID        core.ULID
	Kind      string  // 'person' | 'system'
	PersonID  *string // nil for a system account
	SystemKey *string // nil for a person account; one of the SystemKey* constants otherwise
	Label     string
	CreatedAt core.Micros
	UpdatedAt core.Micros
}

// accountFromRow maps a generated account row into the package's Account.
func accountFromRow(r sqlitegen.Account) Account {
	return Account{
		ID:        core.ULID(r.ID),
		Kind:      r.Kind,
		PersonID:  r.PersonID,
		SystemKey: r.SystemKey,
		Label:     r.Label,
		CreatedAt: core.Micros(r.CreatedAt),
		UpdatedAt: core.Micros(r.UpdatedAt),
	}
}

// GetAccount reads one account by id through the read pool. It returns store.ErrNotFound when no such
// account exists, so a caller can branch on errors.Is without inspecting driver errors.
//
// This is a READ helper: it takes store.Queries rather than a *store.Store so it composes inside a
// transaction (q from Tx) as readily as outside one (store.Q()). PR 9 is read-only; the commit
// service that would call it inside a write transaction lands in PR 10.
func GetAccount(ctx context.Context, q store.Queries, id core.ULID) (Account, error) {
	row, err := q.GetAccount(ctx, id.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, fmt.Errorf("account %s: %w", id, store.ErrNotFound)
		}

		return Account{}, fmt.Errorf("get account %s: %w", id, err)
	}

	return accountFromRow(row), nil
}

// GetSystemAccount resolves one of the four seeded system accounts by its system_key. It backs the
// acceptance requirement that the system accounts are addressable from outside the package, by name
// as well as by id. An unknown or non-system key returns store.ErrNotFound.
func GetSystemAccount(ctx context.Context, q store.Queries, systemKey string) (Account, error) {
	key := systemKey
	row, err := q.GetSystemAccount(ctx, &key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Account{}, fmt.Errorf("system account %q: %w", systemKey, store.ErrNotFound)
		}

		return Account{}, fmt.Errorf("get system account %q: %w", systemKey, err)
	}

	return accountFromRow(row), nil
}
