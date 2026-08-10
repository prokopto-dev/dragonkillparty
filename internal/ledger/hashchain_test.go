package ledger_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
)

// sampleBatch is a fully-populated BatchRow, used by the tests below as the thing to perturb.
//
// Fully populated on purpose: a hash test built from a mostly-zero struct cannot tell whether a
// field is covered, because changing a field that was never set to anything is invisible. Every
// nullable column here carries a value so that TestHashChain_EveryFieldIsCovered can null it out and
// watch the hash move.
func sampleBatch() ledger.BatchRow {
	seed := int64(42)
	sourceRef := "tick_credit:" + padID("TICK", 1)
	actorUser := core.ULID(padID("USER", 1))
	actorToken := core.ULID(padID("TOKEN", 1))
	reverses := core.ULID(padID("BATCH", 9))
	idem := "idem-1"

	return ledger.BatchRow{
		ID:                 core.ULID(padID("BATCH", 1)),
		PoolID:             ledger.DefaultPoolID,
		Seq:                7,
		Kind:               "award",
		StrategyID:         "zero_sum",
		StrategyVersion:    "1.2.3",
		ConfigSnapshotJSON: `{"decay_bp":500}`,
		RngSeed:            &seed,
		Source:             "api",
		SourceRef:          &sourceRef,
		ActorUserID:        &actorUser,
		ActorTokenID:       &actorToken,
		ActorIsBeneficiary: 1,
		Reason:             "Naggy split",
		ReversesBatchID:    &reverses,
		EffectiveAt:        core.Micros(1_717_243_200_000_000),
		RecordedAt:         core.Micros(1_717_243_260_000_000),
		EffectiveDay:       "2024-06-01",
		IdempotencyKey:     &idem,
		EntryCount:         2,
		NetAmountCp:        0,
	}
}

// sampleEntries returns two entries for sampleBatch, with ids that are NOT in sorted order, so that
// the "hashed in id order" property has something to sort.
func sampleEntries() []ledger.EntryRow {
	character := core.ULID(padID("CHAR", 1))
	item := core.ULID(padID("ITEM", 1))

	return []ledger.EntryRow{
		{
			ID: core.ULID(padID("ENTRY", 2)), BatchID: core.ULID(padID("BATCH", 1)),
			PoolID: ledger.DefaultPoolID, Seq: 7,
			AccountID: ledger.AccountIDGuildBank, BalanceKind: "dkp", AmountCp: -100,
			MetadataJSON: "{}",
		},
		{
			ID: core.ULID(padID("ENTRY", 1)), BatchID: core.ULID(padID("BATCH", 1)),
			PoolID: ledger.DefaultPoolID, Seq: 7,
			AccountID: testAccountID(0), CharacterID: &character, BalanceKind: "dkp", AmountCp: 100,
			ItemID: &item, MetadataJSON: `{"note":"main"}`,
		},
	}
}

// TestHashChain_SameInput_SameHash is the base property: hashing is a function.
//
// Trivial-looking and load-bearing. The chain is only worth anything if recomputing it from the
// stored rows reproduces the stored hash — that recomputation IS `dkp verify-ledger`, and if it
// depended on map ordering or a marshalling default it would report drift on an untouched database.
func TestHashChain_SameInput_SameHash(t *testing.T) {
	t.Parallel()

	a, err := ledger.BatchHash(nil, sampleBatch(), sampleEntries())
	require.NoError(t, err)

	b, err := ledger.BatchHash(nil, sampleBatch(), sampleEntries())
	require.NoError(t, err)

	require.Equal(t, a, b)
	require.Len(t, a, 32, "SHA-256 is 32 raw bytes; the column is a BLOB, not hex text")
}

// TestHashChain_EntryOrder_DoesNotChangeTheHash pins that entries are hashed in ID ORDER.
//
// Domain model §9.6 specifies `canonical_json(entries ORDER BY id)`, and the reason is a verifier's:
// a job reading the rows back out of SQLite gets them in whatever order the query plan produced, and
// has no way to recover the order they were inserted in. A hash that depended on insertion order
// would be unverifiable — which is to say, not a hash chain.
func TestHashChain_EntryOrder_DoesNotChangeTheHash(t *testing.T) {
	t.Parallel()

	forward := sampleEntries()
	reversed := []ledger.EntryRow{forward[1], forward[0]}

	a, err := ledger.BatchHash(nil, sampleBatch(), forward)
	require.NoError(t, err)

	b, err := ledger.BatchHash(nil, sampleBatch(), reversed)
	require.NoError(t, err)

	require.Equal(t, a, b)
}

// TestHashChain_EveryFieldIsCovered perturbs each field in turn and requires the hash to move.
//
// This is the test that makes the chain mean "nothing in this batch changed" rather than "nothing in
// the four fields somebody remembered changed". A column omitted from the hash input is a column an
// attacker — or a buggy migration — can alter with the chain still verifying, and the omission is
// invisible in review because the code that hashes and the code that inserts are different lists.
func TestHashChain_EveryFieldIsCovered(t *testing.T) {
	t.Parallel()

	base, err := ledger.BatchHash(nil, sampleBatch(), sampleEntries())
	require.NoError(t, err)

	otherSeed := int64(43)
	otherRef := "tick_credit:" + padID("TICK", 2)
	otherULID := core.ULID(padID("OTHER", 1))
	otherKey := "idem-2"

	batchCases := map[string]func(*ledger.BatchRow){
		"id":                   func(b *ledger.BatchRow) { b.ID = otherULID },
		"pool_id":              func(b *ledger.BatchRow) { b.PoolID = otherULID },
		"seq":                  func(b *ledger.BatchRow) { b.Seq = 8 },
		"kind":                 func(b *ledger.BatchRow) { b.Kind = "adjustment" },
		"strategy_id":          func(b *ledger.BatchRow) { b.StrategyID = "fixed_price" },
		"strategy_version":     func(b *ledger.BatchRow) { b.StrategyVersion = "1.2.4" },
		"config_snapshot_json": func(b *ledger.BatchRow) { b.ConfigSnapshotJSON = `{"decay_bp":501}` },
		"rng_seed":             func(b *ledger.BatchRow) { b.RngSeed = &otherSeed },
		"rng_seed to null":     func(b *ledger.BatchRow) { b.RngSeed = nil },
		"source":               func(b *ledger.BatchRow) { b.Source = "web" },
		"source_ref":           func(b *ledger.BatchRow) { b.SourceRef = &otherRef },
		"source_ref to null":   func(b *ledger.BatchRow) { b.SourceRef = nil },
		"actor_user_id":        func(b *ledger.BatchRow) { b.ActorUserID = &otherULID },
		"actor_token_id":       func(b *ledger.BatchRow) { b.ActorTokenID = &otherULID },
		"actor_is_beneficiary": func(b *ledger.BatchRow) { b.ActorIsBeneficiary = 0 },
		"reason":               func(b *ledger.BatchRow) { b.Reason = "Vox split" },
		"reverses_batch_id":    func(b *ledger.BatchRow) { b.ReversesBatchID = &otherULID },
		"effective_at":         func(b *ledger.BatchRow) { b.EffectiveAt++ },
		"recorded_at":          func(b *ledger.BatchRow) { b.RecordedAt++ },
		"effective_day":        func(b *ledger.BatchRow) { b.EffectiveDay = "2024-06-02" },
		"idempotency_key":      func(b *ledger.BatchRow) { b.IdempotencyKey = &otherKey },
		"entry_count":          func(b *ledger.BatchRow) { b.EntryCount = 3 },
		"net_amount_cp":        func(b *ledger.BatchRow) { b.NetAmountCp = 1 },
	}

	for name, mutate := range batchCases {
		t.Run("batch."+name, func(t *testing.T) {
			t.Parallel()

			b := sampleBatch()
			mutate(&b)

			got, err := ledger.BatchHash(nil, b, sampleEntries())
			require.NoError(t, err)
			require.NotEqual(t, base, got,
				"changing batch.%s must change the hash; a column outside the hash input is a column "+
					"that can be altered with the chain still verifying", name)
		})
	}

	entryCases := map[string]func(*ledger.EntryRow){
		"id":            func(e *ledger.EntryRow) { e.ID = otherULID },
		"batch_id":      func(e *ledger.EntryRow) { e.BatchID = otherULID },
		"pool_id":       func(e *ledger.EntryRow) { e.PoolID = otherULID },
		"seq":           func(e *ledger.EntryRow) { e.Seq = 8 },
		"account_id":    func(e *ledger.EntryRow) { e.AccountID = otherULID },
		"character_id":  func(e *ledger.EntryRow) { e.CharacterID = &otherULID },
		"balance_kind":  func(e *ledger.EntryRow) { e.BalanceKind = "ep" },
		"amount_cp":     func(e *ledger.EntryRow) { e.AmountCp++ },
		"item_id":       func(e *ledger.EntryRow) { e.ItemID = &otherULID },
		"item_award_id": func(e *ledger.EntryRow) { e.ItemAwardID = &otherULID },
		"raid_id":       func(e *ledger.EntryRow) { e.RaidID = &otherULID },
		"tick_id":       func(e *ledger.EntryRow) { e.TickID = &otherULID },
		"metadata_json": func(e *ledger.EntryRow) { e.MetadataJSON = `{"note":"alt"}` },
	}

	for name, mutate := range entryCases {
		t.Run("entry."+name, func(t *testing.T) {
			t.Parallel()

			entries := sampleEntries()
			mutate(&entries[1])

			got, err := ledger.BatchHash(nil, sampleBatch(), entries)
			require.NoError(t, err)
			require.NotEqual(t, base, got, "changing entry.%s must change the hash", name)
		})
	}
}

// TestHashChain_PrevHash_IsPartOfTheDigest is what makes it a CHAIN rather than a set of independent
// checksums. Without it, batches could be reordered or one could be excised entirely and every
// remaining hash would still verify.
func TestHashChain_PrevHash_IsPartOfTheDigest(t *testing.T) {
	t.Parallel()

	genesis, err := ledger.BatchHash(nil, sampleBatch(), sampleEntries())
	require.NoError(t, err)

	linked, err := ledger.BatchHash(genesis, sampleBatch(), sampleEntries())
	require.NoError(t, err)

	require.NotEqual(t, genesis, linked,
		"the same batch at a different chain position must hash differently, or batches could be "+
			"reordered or removed with every remaining hash still verifying")

	// An empty (non-nil) prev must hash identically to nil: both mean "no predecessor", and a chain
	// whose genesis depended on which of the two the caller passed would be a chain with two
	// different valid starting hashes.
	emptyPrev, err := ledger.BatchHash([]byte{}, sampleBatch(), sampleEntries())
	require.NoError(t, err)
	require.Equal(t, genesis, emptyPrev)
}

// TestAuditHash_EveryFieldIsCovered is the audit chain's version of the coverage test above. The
// audit chain is independent of the ledger's and instance-wide, so it gets its own assertion rather
// than being assumed to work because the other one does.
func TestAuditHash_EveryFieldIsCovered(t *testing.T) {
	t.Parallel()

	resourceID := padID("BATCH", 1)
	batchID := core.ULID(padID("BATCH", 1))

	base := ledger.AuditRow{
		ID: core.ULID(padID("AUDIT", 1)), Seq: 3, At: core.Micros(1_717_243_260_000_000),
		ActorKind: "user", ActorLabel: "Melisandre", Action: "ledger.batch.commit",
		ResourceKind: "ledger_batch", ResourceID: &resourceID, Outcome: "success",
		LedgerBatchID: &batchID,
	}

	baseHash, err := ledger.AuditHash(nil, base)
	require.NoError(t, err)

	other := padID("OTHER", 1)
	otherULID := core.ULID(other)

	cases := map[string]func(*ledger.AuditRow){
		"id":                   func(r *ledger.AuditRow) { r.ID = otherULID },
		"seq":                  func(r *ledger.AuditRow) { r.Seq = 4 },
		"at":                   func(r *ledger.AuditRow) { r.At++ },
		"actor_kind":           func(r *ledger.AuditRow) { r.ActorKind = "system" },
		"actor_label":          func(r *ledger.AuditRow) { r.ActorLabel = "somebody else" },
		"action":               func(r *ledger.AuditRow) { r.Action = "ledger.batch.reverse" },
		"resource_kind":        func(r *ledger.AuditRow) { r.ResourceKind = "pool" },
		"resource_id":          func(r *ledger.AuditRow) { r.ResourceID = &other },
		"resource_id to null":  func(r *ledger.AuditRow) { r.ResourceID = nil },
		"outcome":              func(r *ledger.AuditRow) { r.Outcome = "denied" },
		"ledger_batch_id":      func(r *ledger.AuditRow) { r.LedgerBatchID = &otherULID },
		"ledger_batch to null": func(r *ledger.AuditRow) { r.LedgerBatchID = nil },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			row := base
			mutate(&row)

			got, err := ledger.AuditHash(nil, row)
			require.NoError(t, err)
			require.NotEqual(t, baseHash, got, "changing audit.%s must change the hash", name)
		})
	}

	linked, err := ledger.AuditHash(baseHash, base)
	require.NoError(t, err)
	require.NotEqual(t, baseHash, linked, "prev_hash must be part of the audit digest too")
}

// TestHashChain_NullAndEmptyString_HashDifferently is the JSON-encoding trap, asserted rather than
// assumed.
//
// A nullable column that hashed as "" when NULL would make `source_ref = NULL` and
// `source_ref = ”` indistinguishable to the chain — so a row could be altered between those two
// states with the hash unchanged. It is exactly the mistake a helper that returned a bare string
// would make, which is why ulidPtrString returns a pointer.
func TestHashChain_NullAndEmptyString_HashDifferently(t *testing.T) {
	t.Parallel()

	empty := ""

	withNull := sampleBatch()
	withNull.SourceRef = nil

	withEmpty := sampleBatch()
	withEmpty.SourceRef = &empty

	a, err := ledger.BatchHash(nil, withNull, sampleEntries())
	require.NoError(t, err)

	b, err := ledger.BatchHash(nil, withEmpty, sampleEntries())
	require.NoError(t, err)

	require.NotEqual(t, a, b, "a NULL column and an empty-string column must not hash alike")
}
