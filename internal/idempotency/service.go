package idempotency

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/idempotency/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// TTL is how long a record answers for its key: 24 hours, from docs/api's "Records expire after 24
// hours" and domain model §22's retention row.
//
// It is a CEILING ON MEMORY, not a correctness parameter. What makes a retry safe is the real
// uniqueness constraint underneath every fenced operation; this bounds how long the cheap answer is
// available. A bot that reuses a key after a day writes again, which is why the published guidance is
// one key per logical operation rather than one per bot lifetime.
const TTL = 24 * time.Hour

// RetryAfter is what a caller colliding with an in-flight duplicate is told to wait, and it is the
// value docs/api/errors.md publishes for `idempotency_key_in_flight`.
const RetryAfter = time.Second

// Outcome is what Begin decided about a request presenting a key.
//
// FOUR VALUES, AND THE ZERO ONE REFUSES. OutcomeUndecided is what a Decision returned alongside an
// error carries, so a caller that ignored the error and switched on the outcome falls through to a
// branch that does not execute the handler. The default branch of a control that stands between a
// bot's retry and a second raid-night award is the one place a future edit must not be able to open
// by omission.
type Outcome int

const (
	// OutcomeUndecided is the zero value: nothing was decided, and nothing may be executed.
	OutcomeUndecided Outcome = iota

	// OutcomeExecute means this request claimed the key and must run. Exactly one concurrent
	// duplicate ever gets it.
	OutcomeExecute

	// OutcomeReplay means the key was already completed by an identical request; the recorded
	// response is returned instead of executing.
	OutcomeReplay

	// OutcomeReused means the key is held by a DIFFERENT request. 422 idempotency_key_reused: the
	// caller has a key-generation bug, and retrying will not fix it.
	OutcomeReused

	// OutcomeInFlight means an identical request holds the key and has not finished. 409
	// idempotency_key_in_flight with Retry-After.
	OutcomeInFlight
)

// String names the outcome for a log line. The unknown case is spelled out rather than falling back
// to the numeric value, because an outcome nobody added a name for is a bug worth reading.
func (o Outcome) String() string {
	switch o {
	case OutcomeUndecided:
		return "undecided"
	case OutcomeExecute:
		return "execute"
	case OutcomeReplay:
		return "replay"
	case OutcomeReused:
		return "reused"
	case OutcomeInFlight:
		return "in_flight"
	default:
		return fmt.Sprintf("outcome(%d)", int(o))
	}
}

// Recorded is the response a completed record replays: the status, the allowlisted headers and the
// body, byte for byte.
//
// THE HEADERS ARE AN ALLOWLIST APPLIED BY THE CALLER, not the whole response header set, and the
// reason is in db/schema.hcl beside the column: replaying an arbitrary header set replays
// Set-Cookie, and a session cookie minted for one request must never be re-issued into another.
// internal/api decides which headers are worth replaying; this package stores what it is given.
type Recorded struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// Decision is Begin's answer.
//
// RecordID is set only for OutcomeExecute, and it is what Complete and Abandon are keyed on: it
// names THIS request's claim, so a claim that expired and was reclaimed by somebody else in the
// meantime cannot be completed with this request's response.
type Decision struct {
	Outcome  Outcome
	RecordID core.ULID

	// Replay carries the recorded response, for OutcomeReplay.
	Replay Recorded

	// OriginalRequestHash is the hex digest the key was claimed with, for OutcomeReused. It is
	// `meta.original_request_hash` in the 422 docs/api/errors.md publishes — the fingerprint of the
	// request that owns the key, so a bot author can see that the two differ without guessing.
	OriginalRequestHash string
}

// Service reads and writes idempotency records.
//
// It holds a store, a clock and an id generator and nothing else (.claude/rules/go-idioms.md): no
// ctx field, no mutable state, no cache. A cache is specifically wrong here — the record IS the
// synchronisation point between two concurrent requests, and a per-process memo of it would answer
// "not claimed" to the second one.
type Service struct {
	store *store.Store
	clock clock.Clock
	ids   *core.Generator
}

// NewService builds a Service. A nil clock is a programming error the caller fixes, not a default
// this package invents: every expiry in this table is measured from it, and a silently-substituted
// clock would give records a TTL nobody chose.
func NewService(st *store.Store, clk clock.Clock) *Service {
	return &Service{store: st, clock: clk, ids: core.NewGenerator(clk)}
}

// Begin decides what happens to a request presenting an Idempotency-Key, and claims the key when the
// answer is "execute".
//
// THE READ AND THE CLAIM ARE ONE TRANSACTION on the WRITE pool, and that is the whole mechanism.
// store.Tx runs on a pool capped at one connection whose DSN carries `_txlock=immediate`, so two
// concurrent duplicates queue at BEGIN rather than interleaving between "is this key taken" and
// "take it". One of them inserts; every other one reads a live row and is told to replay or to wait.
// That is what makes "100 replays, one effect" a property of the database rather than of timing.
//
// A record past its TTL is RECLAIMED IN PLACE rather than deleted first: the claim's ON CONFLICT arm
// overwrites the whole row, response columns included. Deleting and re-inserting would be two
// statements to the same end, and would leave a window in which the unique index protects nothing.
func (s *Service) Begin(ctx context.Context, req Request) (Decision, error) {
	if s.store == nil {
		return Decision{}, fmt.Errorf("begin idempotent request: %w", ErrNoStore)
	}

	now := core.FromTime(s.clock.Now())
	hash := req.RequestHash()

	var decision Decision

	err := s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		row, err := q.GetIdempotencyRecord(ctx, sqlitegen.GetIdempotencyRecordParams{
			PrincipalRef: req.PrincipalRef,
			Key:          req.Key,
		})

		switch {
		case err == nil && core.Micros(row.ExpiresAt) > now:
			decision, err = decide(row, hash)

			return err

		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read idempotency record: %w", err)
		}

		id, err := s.ids.New()
		if err != nil {
			return fmt.Errorf("mint idempotency record id: %w", err)
		}

		if err = q.ClaimIdempotencyRecord(ctx, sqlitegen.ClaimIdempotencyRecordParams{
			ID:           id.String(),
			PrincipalRef: req.PrincipalRef,
			Key:          req.Key,
			Method:       req.Method,
			PathTemplate: req.PathTemplate,
			RequestHash:  hash,
			State:        kinds.StateInFlight,
			CreatedAt:    int64(now),
			ExpiresAt:    int64(now.Add(TTL)),
		}); err != nil {
			return fmt.Errorf("claim idempotency record: %w", err)
		}

		decision = Decision{Outcome: OutcomeExecute, RecordID: id}

		return nil
	})
	if err != nil {
		return Decision{}, fmt.Errorf("begin idempotent request: %w", err)
	}

	return decision, nil
}

// decide reads a LIVE record and says what the incoming request gets.
//
// THE HASH IS COMPARED BEFORE THE STATE, and the order is a decision rather than an accident. A
// different body against an in-flight record could defensibly be either 409 or 422; 422 is the true
// one, because the bodies differ and no amount of waiting changes that — answering 409 would send a
// client into a retry loop that can only ever end in the 422 it should have had immediately.
func decide(row sqlitegen.IdempotencyRecord, hash []byte) (Decision, error) {
	if !bytes.Equal(row.RequestHash, hash) {
		return Decision{
			Outcome:             OutcomeReused,
			OriginalRequestHash: hex.EncodeToString(row.RequestHash),
		}, nil
	}

	if row.State == kinds.StateInFlight {
		return Decision{Outcome: OutcomeInFlight}, nil
	}

	if row.ResponseStatus == nil {
		// A complete record with no status is a row this package cannot have written. Reporting it is
		// the only honest answer: replaying a zero status would answer 200 with an empty body, which
		// is the one response shape this API must never produce, and treating it as unclaimed would
		// execute a request whose original effect already landed.
		return Decision{}, fmt.Errorf("idempotency record %s is complete with no recorded status: %w",
			row.ID, errCorruptRecord)
	}

	headers, err := decodeHeaders(row.ResponseHeadersJson)
	if err != nil {
		return Decision{}, fmt.Errorf("idempotency record %s: %w", row.ID, err)
	}

	return Decision{
		Outcome: OutcomeReplay,
		Replay: Recorded{
			Status:  int(*row.ResponseStatus),
			Headers: headers,
			Body:    row.ResponseBody,
		},
	}, nil
}

// errCorruptRecord is the fault behind a record that cannot be replayed or re-executed. Unexported:
// nothing outside this package can act on it differently from any other server fault, and it is a
// sentinel rather than an inline errors.New so a log grep finds every occurrence.
var errCorruptRecord = errors.New("corrupt idempotency record")

// Complete records the response the claimed request produced, so every later duplicate replays it.
//
// It is keyed on the record id, so a claim that expired and was reclaimed by a different request in
// the meantime is not overwritten: the UPDATE matches nothing and this returns nil. That is the
// correct outcome rather than an error — the claim this request held is gone, its response has
// nowhere to live, and the next duplicate re-executing is exactly what an expired key means.
func (s *Service) Complete(ctx context.Context, recordID core.ULID, rec Recorded) error {
	if s.store == nil {
		return fmt.Errorf("complete idempotent request: %w", ErrNoStore)
	}

	headers, err := encodeHeaders(rec.Headers)
	if err != nil {
		return fmt.Errorf("complete idempotent request %s: %w", recordID, err)
	}

	status := int64(rec.Status)

	if err := s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		return q.CompleteIdempotencyRecord(ctx, sqlitegen.CompleteIdempotencyRecordParams{
			ID:                  recordID.String(),
			State:               kinds.StateComplete,
			ResponseStatus:      &status,
			ResponseHeadersJson: headers,
			ResponseBody:        rec.Body,
		})
	}); err != nil {
		return fmt.Errorf("complete idempotent request %s: %w", recordID, err)
	}

	return nil
}

// Abandon deletes a claim whose request produced no recordable response — a 5xx, or a panic.
//
// ABSENCE IS HOW THIS TABLE SAYS "RETRY ME", and docs/api/idempotency-and-concurrency.md promises
// exactly that: after a 5xx the caller's retry executes normally. Leaving the row in_flight would
// answer every retry with 409 for the next 24 hours — a transient server fault turned into a day of
// refusals, on the endpoint a raid night depends on.
func (s *Service) Abandon(ctx context.Context, recordID core.ULID) error {
	if s.store == nil {
		return fmt.Errorf("abandon idempotent request: %w", ErrNoStore)
	}

	if err := s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		return q.AbandonIdempotencyRecord(ctx, recordID.String())
	}); err != nil {
		return fmt.Errorf("abandon idempotent request %s: %w", recordID, err)
	}

	return nil
}

// Sweep deletes every record past its TTL and returns how many went, for the retention job.
//
// IT IS RETENTION, NOT CORRECTNESS. Begin reclaims an expired record in place, so a table nobody
// swept still answers every request correctly — it just grows. This is the statement the scheduled
// prune calls; until the job runner exists (Phase 3) its caller is `dkp` operator tooling and this
// package's own test, which is the honest shape: a DELETE nobody has watched run is a DELETE nobody
// knows is bounded to the rows it names.
func (s *Service) Sweep(ctx context.Context) (int64, error) {
	if s.store == nil {
		return 0, fmt.Errorf("sweep idempotency records: %w", ErrNoStore)
	}

	var removed int64

	if err := s.store.Tx(ctx, func(ctx context.Context, q store.Queries) error {
		n, err := q.DeleteExpiredIdempotencyRecords(ctx, int64(core.FromTime(s.clock.Now())))
		if err != nil {
			return err
		}

		removed = n

		return nil
	}); err != nil {
		return 0, fmt.Errorf("sweep idempotency records: %w", err)
	}

	return removed, nil
}

// encodeHeaders renders the replayable headers as the JSON object the column holds. A nil or empty
// map becomes `{}` rather than `null`, so the column's NOT NULL DEFAULT and what this writes agree.
func encodeHeaders(headers map[string]string) (string, error) {
	if len(headers) == 0 {
		return "{}", nil
	}

	encoded, err := json.Marshal(headers)
	if err != nil {
		return "", fmt.Errorf("encode response headers: %w", err)
	}

	return string(encoded), nil
}

// decodeHeaders reads the column back. An empty string is treated as `{}` rather than as a parse
// failure: a row written before this column existed cannot occur (the table ships with it), but a
// replay that failed on an empty string would turn a cosmetic gap into a 500 on a request whose
// original effect already landed.
func decodeHeaders(encoded string) (map[string]string, error) {
	if encoded == "" || encoded == "{}" {
		return nil, nil
	}

	var headers map[string]string

	if err := json.Unmarshal([]byte(encoded), &headers); err != nil {
		return nil, fmt.Errorf("decode response headers: %w", err)
	}

	return headers, nil
}
