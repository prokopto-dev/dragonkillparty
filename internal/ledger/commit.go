package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The write path. Phase 0 PR 10a.
//
// Commit takes a planned proposal and makes it real: FIVE rows across five tables plus two chain
// heads, in ONE transaction. Nothing here is best-effort and nothing is deferred to a job.
//
//	ledger_batch      the economic event
//	ledger_entry      one row per (account, balance kind) delta
//	balance_snapshot  the droppable cache, upserted additively
//	audit_log         who did it, chained
//	event_outbox      the event, so a subscriber sees it iff it committed
//	dkp_meta          'ledger_head:<pool_id>' and 'audit_head'
//
// If any one of those fails the whole thing rolls back and none of them exists —
// TestCommit_FaultInjectedMidWrite_LeavesNothing injects a failure at every write position and
// asserts all five tables are empty afterwards. That is the property the outbox pattern exists for:
// a subscriber can never see an event for a batch that was rolled back, and can never miss one for a
// batch that committed, because there is no second write that could fail on its own.
//
// WHAT COMMIT DOES NOT DO. It does not plan. It does not decide whether a proposal is a good idea.
// It validates (invariant.go), orders (NextPoolSeq), attests (hashchain.go) and writes. The split is
// what lets a guild configure its own rules without being able to corrupt the ledger.

// Errors this file returns.
var (
	// ErrInvalidRequest is returned for a CommitRequest the caller has filled in wrongly — an empty
	// pool, an unknown source, an actor with no kind. It is separate from ErrInvariantViolated
	// because the two have different audiences: an invariant failure is shown to the officer whose
	// award was rejected, and this one is shown to whoever wrote the calling code.
	ErrInvalidRequest = errors.New("invalid commit request")
)

// The audit and outbox defaults a ledger commit uses when the caller does not override them. They
// are constants rather than literals at the call site so that the vocabulary is greppable: when
// Phase 2 adds the permission catalogue entry and Phase 1 adds the SSE topic registry, these are the
// two strings those lanes have to reconcile with.
const (
	// DefaultAuditAction is the permission key an ordinary batch commit records. It is the audit
	// log's `action` column, verbatim and never a rendered sentence.
	DefaultAuditAction = "ledger.batch.commit"

	// DefaultEventType is the outbox event a committed batch emits.
	DefaultEventType = "ledger.batch.committed"

	// auditResourceKind and eventResourceRefPrefix describe the committed batch to the audit log and
	// to a subscriber. The reference is a PATH, not a document: a payload copied into the outbox
	// goes stale and re-decides authorisation in a second place.
	auditResourceKind      = "ledger_batch"
	eventResourceRefPrefix = "/api/v1/ledger/batches/"

	// eventTopicPrefix scopes the event to its pool, so a subscriber watching one pool is not woken
	// by every other pool's traffic.
	eventTopicPrefix = "pool:"

	// metaLedgerHeadPrefix and metaAuditHead are the dkp_meta keys the two chains' heads live under
	// (docs/design/01-domain-model.md §9.6). The ledger head is per pool because the chain is.
	metaLedgerHeadPrefix = "ledger_head:"
	metaAuditHead        = "audit_head"
)

// NO ENUM VOCABULARY IS RESTATED IN THIS FILE. ledger_batch.kind and ledger_batch.source live in
// internal/ledger/kinds, audit_log.actor_kind lives in internal/audit/kinds, and both are the
// catalogues db/schema.hcl's CHECKs are generated from (canonical §5). validate below consults them
// through kinds.IsBatchKind, kinds.IsBatchSource and auditkinds.IsActorKind.
//
// A second list here was exactly the drift the catalogues remove, and it is worth naming both
// directions because they fail differently: a value added to the CHECK alone makes this function
// refuse a legal value with ErrInvalidRequest, and a value added to the list alone waves through a
// value SQLite rejects from inside the transaction — after the batch, its entries and its snapshots
// have been written and must be rolled back. `validSources` went in #29 and `validActorKinds` in #40.

// TxRunner is the transaction seam Commit writes through. *store.Store satisfies it as declared.
//
// An interface rather than a concrete *store.Store, and it buys exactly one thing that matters:
// TestCommit_FaultInjectedMidWrite_LeavesNothing can supply a runner that wraps the REAL Queries in
// a decorator which fails on the Nth write. That is fault injection over a real SQLite database, not
// a fake — .claude/rules/go-idioms.md's "no mocks of the database" stands, and the atomicity claim
// is only worth making if something has tried to break it.
//
// Declared by the consumer, small, satisfied implicitly (.claude/rules/go-idioms.md).
type TxRunner interface {
	Tx(ctx context.Context, fn func(context.Context, store.Queries) error) error
}

// Actor is who is committing, for the audit row.
//
// UserID and TokenID are nullable TEXT with no foreign key: app_user and api_token are Phase 2
// tables. A Phase 0 caller leaves both nil and sets Kind to auditkinds.ActorSystem, which is the
// truth — there is no authentication yet, and recording an invented user id in the one table whose
// entire value is that it is not invented would be the worst possible place to start.
type Actor struct {
	// Kind is audit_log.actor_kind. Required, and named through internal/audit/kinds
	// (auditkinds.ActorSystem, not "system") — that package is the catalogue the column's CHECK is
	// generated from, so a constant is a compile error when it is wrong where a literal is a runtime
	// one.
	Kind string
	// Label is the denormalised display name, kept so the row survives the actor's deletion.
	Label string
	// UserID and TokenID are the acting human and the acting bot token, when there are any.
	UserID  *core.ULID
	TokenID *core.ULID
}

// CommitRequest is everything Commit needs that the proposal does not carry.
//
// The split is deliberate: the Proposal holds what the STRATEGY decided (which accounts, how much,
// under what config, from what seed) and this holds what the REQUEST decided (which pool, who asked,
// through what channel, under what idempotency key). A strategy cannot set the second group, which
// is why they are different types rather than one wide struct.
type CommitRequest struct {
	// PoolID scopes the batch. Required.
	PoolID core.ULID

	// Proposal is the planned batch. Required, and non-empty.
	Proposal strategy.BatchProposal

	// Source is ledger_batch.source: where the request came from. Required.
	Source string

	// SourceRef is the addressable receipt this batch derives from ('tick_credit:<ulid>'), unique
	// per pool when present. It is the OTHER idempotency mechanism — ux_batch_srcref makes a
	// fan-out that re-derives the same receipt a constraint violation rather than a duplicate batch.
	SourceRef *string

	// IdempotencyKey makes a retry a no-op returning the first batch. Uniqueness is
	// (pool_id, idempotency_key) and it is NOT scoped by token: a token rotated between the first
	// attempt and the retry must still replay.
	IdempotencyKey *string

	// Actor is who is committing. Actor.Kind is required.
	Actor Actor

	// Action overrides the audit row's permission key. Empty means DefaultAuditAction.
	Action string

	// EventType overrides the outbox event type. Empty means DefaultEventType.
	EventType string

	// Topic overrides the outbox topic. Empty means "pool:<pool_id>".
	Topic string
}

// Receipt is what a caller gets back: enough to answer "what did I just write?" without a re-read.
//
// On a REPLAY (Replayed == true) only the batch fields are populated. AuditSeq and EventSeq are zero,
// because the original audit and outbox rows are not re-read — they belong to the first attempt, and
// fetching them would add two statements to the hot path of every bot retry to answer a question no
// retrying bot asks. A caller that needs them can query by BatchID.
type Receipt struct {
	BatchID     core.ULID
	Seq         int64
	PrevHash    []byte
	Hash        []byte
	EntryCount  int64
	NetAmountCp core.Centipoints
	AuditSeq    int64
	EventSeq    int64

	// Replayed reports that the idempotency key had already been used and nothing was written.
	Replayed bool
}

// BatchRow is the persisted ledger_batch shape: the ledger package's own type, so that a caller
// never touches internal/store/sqlitegen and so that the hash input (hashchain.go) is defined
// against a shape somebody edits on purpose rather than one sqlc derives from a column order.
type BatchRow struct {
	ID                 core.ULID
	PoolID             core.ULID
	Seq                int64
	Kind               string
	StrategyID         string
	StrategyVersion    string
	ConfigSnapshotJSON string
	RngSeed            *int64
	Source             string
	SourceRef          *string
	ActorUserID        *core.ULID
	ActorTokenID       *core.ULID
	ActorIsBeneficiary int64
	Reason             string
	ReversesBatchID    *core.ULID
	EffectiveAt        core.Micros
	RecordedAt         core.Micros
	EffectiveDay       string
	IdempotencyKey     *string
	EntryCount         int64
	NetAmountCp        core.Centipoints
	PrevHash           []byte
	Hash               []byte
}

// EntryRow is the persisted ledger_entry shape. pool_id and seq are denormalised from the batch,
// which is what lets BalanceAsOfSeq be answered from ix_entry_balance with no join.
type EntryRow struct {
	ID           core.ULID
	BatchID      core.ULID
	PoolID       core.ULID
	Seq          int64
	AccountID    core.ULID
	CharacterID  *core.ULID
	BalanceKind  string
	AmountCp     core.Centipoints
	ItemID       *core.ULID
	ItemAwardID  *core.ULID
	RaidID       *core.ULID
	TickID       *core.ULID
	MetadataJSON string
}

// AuditRow is the persisted audit_log shape, minimal at this phase for the reasons db/schema.hcl
// gives: every omitted column is either a FK to a Phase 2 table or a forensic field whose only
// writer is the Phase 2 HTTP middleware.
type AuditRow struct {
	ID            core.ULID
	Seq           int64
	At            core.Micros
	ActorKind     string
	ActorLabel    string
	Action        string
	ResourceKind  string
	ResourceID    *string
	Outcome       string
	LedgerBatchID *core.ULID
	PrevHash      []byte
	Hash          []byte
}

// Service commits planned batches. It holds a transaction runner, an id generator and a clock — and
// no context, per .claude/rules/go-idioms.md.
type Service struct {
	tx    TxRunner
	ids   *core.Generator
	clock clock.Clock
}

// NewService wires a commit service. The clock is injected because recorded_at, the audit row's
// timestamp and every minted ULID's timestamp prefix all come from it, and a test that could not
// control those could not assert on any of them.
func NewService(tx TxRunner, c clock.Clock) *Service {
	return &Service{tx: tx, ids: core.NewGenerator(c), clock: c}
}

// Commit validates, orders, hashes and writes a proposal — five rows and two chain heads, in one
// transaction.
//
// The order inside the transaction is not arbitrary:
//
//  1. the idempotency lookup, so a retry costs one indexed read and writes nothing;
//  2. NextPoolSeq, which both allocates the seq and (as next-1) tells the invariant engine the head
//     it must read balances at — one query answering two questions rather than two answering one
//     each, which also removes the window in which they could disagree;
//  3. the invariants, BEFORE anything is written, so a rejection costs no rollback;
//  4. the five writes and the two heads.
//
// Every read in here is on the transaction's Queries, not on store.Q(). A read outside the
// transaction could observe a different snapshot from the one the write lands in, and "the balance
// I checked is not the balance I wrote against" is the bug class this whole file exists to preclude.
func (s *Service) Commit(ctx context.Context, req CommitRequest) (Receipt, error) {
	if err := validate(req); err != nil {
		return Receipt{}, err
	}

	var receipt Receipt

	err := s.tx.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		if req.IdempotencyKey != nil {
			replay, found, err := findReplay(ctx, q, req)
			if err != nil {
				return err
			}

			if found {
				receipt = replay

				return nil
			}
		}

		written, err := s.write(ctx, q, req)
		if err != nil {
			return err
		}

		receipt = written

		return nil
	})
	if err != nil {
		return Receipt{}, err
	}

	if !receipt.Replayed {
		slog.InfoContext(ctx, "committed ledger batch",
			"batch_id", receipt.BatchID, "pool_id", req.PoolID, "seq", receipt.Seq,
			"entries", receipt.EntryCount, "net_amount_cp", receipt.NetAmountCp,
			"audit_seq", receipt.AuditSeq, "event_seq", receipt.EventSeq)
	}

	return receipt, nil
}

// findReplay returns the receipt for an already-committed batch carrying this idempotency key.
//
// The lookup is (pool_id, idempotency_key), served by the partial unique index ux_batch_idem. It is
// deliberately NOT scoped by token or by actor: domain model §15 is explicit that a principal's
// idempotency key must replay across a token rotation ("NEVER 'token:<ulid>': rotation mid-retry
// must replay"), because a bot whose token rolled over between the attempt and the retry is exactly
// the case idempotency exists for. TestCommit_DuplicateIdempotencyKey_ReturnsFirstBatch rotates the
// token between the two calls and requires the same batch id back.
//
// Scoping by PRINCIPAL — so that two different bots may each use the key "abc" — is Phase 2. It
// needs app_user and service_account to exist for the principal to be a thing, and until then there
// is exactly one principal per instance and the pool is the correct scope.
func findReplay(ctx context.Context, q store.Queries, req CommitRequest) (Receipt, bool, error) {
	row, err := q.GetBatchByIdempotencyKey(ctx, sqlitegen.GetBatchByIdempotencyKeyParams{
		PoolID:         req.PoolID.String(),
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Receipt{}, false, nil
		}

		return Receipt{}, false, fmt.Errorf("look up idempotency key in pool %s: %w", req.PoolID, err)
	}

	return Receipt{
		BatchID:     core.ULID(row.ID),
		Seq:         row.Seq,
		PrevHash:    row.PrevHash,
		Hash:        row.Hash,
		EntryCount:  row.EntryCount,
		NetAmountCp: core.Centipoints(row.NetAmountCp),
		Replayed:    true,
	}, true, nil
}

// write performs the five inserts and the two head updates. It runs only after the invariants have
// passed, so every failure from here down is an I/O failure rather than a rejection.
func (s *Service) write(ctx context.Context, q store.Queries, req CommitRequest) (Receipt, error) {
	seq, err := q.NextPoolSeq(ctx, req.PoolID.String())
	if err != nil {
		return Receipt{}, fmt.Errorf("allocate seq for pool %s: %w", req.PoolID, err)
	}

	ic := invariantCtx{q: q, poolID: req.PoolID, seq: seq, headSeq: seq - 1}
	if err := checkInvariants(ctx, ic, req.Proposal); err != nil {
		return Receipt{}, err
	}

	batch, entries, err := s.materialise(req, seq)
	if err != nil {
		return Receipt{}, err
	}

	prev, err := readHead(ctx, q, metaLedgerHeadPrefix+req.PoolID.String())
	if err != nil {
		return Receipt{}, err
	}

	batch.PrevHash = prev

	hash, err := BatchHash(prev, batch, entries)
	if err != nil {
		return Receipt{}, err
	}

	batch.Hash = hash

	// Write 1: the batch header.
	if err := q.InsertLedgerBatch(ctx, batchParams(batch)); err != nil {
		return Receipt{}, fmt.Errorf("insert batch %s: %w", batch.ID, err)
	}

	// Writes 2..N+1: the entries.
	for _, e := range entries {
		if err := q.InsertLedgerEntry(ctx, entryParams(e)); err != nil {
			return Receipt{}, fmt.Errorf("insert entry %s of batch %s: %w", e.ID, batch.ID, err)
		}
	}

	// Write N+2..: the snapshot cache, one upsert per (account, balance kind) rather than one per
	// entry. A batch that credits the same account twice in two kinds is two rows; twice in one kind
	// is one row carrying the summed delta, which is what the additive upsert expects.
	if err := upsertSnapshots(ctx, q, batch, entries); err != nil {
		return Receipt{}, err
	}

	auditRow, err := s.writeAudit(ctx, q, req, batch)
	if err != nil {
		return Receipt{}, err
	}

	eventSeq, err := s.writeOutboxEvent(ctx, q, req, batch)
	if err != nil {
		return Receipt{}, err
	}

	// The two heads, last. They are the derived values: written after the rows they describe so that
	// a reader of this function sees the same order the data depends on, and inside the same
	// transaction so a crash can never leave a head describing a batch that does not exist.
	now := core.FromTime(s.clock.Now())

	if err := writeHead(ctx, q, metaLedgerHeadPrefix+req.PoolID.String(), batch.Hash, now); err != nil {
		return Receipt{}, err
	}

	if err := writeHead(ctx, q, metaAuditHead, auditRow.Hash, now); err != nil {
		return Receipt{}, err
	}

	return Receipt{
		BatchID:     batch.ID,
		Seq:         batch.Seq,
		PrevHash:    batch.PrevHash,
		Hash:        batch.Hash,
		EntryCount:  batch.EntryCount,
		NetAmountCp: batch.NetAmountCp,
		AuditSeq:    auditRow.Seq,
		EventSeq:    eventSeq,
	}, nil
}

// materialise turns a proposal plus an allocated seq into the rows that will be written: it mints
// the ids, stamps recorded_at, derives effective_day and computes entry_count and net_amount_cp.
//
// EFFECTIVE_DAY IS UTC AT THIS PHASE, and that is a deferral rather than a decision. Domain model
// §9.1 specifies 'YYYY-MM-DD' guild-local, and the guild's timezone lives in guild.timezone — but
// resolving it means a read of the guild row inside every commit and a policy for what a batch
// backdated across a DST boundary means, both of which belong with the attendance work in Phase 1
// that first needs day buckets to line up with raid nights. UTC is correct for a guild in UTC and
// off by at most one day-boundary for everybody else, in a column nothing reads yet.
//
// ACTOR_IS_BENEFICIARY IS 0 AT THIS PHASE, likewise. Computing it means asking whether the acting
// principal owns any account this batch credits, and there is no principal-to-account mapping until
// person and app_user land in Phase 1/2. Writing 0 is honest — no batch committed by Phase 0 code
// has an authenticated beneficiary — where writing a guess would populate the self-dealing report
// with noise and teach officers to ignore it.
func (s *Service) materialise(req CommitRequest, seq int64) (BatchRow, []EntryRow, error) {
	p := req.Proposal

	batchID, err := s.ids.New()
	if err != nil {
		return BatchRow{}, nil, fmt.Errorf("mint batch id: %w", err)
	}

	net, ok := p.NetAmountCp()
	if !ok {
		// Unreachable: NoAmountOverflow has already run. Checked anyway, because "unreachable" is a
		// claim about today's call order and this value goes into a hash and a column.
		return BatchRow{}, nil, fmt.Errorf("batch %s: net amount overflows int64", batchID)
	}

	now := core.FromTime(s.clock.Now())

	batch := BatchRow{
		ID:                 batchID,
		PoolID:             req.PoolID,
		Seq:                seq,
		Kind:               p.Kind,
		StrategyID:         p.StrategyID,
		StrategyVersion:    p.StrategyVersion,
		ConfigSnapshotJSON: orEmptyJSON(p.ConfigSnapshotJSON),
		RngSeed:            p.RngSeed,
		Source:             req.Source,
		SourceRef:          req.SourceRef,
		ActorUserID:        req.Actor.UserID,
		ActorTokenID:       req.Actor.TokenID,
		ActorIsBeneficiary: 0,
		Reason:             p.Reason,
		ReversesBatchID:    p.ReversesBatchID,
		EffectiveAt:        p.EffectiveAt,
		RecordedAt:         now,
		EffectiveDay:       p.EffectiveAt.Time().Format(time.DateOnly),
		IdempotencyKey:     req.IdempotencyKey,
		EntryCount:         int64(len(p.Entries)),
		NetAmountCp:        net,
	}

	entries := make([]EntryRow, len(p.Entries))

	for i, e := range p.Entries {
		entryID, err := s.ids.New()
		if err != nil {
			return BatchRow{}, nil, fmt.Errorf("mint id for entry %d of batch %s: %w", i, batchID, err)
		}

		entries[i] = EntryRow{
			ID:           entryID,
			BatchID:      batchID,
			PoolID:       req.PoolID,
			Seq:          seq,
			AccountID:    e.AccountID,
			CharacterID:  e.CharacterID,
			BalanceKind:  e.BalanceKind,
			AmountCp:     e.AmountCp,
			ItemID:       e.ItemID,
			ItemAwardID:  e.ItemAwardID,
			RaidID:       e.RaidID,
			TickID:       e.TickID,
			MetadataJSON: orEmptyJSON(e.MetadataJSON),
		}
	}

	return batch, entries, nil
}

// writeAudit appends the audit row that says who committed this batch, chained to the previous one.
func (s *Service) writeAudit(
	ctx context.Context, q store.Queries, req CommitRequest, batch BatchRow,
) (AuditRow, error) {
	seq, err := q.NextAuditSeq(ctx)
	if err != nil {
		return AuditRow{}, fmt.Errorf("allocate audit seq: %w", err)
	}

	id, err := s.ids.New()
	if err != nil {
		return AuditRow{}, fmt.Errorf("mint audit row id: %w", err)
	}

	prev, err := readHead(ctx, q, metaAuditHead)
	if err != nil {
		return AuditRow{}, err
	}

	resourceID := batch.ID.String()
	batchID := batch.ID

	row := AuditRow{
		ID:            id,
		Seq:           seq,
		At:            core.FromTime(s.clock.Now()),
		ActorKind:     req.Actor.Kind,
		ActorLabel:    req.Actor.Label,
		Action:        orDefault(req.Action, DefaultAuditAction),
		ResourceKind:  auditResourceKind,
		ResourceID:    &resourceID,
		Outcome:       auditkinds.OutcomeSuccess,
		LedgerBatchID: &batchID,
		PrevHash:      prev,
	}

	hash, err := AuditHash(prev, row)
	if err != nil {
		return AuditRow{}, err
	}

	row.Hash = hash

	if err := q.InsertAuditLog(ctx, sqlitegen.InsertAuditLogParams{
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
		PrevHash:      row.PrevHash,
		Hash:          row.Hash,
	}); err != nil {
		return AuditRow{}, fmt.Errorf("insert audit row for batch %s: %w", batch.ID, err)
	}

	return row, nil
}

// writeOutboxEvent appends the event and returns the sequence SQLite allocated for it.
func (s *Service) writeOutboxEvent(
	ctx context.Context, q store.Queries, req CommitRequest, batch BatchRow,
) (int64, error) {
	id, err := s.ids.New()
	if err != nil {
		return 0, fmt.Errorf("mint outbox event id: %w", err)
	}

	eventSeq, err := q.InsertOutboxEvent(ctx, sqlitegen.InsertOutboxEventParams{
		ID:          id.String(),
		Topic:       orDefault(req.Topic, eventTopicPrefix+req.PoolID.String()),
		EventType:   orDefault(req.EventType, DefaultEventType),
		ResourceRef: eventResourceRefPrefix + batch.ID.String(),
		CreatedAt:   int64(core.FromTime(s.clock.Now())),
	})
	if err != nil {
		return 0, fmt.Errorf("insert outbox event for batch %s: %w", batch.ID, err)
	}

	return eventSeq, nil
}

// upsertSnapshots folds the batch's entries into the droppable balance cache, one additive upsert
// per (account, balance kind).
//
// It goes through UpsertBalanceSnapshot (PR 9's helper) rather than a bespoke insert, so the cache's
// semantics live in one place: on conflict the amount and the count are ADDED and the as-of-seq
// advances, which is what makes the cache equal a naive fold over every entry
// (TestSnapshot_TenThousandEntries_MatchesFold).
func upsertSnapshots(ctx context.Context, q store.Queries, batch BatchRow, entries []EntryRow) error {
	deltas := make(map[accountKind]SnapshotDelta, len(entries))
	order := make([]accountKind, 0, len(entries))

	for _, e := range entries {
		key := accountKind{accountID: e.AccountID, balanceKind: e.BalanceKind}

		d, seen := deltas[key]
		if !seen {
			order = append(order, key)

			d = SnapshotDelta{
				PoolID:      batch.PoolID,
				AccountID:   e.AccountID,
				BalanceKind: e.BalanceKind,
				AsOfSeq:     batch.Seq,
				UpdatedAt:   batch.RecordedAt,
			}
		}

		sum, ok := addCentipoints(d.AmountCp, e.AmountCp)
		if !ok {
			// Unreachable while NoAmountOverflow holds — a per-account subtotal cannot exceed the
			// batch total's magnitude — but the cache is the thing /standings reads, so a wrapped
			// value here would be visible to every member and derivable from nothing.
			return fmt.Errorf("snapshot delta for account %s kind %q in batch %s overflows int64",
				e.AccountID, e.BalanceKind, batch.ID)
		}

		d.AmountCp = sum
		d.EntryCount++
		deltas[key] = d
	}

	// Iterated in first-appearance order rather than by ranging the map: the upserts are statements,
	// and a statement order that changed run to run would make a budget or an EXPLAIN golden
	// meaningless. Entries arrive from the allocator in account order, so this is account order.
	for _, key := range order {
		if err := UpsertBalanceSnapshot(ctx, q, deltas[key]); err != nil {
			return err
		}
	}

	return nil
}

// readHead reads a chain head out of dkp_meta, returning nil when the chain has no links yet.
//
// A missing key means "this chain starts here" and is the normal case for the first batch in a pool;
// a key that is present but unparseable is an error (see decodeHead), because treating a corrupted
// head as absent would silently fork the chain at the next commit.
func readHead(ctx context.Context, q store.Queries, key string) ([]byte, error) {
	value, err := q.GetMetaValue(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, fmt.Errorf("read chain head %s: %w", key, err)
	}

	head, err := decodeHead(value)
	if err != nil {
		return nil, err
	}

	return head, nil
}

// writeHead advances a chain head in dkp_meta.
func writeHead(ctx context.Context, q store.Queries, key string, hash []byte, at core.Micros) error {
	if err := q.UpsertMetaValue(ctx, sqlitegen.UpsertMetaValueParams{
		Key:       key,
		Value:     encodeHead(hash),
		UpdatedAt: int64(at),
	}); err != nil {
		return fmt.Errorf("advance chain head %s: %w", key, err)
	}

	return nil
}

// batchParams maps a BatchRow onto the generated insert parameters.
func batchParams(b BatchRow) sqlitegen.InsertLedgerBatchParams {
	return sqlitegen.InsertLedgerBatchParams{
		ID:                 b.ID.String(),
		PoolID:             b.PoolID.String(),
		Seq:                b.Seq,
		Kind:               b.Kind,
		StrategyID:         b.StrategyID,
		StrategyVersion:    b.StrategyVersion,
		ConfigSnapshotJson: b.ConfigSnapshotJSON,
		RngSeed:            b.RngSeed,
		Source:             b.Source,
		SourceRef:          b.SourceRef,
		ActorUserID:        ulidPtrString(b.ActorUserID),
		ActorTokenID:       ulidPtrString(b.ActorTokenID),
		ActorIsBeneficiary: b.ActorIsBeneficiary,
		Reason:             b.Reason,
		ReversesBatchID:    ulidPtrString(b.ReversesBatchID),
		EffectiveAt:        int64(b.EffectiveAt),
		RecordedAt:         int64(b.RecordedAt),
		EffectiveDay:       b.EffectiveDay,
		IdempotencyKey:     b.IdempotencyKey,
		EntryCount:         b.EntryCount,
		NetAmountCp:        int64(b.NetAmountCp),
		PrevHash:           b.PrevHash,
		Hash:               b.Hash,
	}
}

// entryParams maps an EntryRow onto the generated insert parameters.
func entryParams(e EntryRow) sqlitegen.InsertLedgerEntryParams {
	return sqlitegen.InsertLedgerEntryParams{
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
		MetadataJson: e.MetadataJSON,
	}
}

// validate rejects a request the caller has filled in wrongly, before a transaction is opened.
//
// The enum checks restate CHECK constraints that SQLite would also enforce, and the duplication
// earns its keep: a constraint violation from inside a transaction names a column and a table, while
// this names the request field and lists the legal values — and it does so without having taken the
// single write connection first.
func validate(req CommitRequest) error {
	if req.PoolID == "" {
		return fmt.Errorf("empty pool id: %w", ErrInvalidRequest)
	}

	if !kinds.IsBatchSource(req.Source) {
		return fmt.Errorf("source %q is not one of %s: %w",
			req.Source, strings.Join(kinds.BatchSources(), ", "), ErrInvalidRequest)
	}

	if !auditkinds.IsActorKind(req.Actor.Kind) {
		return fmt.Errorf("actor kind %q is not one of %s: %w",
			req.Actor.Kind, strings.Join(auditkinds.ActorKinds(), ", "), ErrInvalidRequest)
	}

	if req.Proposal.Kind == "" {
		return fmt.Errorf("empty batch kind on the proposal: %w", ErrInvalidRequest)
	}

	// A planner sets Kind as a string literal, so a typo — or a kind a new strategy invented and
	// never added to the catalogue — is valid Go all the way to the INSERT, where SQLite rejects it
	// from inside the transaction. Checking membership here is what turns that into a named error
	// against a named field, before the write connection is taken.
	if !kinds.IsBatchKind(req.Proposal.Kind) {
		return fmt.Errorf("batch kind %q is not one of %s: %w",
			req.Proposal.Kind, strings.Join(kinds.BatchKinds(), ", "), ErrInvalidRequest)
	}

	if req.Proposal.StrategyID == "" || req.Proposal.StrategyVersion == "" {
		return fmt.Errorf("proposal is missing a strategy id or version: %w", ErrInvalidRequest)
	}

	if req.IdempotencyKey != nil && *req.IdempotencyKey == "" {
		// A present-but-empty key is worse than none: ux_batch_idem is a partial index on
		// `idempotency_key IS NOT NULL`, so "" is a real value and the SECOND batch to use it would
		// be rejected as a duplicate of an unrelated first one.
		return fmt.Errorf("idempotency key is present but empty; pass nil for no key: %w", ErrInvalidRequest)
	}

	return nil
}

// orEmptyJSON substitutes the schema default for an unset JSON column. The column is NOT NULL with
// DEFAULT '{}', and the insert names every column explicitly (so that a value the database invented
// is never a value the hash did not cover) — which means the default has to be applied here.
func orEmptyJSON(s string) string {
	if s == "" {
		return "{}"
	}

	return s
}

// orDefault returns value, or fallback when value is empty.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}

	return value
}
