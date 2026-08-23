package idempotency_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	auditkinds "github.com/prokopto-dev/dragonkillparty/internal/audit/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/idempotency"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// The record's own tests, against real SQLite. The HTTP shape of every outcome below is
// internal/api/idempotency_test.go's; this file is about the decision and the row.

// fixture is a service over a migrated database and a fake clock, plus a request builder so each
// test names only the field it is varying.
type fixture struct {
	svc   *idempotency.Service
	clock *clock.Fake
	store *store.Store
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	st := store.NewDB(t)
	clk := clock.NewFake(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))

	return fixture{svc: idempotency.NewService(st, clk), clock: clk, store: st}
}

// request is one fenced POST. The defaults are a realistic fenced create; a test overrides what it
// is about.
func request(body string) idempotency.Request {
	return idempotency.Request{
		PrincipalRef: "service_account:01JZ0BOT0000000000000001",
		Key:          "01JZ0IDEMPOTENCYKEY0000001",
		Method:       http.MethodPost,
		PathTemplate: "/api/v1/raids/{raid_id}/ticks",
		Path:         "/api/v1/raids/01JZ0RAID000000000000001/ticks",
		Body:         []byte(body),
	}
}

// recorded is a plausible response for the request above.
func recorded() idempotency.Recorded {
	return idempotency.Recorded{
		Status:  http.StatusCreated,
		Headers: map[string]string{"Content-Type": "application/json", "ETag": `"abc"`},
		Body:    []byte(`{"created":[{"id":"01JZ0TICK000000000000001"}]}`),
	}
}

// TestBegin_FirstRequest_ClaimsTheKey. The base case: nothing holds the key, so this request runs.
func TestBegin_FirstRequest_ClaimsTheKey(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	decision, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeExecute, decision.Outcome)
	require.NotEmpty(t, decision.RecordID, "an execute decision must name the claim it took")
}

// TestBegin_SecondIdenticalRequest_WhileInFlight_IsRefused is the concurrency answer in its
// sequential form: the first request has not completed, so the second must not execute.
func TestBegin_SecondIdenticalRequest_WhileInFlight_IsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeExecute, first.Outcome)

	second, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeInFlight, second.Outcome,
		"a duplicate arriving while the original executes must wait, never run")
	require.Empty(t, second.RecordID, "only an execute decision names a claim")
}

// TestBegin_AfterCompletion_ReplaysTheRecordedResponse is the property the whole table exists for:
// the retry gets the ORIGINAL response, byte for byte, rather than a second effect.
func TestBegin_AfterCompletion_ReplaysTheRecordedResponse(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)

	require.NoError(t, f.svc.Complete(t.Context(), first.RecordID, recorded()))

	second, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeReplay, second.Outcome)
	require.Equal(t, recorded(), second.Replay,
		"a replay is the original response — status, allowlisted headers and body — not a "+
			"reconstruction of it")
}

// TestBegin_SameKeyDifferentBody_IsReused. The key-generation bug, and it has to be loud: a client
// reusing one key for two operations would otherwise have the second silently discarded.
func TestBegin_SameKeyDifferentBody_IsReused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first, err := f.svc.Begin(t.Context(), request(`{"ticks":[{"value_centipoints":100}]}`))
	require.NoError(t, err)
	require.NoError(t, f.svc.Complete(t.Context(), first.RecordID, recorded()))

	second, err := f.svc.Begin(t.Context(), request(`{"ticks":[{"value_centipoints":200}]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeReused, second.Outcome)
	require.NotEmpty(t, second.OriginalRequestHash,
		"docs/api/errors.md promises meta.original_request_hash, and without it a bot author is "+
			"told the two differ with no way to see how")
}

// TestBegin_SameKeyDifferentBody_WhileInFlight_IsReusedNotInFlight pins the order decide() applies
// its two checks, which is a decision rather than an accident.
//
// A different body against an in-flight record could defensibly be 409 or 422. 422 is the true
// answer: the bodies differ and no amount of waiting changes that, so a 409 would send the client
// into a retry loop that can only ever end in the 422 it should have had immediately.
func TestBegin_SameKeyDifferentBody_WhileInFlight_IsReusedNotInFlight(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	_, err := f.svc.Begin(t.Context(), request(`{"ticks":[{"value_centipoints":100}]}`))
	require.NoError(t, err)

	second, err := f.svc.Begin(t.Context(), request(`{"ticks":[{"value_centipoints":200}]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeReused, second.Outcome,
		"a body mismatch is not something waiting can fix")
}

// TestBegin_SameKeyDifferentPath_IsReused is the hole canonical §7's wording leaves open, closed.
//
// §7 says the stored hash covers "method + path template + body", which alone makes two ticks on two
// DIFFERENT raids indistinguishable when their bodies match — so one reused key would replay the
// first and the second raid would silently receive nothing. A missed tick is a duplicated tick with
// the sign flipped, and it is the quieter of the two.
func TestBegin_SameKeyDifferentPath_IsReused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first := request(`{"ticks":[]}`)

	claim, err := f.svc.Begin(t.Context(), first)
	require.NoError(t, err)
	require.NoError(t, f.svc.Complete(t.Context(), claim.RecordID, recorded()))

	second := request(`{"ticks":[]}`)
	second.Path = "/api/v1/raids/01JZ0RAID000000000000002/ticks"

	decision, err := f.svc.Begin(t.Context(), second)
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeReused, decision.Outcome,
		"the same key on a different raid must be refused, not replayed onto the wrong raid")
}

// TestBegin_DifferentPrincipals_DoNotCollide. Keys are scoped to the principal, so two bots that
// happen to mint the same key are two independent operations (docs/api/idempotency-and-concurrency).
func TestBegin_DifferentPrincipals_DoNotCollide(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	_, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)

	other := request(`{"ticks":[]}`)
	other.PrincipalRef = "user:01JZ0USER000000000000001"

	decision, err := f.svc.Begin(t.Context(), other)
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeExecute, decision.Outcome,
		"two principals sharing a key by coincidence are two operations, not one")
}

// TestBegin_AfterAbandon_ExecutesAgain is the 5xx promise: "the record rolled back with the
// transaction; your retry executes normally".
func TestBegin_AfterAbandon_ExecutesAgain(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.NoError(t, f.svc.Abandon(t.Context(), first.RecordID))

	second, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeExecute, second.Outcome,
		"a released claim must let the retry run — leaving it in flight turns a transient server "+
			"fault into a day of 409s")
}

// TestBegin_AfterTTL_ReclaimsTheRecord. Records expire after 24 hours, and an expired one is
// reclaimed in place rather than blocking the key forever.
func TestBegin_AfterTTL_ReclaimsTheRecord(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.NoError(t, f.svc.Complete(t.Context(), first.RecordID, recorded()))

	f.clock.Advance(idempotency.TTL + time.Second)

	second, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeExecute, second.Outcome)
	require.NotEqual(t, first.RecordID, second.RecordID, "a reclaim is a new claim")
}

// TestBegin_ReclaimedRecord_CarriesNoStaleResponse is the half of the reclaim that a naive upsert
// gets wrong: overwriting the request columns while leaving the response columns behind would make
// the fresh in_flight claim replay the PREVIOUS request's response the moment anything read it.
func TestBegin_ReclaimedRecord_CarriesNoStaleResponse(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.NoError(t, f.svc.Complete(t.Context(), first.RecordID, recorded()))

	f.clock.Advance(idempotency.TTL + time.Second)

	_, err = f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)

	// The reclaimed record is in flight, so an immediate duplicate waits. If the previous response
	// had survived the reclaim this would be a replay of it instead.
	duplicate, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeInFlight, duplicate.Outcome)
}

// TestComplete_AfterTheClaimWasReclaimed_WritesNothing is why Complete is keyed on the record id.
//
// A slow request whose claim expired and was taken by somebody else must not overwrite the new
// claim with its own response — that would hand the second caller the first one's answer.
func TestComplete_AfterTheClaimWasReclaimed_WritesNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	stale, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)

	f.clock.Advance(idempotency.TTL + time.Second)

	fresh, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.NotEqual(t, stale.RecordID, fresh.RecordID)

	// The slow request finally finishes and records its response against a claim nobody holds.
	require.NoError(t, f.svc.Complete(t.Context(), stale.RecordID, recorded()))

	duplicate, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeInFlight, duplicate.Outcome,
		"the live claim is still in flight; a stale completion must not have marked it complete")
}

// TestBegin_HundredConcurrentDuplicates_ProduceOneEffect is the acceptance criterion, and the
// property docs/design/03-security.md §"Concurrency and idempotency" states as "100× replay → one
// effect".
//
// EXACTLY ONE EXECUTE. The other 99 are told to wait — none of them executes, and none of them
// errors. The mechanism is the single-writer pool plus the unique index: store.Tx takes the write
// lock at BEGIN, so the read and the claim cannot interleave, and every loser sees a live row.
func TestBegin_HundredConcurrentDuplicates_ProduceOneEffect(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	const duplicates = 100

	outcomes := make([]idempotency.Outcome, duplicates)
	errs := make([]error, duplicates)

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)

	start.Add(1)

	for i := range duplicates {
		done.Add(1)

		go func() {
			defer done.Done()

			start.Wait()

			decision, err := f.svc.Begin(context.Background(), request(`{"ticks":[]}`))
			outcomes[i], errs[i] = decision.Outcome, err
		}()
	}

	start.Done()
	done.Wait()

	executed := 0

	for i, outcome := range outcomes {
		require.NoError(t, errs[i], "duplicate %d errored", i)

		if outcome == idempotency.OutcomeExecute {
			executed++

			continue
		}

		require.Equal(t, idempotency.OutcomeInFlight, outcome,
			"duplicate %d got %s; a loser of the race waits, it does not run and does not fail", i, outcome)
	}

	require.Equal(t, 1, executed,
		"exactly one of %d concurrent duplicates may execute — that is the whole claim this table "+
			"makes, and %d executions is %d raid nights credited twice", duplicates, executed, executed-1)
}

// TestSweep_RemovesOnlyExpiredRecords. Retention, not correctness — Begin reclaims an expired record
// on its own — but a DELETE nobody has watched run is a DELETE nobody knows is bounded to the rows
// it names.
func TestSweep_RemovesOnlyExpiredRecords(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	old, err := f.svc.Begin(t.Context(), request(`{"ticks":[]}`))
	require.NoError(t, err)
	require.NoError(t, f.svc.Complete(t.Context(), old.RecordID, recorded()))

	f.clock.Advance(idempotency.TTL + time.Second)

	fresh := request(`{"ticks":[]}`)
	fresh.Key = "01JZ0IDEMPOTENCYKEY0000002"

	_, err = f.svc.Begin(t.Context(), fresh)
	require.NoError(t, err)

	removed, err := f.svc.Sweep(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(1), removed, "the sweep must take the expired record and only that one")

	// The live claim survived: a duplicate of it still waits.
	duplicate, err := f.svc.Begin(t.Context(), fresh)
	require.NoError(t, err)
	require.Equal(t, idempotency.OutcomeInFlight, duplicate.Outcome)
}

// TestService_NoStore_RefusesEveryOperation. The degraded boot state (canonical §13): the process is
// up so /healthz stays green, and a fenced request cannot be served — executing it unkeyed is the
// double-write the record exists to prevent, so every door reports ErrNoStore rather than nil.
func TestService_NoStore_RefusesEveryOperation(t *testing.T) {
	t.Parallel()

	svc := idempotency.NewService(nil, clock.NewFake(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)))

	_, err := svc.Begin(t.Context(), request(`{}`))
	require.ErrorIs(t, err, idempotency.ErrNoStore)

	require.ErrorIs(t, svc.Complete(t.Context(), core.ULID("01JZ0RECORD00000000000001"), recorded()),
		idempotency.ErrNoStore)
	require.ErrorIs(t, svc.Abandon(t.Context(), core.ULID("01JZ0RECORD00000000000001")),
		idempotency.ErrNoStore)

	_, err = svc.Sweep(t.Context())
	require.ErrorIs(t, err, idempotency.ErrNoStore)
}

// TestPrincipalRef_NamesTheSubjectAndNeverTheToken pins canonical §7's hardest sentence: the key is
// scoped to the service account or the user, NEVER the token.
//
// A token-scoped key breaks rotation mid-retry — the retry would execute a second time — which is the
// guarantee docs/api/auth-and-scopes.md publishes, broken in the direction that writes twice.
func TestPrincipalRef_NamesTheSubjectAndNeverTheToken(t *testing.T) {
	t.Parallel()

	user, err := idempotency.PrincipalRef(auditkinds.ActorUser, core.ULID("01JZ0USER000000000000001"))
	require.NoError(t, err)
	require.Equal(t, "user:01JZ0USER000000000000001", user)

	bot, err := idempotency.PrincipalRef(auditkinds.ActorServiceAccount,
		core.ULID("01JZ0BOT0000000000000001"))
	require.NoError(t, err)
	require.Equal(t, "service_account:01JZ0BOT0000000000000001", bot)

	for _, tt := range []struct {
		name string
		kind string
		id   core.ULID
	}{
		{name: "token", kind: "token", id: core.ULID("01JZ0TOKEN00000000000001")},
		{name: "empty kind", kind: "", id: core.ULID("01JZ0USER000000000000001")},
		{name: "empty id", kind: auditkinds.ActorUser, id: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := idempotency.PrincipalRef(tt.kind, tt.id)
			require.ErrorIs(t, err, idempotency.ErrUnknownPrincipalKind)
		})
	}
}
