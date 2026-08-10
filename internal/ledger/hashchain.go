package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// The hash chains. Phase 0 PR 10a, implementing docs/design/01-domain-model.md §9.6 and §17.
//
//	batch hash = SHA-256( prev_hash || canonical_json(batch without hash) || canonical_json(entries ORDER BY id) )
//	audit hash = SHA-256( prev_hash || canonical_json(row without hash) )
//
// The ledger chain is PER POOL, because seq is per pool. The audit chain is instance-wide and
// independent of it. Each head is mirrored into dkp_meta — 'ledger_head:<pool_id>' and 'audit_head'
// — inside the same transaction as the row, so the head and the row it describes can never disagree
// across a crash.
//
// THE HONEST LIMITATION, which the docs state and which this file must not overclaim. An actor with
// filesystem access can rewrite rows AND recompute the chain; a local-only hash chain proves nothing
// against a local adversary. The control is PUBLICATION — a daily anchor written off-box and posted
// to Discord (§9.6), which is Phase 2 work. What the chain buys today is that a rewrite must be
// TOTAL and DELIBERATE: you cannot edit one batch and leave the rest consistent, so an accidental
// corruption, a partial restore or a half-applied migration is detectable by replay. That is worth
// having on its own, and it is not the same claim as tamper-proof.

// hashLength is SHA-256's digest size. Named so the code reads as an assertion about the chain
// rather than as an arbitrary 32.
const hashLength = sha256.Size

// batchHashInput is the batch as it is hashed: every persisted column EXCEPT hash itself, in a fixed
// field order.
//
// A separate struct rather than reusing the generated row type, and the reason is durability. The
// generated struct's field ORDER is an artefact of the column order in the SELECT that sqlc parsed,
// so reordering a query — a purely cosmetic edit — would change every hash the product has ever
// computed and orphan the whole chain. Declaring the hashed shape here makes the hash's definition a
// thing somebody has to edit on purpose, and the golden-value test in hashchain_test.go is what
// notices if they do.
//
// prev_hash is not a field: it is prepended to the digest as raw bytes, exactly as §9.6 writes it.
type batchHashInput struct {
	ID                 string  `json:"id"`
	PoolID             string  `json:"pool_id"`
	Seq                int64   `json:"seq"`
	Kind               string  `json:"kind"`
	StrategyID         string  `json:"strategy_id"`
	StrategyVersion    string  `json:"strategy_version"`
	ConfigSnapshotJSON string  `json:"config_snapshot_json"`
	RngSeed            *int64  `json:"rng_seed"`
	Source             string  `json:"source"`
	SourceRef          *string `json:"source_ref"`
	ActorUserID        *string `json:"actor_user_id"`
	ActorTokenID       *string `json:"actor_token_id"`
	ActorIsBeneficiary int64   `json:"actor_is_beneficiary"`
	Reason             string  `json:"reason"`
	ReversesBatchID    *string `json:"reverses_batch_id"`
	EffectiveAt        int64   `json:"effective_at"`
	RecordedAt         int64   `json:"recorded_at"`
	EffectiveDay       string  `json:"effective_day"`
	IdempotencyKey     *string `json:"idempotency_key"`
	EntryCount         int64   `json:"entry_count"`
	NetAmountCp        int64   `json:"net_amount_cp"`
}

// entryHashInput is one entry as it is hashed: every persisted column, in a fixed field order. Same
// reasoning as batchHashInput — the shape is declared, not borrowed from a generated type.
type entryHashInput struct {
	ID           string  `json:"id"`
	BatchID      string  `json:"batch_id"`
	PoolID       string  `json:"pool_id"`
	Seq          int64   `json:"seq"`
	AccountID    string  `json:"account_id"`
	CharacterID  *string `json:"character_id"`
	BalanceKind  string  `json:"balance_kind"`
	AmountCp     int64   `json:"amount_cp"`
	ItemID       *string `json:"item_id"`
	ItemAwardID  *string `json:"item_award_id"`
	RaidID       *string `json:"raid_id"`
	TickID       *string `json:"tick_id"`
	MetadataJSON string  `json:"metadata_json"`
}

// auditHashInput is the audit row as it is hashed: every persisted column except hash.
type auditHashInput struct {
	ID            string  `json:"id"`
	Seq           int64   `json:"seq"`
	At            int64   `json:"at"`
	ActorKind     string  `json:"actor_kind"`
	ActorLabel    string  `json:"actor_label"`
	Action        string  `json:"action"`
	ResourceKind  string  `json:"resource_kind"`
	ResourceID    *string `json:"resource_id"`
	Outcome       string  `json:"outcome"`
	LedgerBatchID *string `json:"ledger_batch_id"`
}

// BatchHash computes the per-pool chain link for a batch and its entries.
//
// Entries are hashed in ID ORDER, not in the order the planner emitted them. That is what §9.6
// specifies and it is the right choice here — the persisted set is what is being attested, and a
// verifier reading the rows back out of SQLite has no way to recover an insertion order. It is the
// opposite of strategy.BatchProposal.Canonical, which deliberately preserves planner order so that
// the determinism property can see an ordering bug; the two functions hash different things for
// different reasons and neither should be "unified" with the other.
//
// prev is nil for the first batch in a pool, matching prev_hash's NULL at seq = 1.
func BatchHash(prev []byte, batch BatchRow, entries []EntryRow) ([]byte, error) {
	sorted := make([]EntryRow, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	inputs := make([]entryHashInput, len(sorted))
	for i, e := range sorted {
		inputs[i] = entryHashInput{
			ID:           e.ID.String(),
			BatchID:      e.BatchID.String(),
			PoolID:       e.PoolID.String(),
			Seq:          e.Seq,
			AccountID:    e.AccountID.String(),
			CharacterID:  ulidPtrString(e.CharacterID),
			BalanceKind:  e.BalanceKind,
			AmountCp:     int64(e.AmountCp),
			ItemID:       ulidPtrString(e.ItemID),
			ItemAwardID:  ulidPtrString(e.ItemAwardID),
			RaidID:       ulidPtrString(e.RaidID),
			TickID:       ulidPtrString(e.TickID),
			MetadataJSON: e.MetadataJSON,
		}
	}

	batchJSON, err := canonicalJSON(batchHashInput{
		ID:                 batch.ID.String(),
		PoolID:             batch.PoolID.String(),
		Seq:                batch.Seq,
		Kind:               batch.Kind,
		StrategyID:         batch.StrategyID,
		StrategyVersion:    batch.StrategyVersion,
		ConfigSnapshotJSON: batch.ConfigSnapshotJSON,
		RngSeed:            batch.RngSeed,
		Source:             batch.Source,
		SourceRef:          batch.SourceRef,
		ActorUserID:        ulidPtrString(batch.ActorUserID),
		ActorTokenID:       ulidPtrString(batch.ActorTokenID),
		ActorIsBeneficiary: batch.ActorIsBeneficiary,
		Reason:             batch.Reason,
		ReversesBatchID:    ulidPtrString(batch.ReversesBatchID),
		EffectiveAt:        int64(batch.EffectiveAt),
		RecordedAt:         int64(batch.RecordedAt),
		EffectiveDay:       batch.EffectiveDay,
		IdempotencyKey:     batch.IdempotencyKey,
		EntryCount:         batch.EntryCount,
		NetAmountCp:        int64(batch.NetAmountCp),
	})
	if err != nil {
		return nil, fmt.Errorf("hash batch %s: %w", batch.ID, err)
	}

	entriesJSON, err := canonicalJSON(inputs)
	if err != nil {
		return nil, fmt.Errorf("hash entries of batch %s: %w", batch.ID, err)
	}

	h := sha256.New()
	h.Write(prev)
	h.Write(batchJSON)
	h.Write(entriesJSON)

	return h.Sum(nil), nil
}

// AuditHash computes the instance-wide chain link for one audit row. prev is nil at seq = 1.
func AuditHash(prev []byte, row AuditRow) ([]byte, error) {
	rowJSON, err := canonicalJSON(auditHashInput{
		ID:            row.ID.String(),
		Seq:           row.Seq,
		At:            int64(row.At),
		ActorKind:     row.ActorKind,
		ActorLabel:    row.ActorLabel,
		Action:        row.Action,
		ResourceKind:  row.ResourceKind,
		ResourceID:    row.ResourceID,
		Outcome:       row.Outcome,
		LedgerBatchID: ulidPtrString(row.LedgerBatchID),
	})
	if err != nil {
		return nil, fmt.Errorf("hash audit row %s: %w", row.ID, err)
	}

	h := sha256.New()
	h.Write(prev)
	h.Write(rowJSON)

	return h.Sum(nil), nil
}

// canonicalJSON marshals v deterministically: declaration-order fields, no HTML escaping, no
// trailing newline. The same three properties strategy.BatchProposal.Canonical relies on, and for
// the same reason — a digest whose input depends on a marshalling default is a digest that changes
// when somebody upgrades a library.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("canonicalise for hashing: %w", err)
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ulidPtrString converts an optional ULID to the optional string the hash input and the generated
// params both want. nil in, nil out — a missing pointer must hash as JSON null and not as "".
func ulidPtrString(u *core.ULID) *string {
	if u == nil {
		return nil
	}

	s := u.String()

	return &s
}

// encodeHead renders a chain head for storage in dkp_meta, whose value column is TEXT.
//
// Hex rather than base64: it is what an officer sees when they open the database with sqlite3 at
// 1 a.m., it sorts and diffs sensibly, and it is the encoding `dkp verify-ledger` will print. The
// cost is 64 characters instead of 44, for a value there is exactly one of per pool.
func encodeHead(hash []byte) string { return hex.EncodeToString(hash) }

// decodeHead parses a chain head read back out of dkp_meta.
//
// A head that is present but unparseable, or the wrong length, is an ERROR rather than a silent
// "start a new chain". Treating it as absent would let a corrupted meta row quietly fork the chain
// at the next commit — which is precisely the event the chain exists to make visible.
func decodeHead(value string) ([]byte, error) {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode chain head %q: %w", value, err)
	}

	if len(raw) != hashLength {
		return nil, fmt.Errorf("chain head %q is %d bytes, want %d", value, len(raw), hashLength)
	}

	return raw, nil
}
