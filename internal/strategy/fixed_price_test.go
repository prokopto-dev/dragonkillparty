package strategy_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// fixed_price, tested at the STRATEGY level. Phase 0 PR 10b.
//
// internal/ledger's property_test.go proves the arithmetic in isolation: P2 over the allocator, P5
// over Negated, P8 over a two-line planner. This file proves the same three properties over a REAL
// STRATEGY, which is a different claim — a planner can hold a correct allocator and still lose a
// centipoint by rounding its own debit, emit entries in a map's iteration order, or carry the
// original's effective time onto a reversal.
//
// ON IMPORTING internal/ledger FROM THIS TEST. It is deliberate and it is what the seam requires. The
// shipped package may not import it (law 3, transitively — arch_test.go proves so), but the
// ledger-side implementations of the two injected seams are the only real ones that exist:
// ledger.NewRng is the only strategy.Rng, ledger.Allocate is the only allocator, and
// ledger.SystemAccountIDs holds the ids the four system-account keys resolve to. Testing a façade
// against a hand-written stand-in for all three would prove the strategy agrees with the test's idea
// of the ledger, which is the one thing nobody needs to know.

// updateGolden regenerates the committed canonical proposals. It is REFUSED under CI: a golden that
// CI can rewrite proves nothing, because the run that would have caught a changed proposal is the run
// that overwrites the evidence.
var updateGolden = flag.Bool("update", false, "regenerate the fixed_price proposal goldens (refused under CI)")

// goldenDir is where the canonical proposals live. Under test/golden/ rather than beside this file
// because that tree is CODEOWNERS-protected and is gated against shrinking.
const goldenDir = "../../test/golden/strategy/fixed_price"

// fixedNow is the instant every fake clock here is pinned to: 2024-06-01T12:00:00Z, mid-day UTC so
// that an effective_day derived from it is unambiguous. It is the same instant internal/ledger's
// commit tests use, so a proposal from here and a batch from there describe the same night.
var fixedNow = core.Micros(1_717_243_200_000_000)

// acct builds the deterministic account id for index i.
//
// Order-preserving as well as deterministic: acct(i) sorts before acct(j) exactly when i < j, which
// is what lets a test assert that entries come out in account order rather than in input order. The
// alphabet is legal Crockford base32 (no I, L, O or U) and the length is core.ULIDLength.
func acct(i int) core.ULID { return core.ULID(fmt.Sprintf("0000000000000000ACCT%06d", i)) }

// fakeCtx is the read-only façade under test control: an in-memory pool.
//
// Everything it cannot answer in memory it delegates to the REAL implementation — Allocate to
// ledger.Allocate, SystemAccount to ledger.SystemAccountIDs, Rng to ledger.NewRng. See the file
// header for why.
type fakeCtx struct {
	poolID     core.ULID
	headSeq    int64
	configJSON string
	balances   map[core.ULID]core.Centipoints
	roster     []strategy.AccountRef

	// earned is what Ctx.EarnedBetween reports each account was CREDITED, for the tests that need one
	// number rather than a history. It is SEPARATE from balances for the reason the façade method
	// exists: what an account earned in a slice of the log and what it holds now are different facts,
	// and a fake that derived one from the other could not express the member who earned 500 last
	// year and has spent every point of it — the account `decay_window` must not push into debt.
	//
	// decay_window_test.go's ledgerCtx answers both from a real entry log instead, which is what the
	// no-double-expiry property needs; this map is the simpler half, for tests about one run.
	earned map[core.ULID]core.Centipoints

	// earnedSlices records the (from, to] bounds of every EarnedBetween call, so a test can assert
	// that the slice a run expired is the one the scheduler resolved and not the whole of history.
	earnedSlices [][2]int64

	// history is the accounts Ctx.HasHistory reports a ledger for. It is SEPARATE from balances, and
	// deliberately not derived from them, because the whole point of the façade method is that a zero
	// balance and an empty history are different facts — a fake that derived one from the other could
	// not express the veteran who earned and spent everything, which is the account start_points must
	// never grant to (P7).
	history map[core.ULID]bool

	// balanceErr, rosterErr, systemErr, historyErr and earnedErr make each façade read fail on
	// demand, so the planners' error paths are exercised against a façade that fails the way a
	// database does.
	balanceErr error
	rosterErr  error
	systemErr  error
	historyErr error
	earnedErr  error

	// allocateErr makes the shared allocator fail on demand.
	//
	// It is the one façade method a planner can call with arguments it has ALREADY validated — every
	// allocating strategy here checks the weights and the amount before handing them over — so the
	// refusal it returns is unreachable through any event a test can construct. That does not make the
	// planner's handling of it dead: the allocator's contract is internal/ledger's to change, and a
	// planner that ignored an error from it would build a batch out of a nil credit list. This is how
	// that branch is watched executing rather than assumed correct.
	allocateErr error

	// readAtSeq records the seq of every Balance call. Balances are POSITIONAL, and a planner that
	// read "current" instead of "as of the run's seq" would pass every value assertion while being
	// wrong the moment a batch commits mid-run.
	readAtSeq []int64

	// rng counts its own use. fixed_price must consume no randomness at all, and a counter is how
	// that is asserted rather than assumed.
	rng *countingRng
}

// newCtx builds a façade with n person accounts, all at the given opening balance.
func newCtx(tb testing.TB, n int, opening core.Centipoints, configJSON string) *fakeCtx {
	tb.Helper()

	c := &fakeCtx{
		poolID:     ledger.DefaultPoolID,
		headSeq:    7,
		configJSON: configJSON,
		balances:   map[core.ULID]core.Centipoints{},
		earned:     map[core.ULID]core.Centipoints{},
		history:    map[core.ULID]bool{},
		rng:        &countingRng{inner: ledger.NewRng(42)},
	}

	for i := range n {
		id := acct(i)
		c.balances[id] = opening
		c.roster = append(c.roster, strategy.AccountRef{
			ID: id, Kind: "person", Label: fmt.Sprintf("Raider %d", i),
		})
	}

	for key, id := range ledger.SystemAccountIDs() {
		c.roster = append(c.roster, strategy.AccountRef{ID: id, Kind: "system", SystemKey: key})
	}

	return c
}

func (c *fakeCtx) PoolID() core.ULID  { return c.poolID }
func (c *fakeCtx) HeadSeq() int64     { return c.headSeq }
func (c *fakeCtx) Clock() clock.Clock { return clock.NewFake(fixedNow.Time()) }
func (c *fakeCtx) Rng() strategy.Rng  { return c.rng }
func (c *fakeCtx) ConfigJSON() string { return c.configJSON }
func (c *fakeCtx) Roster() ([]strategy.AccountRef, error) {
	if c.rosterErr != nil {
		return nil, c.rosterErr
	}

	return c.roster, nil
}

func (c *fakeCtx) Balance(account core.ULID, balanceKind string, asOfSeq int64) (core.Centipoints, error) {
	c.readAtSeq = append(c.readAtSeq, asOfSeq)

	if c.balanceErr != nil {
		return 0, c.balanceErr
	}

	if balanceKind != strategy.BalanceKindDKP {
		return 0, fmt.Errorf("fake ctx: unknown balance kind %q", balanceKind)
	}

	return c.balances[account], nil
}

func (c *fakeCtx) EarnedBetween(
	account core.ULID, balanceKind string, fromSeq, toSeq int64,
) (core.Centipoints, error) {
	c.earnedSlices = append(c.earnedSlices, [2]int64{fromSeq, toSeq})

	if c.earnedErr != nil {
		return 0, c.earnedErr
	}

	if balanceKind != strategy.BalanceKindDKP {
		return 0, fmt.Errorf("fake ctx: unknown balance kind %q", balanceKind)
	}

	return c.earned[account], nil
}

func (c *fakeCtx) HasHistory(account core.ULID, balanceKind string, asOfSeq int64) (bool, error) {
	c.readAtSeq = append(c.readAtSeq, asOfSeq)

	if c.historyErr != nil {
		return false, c.historyErr
	}

	if balanceKind != strategy.BalanceKindDKP {
		return false, fmt.Errorf("fake ctx: unknown balance kind %q", balanceKind)
	}

	return c.history[account], nil
}

func (c *fakeCtx) SystemAccount(systemKey string) (core.ULID, error) {
	if c.systemErr != nil {
		return "", c.systemErr
	}

	id, ok := ledger.SystemAccountIDs()[systemKey]
	if !ok {
		return "", fmt.Errorf("fake ctx: no system account %q", systemKey)
	}

	return id, nil
}

func (c *fakeCtx) Allocate(
	total core.Centipoints, shares []strategy.Share, emptyAccount core.ULID,
) ([]strategy.Allocation, error) {
	if c.allocateErr != nil {
		return nil, c.allocateErr
	}

	return ledger.Allocate(total, shares, emptyAccount)
}

// countingRng wraps the real seeded Rng and counts every call. fixed_price must never reach for it.
type countingRng struct {
	inner *ledger.Rng
	calls int
}

func (r *countingRng) Seed() int64 { r.calls++; return r.inner.Seed() }
func (r *countingRng) IntN(n int) int {
	r.calls++

	return r.inner.IntN(n)
}

func (r *countingRng) Shuffle(n int, swap func(i, j int)) {
	r.calls++

	r.inner.Shuffle(n, swap)
}

// shares builds an even split across the first n accounts.
func shares(n int) []strategy.Share {
	out := make([]strategy.Share, n)
	for i := range out {
		out[i] = strategy.Share{AccountID: acct(i), Weight: 1}
	}

	return out
}

// sumEntries totals a proposal's entries. Written out rather than reusing NetAmountCp so that a bug
// in NetAmountCp cannot make a zero-sum assertion pass by agreeing with itself.
func sumEntries(p strategy.BatchProposal) core.Centipoints {
	var sum core.Centipoints
	for _, e := range p.Entries {
		sum += e.AmountCp
	}

	return sum
}

// --- The planners, one example each -------------------------------------------------------------

// TestFixedPrice_PlanAward_DebitsTheBuyerAndCreditsTheBank is the default configuration: a sink
// economy, where the price leaves circulation.
func TestFixedPrice_PlanAward_DebitsTheBuyerAndCreditsTheBank(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 10_000, `{"default_price_cp": 2500}`)

	p, err := strategy.FixedPrice{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		EffectiveAt: fixedNow,
		Reason:      "Nagafen loot",
	})
	require.NoError(t, err)

	require.Equal(t, "award", p.Kind)
	require.Equal(t, "fixed_price", p.StrategyID)
	require.Len(t, p.Entries, 2, "one debit on the buyer, one credit on the bank")
	require.Equal(t, core.Centipoints(-2500), p.Entries[0].AmountCp)
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(2500), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	// The provenance pointer reaches every entry, so a member's statement can say which item moved
	// their points — including the bank's side of it.
	require.NotNil(t, p.Entries[0].ItemID)
	require.Equal(t, acct(90), *p.Entries[0].ItemID)
	require.NotNil(t, p.Entries[1].ItemID)

	// No split ran, so the strategy does not claim one was exact.
	require.NotContains(t, invariantKinds(p), strategy.InvariantLargestRemainderSumsToDebit,
		"a single credit is not a largest-remainder allocation and must not claim to be one")
}

// TestFixedPrice_PlanAward_SplitsTheProceedsExactly is the redistributing configuration, on a price
// that does not divide evenly: 1000 across 3 attendees is 333, 333, 334.
//
// The uneven price is the whole test. Three equal credits of 333 would be an obvious bug; three of
// 334 would mint two centipoints; and rounding each credit independently produces one or the other
// on nearly every real award.
func TestFixedPrice_PlanAward_SplitsTheProceedsExactly(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 10_000, `{"default_price_cp": 1000, "proceeds": "attendees"}`)

	p, err := strategy.FixedPrice{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		Beneficiaries: shares(3),
		EffectiveAt:   fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 4, "one debit plus three credits")
	require.Equal(t, core.Centipoints(-1000), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	require.Equal(t, acct(0), p.Entries[1].AccountID,
		"the buyer is an attendee and receives their own share back; excluding them would be a "+
			"tax rather than a zero-sum split, and a different DKP system")

	amounts := make([]core.Centipoints, 0, 3)
	for _, e := range p.Entries[1:] {
		amounts = append(amounts, e.AmountCp)
	}

	require.Equal(t, []core.Centipoints{334, 333, 333}, amounts,
		"largest remainder awards the spare centipoint to the lowest account id, deterministically")
	require.Contains(t, invariantKinds(p), strategy.InvariantLargestRemainderSumsToDebit)
}

// TestFixedPrice_PlanAward_NoBeneficiaries_RoutesToTheSoloPolicyAccount is the degenerate case that
// must route rather than drop.
//
// A solo kill has nobody to split across. The buyer still pays; the question is only where the
// points land, and a system account is the answer that keeps conservation verifiable.
func TestFixedPrice_PlanAward_NoBeneficiaries_RoutesToTheSoloPolicyAccount(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		config string
		want   core.ULID
	}{
		{"guild_bank", `{"default_price_cp": 500, "proceeds": "attendees"}`, ledger.AccountIDGuildBank},
		{
			"write_off",
			`{"default_price_cp": 500, "proceeds": "attendees", "solo_policy": "write_off"}`,
			ledger.AccountIDWriteOff,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p, err := strategy.FixedPrice{}.PlanAward(newCtx(t, 1, 10_000, tc.config), strategy.AwardEvent{
				Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:        strategy.ItemRef{Name: "Rotted"},
				EffectiveAt: fixedNow,
			})
			require.NoError(t, err)

			require.Len(t, p.Entries, 2)
			require.Equal(t, tc.want, p.Entries[1].AccountID)
			require.Equal(t, core.Centipoints(500), p.Entries[1].AmountCp)
			require.Equal(t, core.Centipoints(0), sumEntries(p))

			// The item carried no id, so the provenance pointer is absent rather than an empty
			// string in a nullable column.
			require.Nil(t, p.Entries[0].ItemID)
		})
	}
}

// TestFixedPrice_PlanAward_PriceResolution_PrefersTheOfficerThenTheItemThenTheConfig pins the order
// of the three sources, including that each really does override the one below it.
func TestFixedPrice_PlanAward_PriceResolution_PrefersTheOfficerThenTheItemThenTheConfig(t *testing.T) {
	t.Parallel()

	catalogue := core.Centipoints(200)
	officer := core.Centipoints(300)

	for _, tc := range []struct {
		name string
		ev   strategy.AwardEvent
		want core.Centipoints
	}{
		{
			name: "config default",
			ev:   strategy.AwardEvent{Item: strategy.ItemRef{Name: "Plain"}},
			want: 100,
		},
		{
			name: "item catalogue price beats the config",
			ev:   strategy.AwardEvent{Item: strategy.ItemRef{Name: "Priced", FixedPriceCp: &catalogue}},
			want: 200,
		},
		{
			name: "an officer's price beats both",
			ev: strategy.AwardEvent{
				Item:    strategy.ItemRef{Name: "Priced", FixedPriceCp: &catalogue},
				PriceCp: &officer,
			},
			want: 300,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := tc.ev
			ev.Buyer = strategy.AccountRef{ID: acct(0), Kind: "person"}
			ev.EffectiveAt = fixedNow

			p, err := strategy.FixedPrice{}.PlanAward(
				newCtx(t, 1, 10_000, `{"default_price_cp": 100}`), ev)
			require.NoError(t, err)
			require.Equal(t, -tc.want, p.Entries[0].AmountCp)
		})
	}
}

// TestFixedPrice_PlanAttendance_CreditsEachAttendeeFromTheBank is the earning half of the model.
func TestFixedPrice_PlanAttendance_CreditsEachAttendeeFromTheBank(t *testing.T) {
	t.Parallel()

	tick := core.ULID(acct(80))

	p, err := strategy.FixedPrice{}.PlanAttendance(
		newCtx(t, 3, 0, `{"tick_award_cp": 150}`), strategy.AttendanceEvent{
			Attendees:   shares(3),
			TickID:      &tick,
			EffectiveAt: fixedNow,
			Reason:      "Vox, tick 3",
		})
	require.NoError(t, err)

	require.Equal(t, "attendance", p.Kind)
	require.Len(t, p.Entries, 4, "the bank's debit plus three credits")
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-450), p.Entries[0].AmountCp)

	for _, e := range p.Entries[1:] {
		require.Equal(t, core.Centipoints(150), e.AmountCp)
		require.NotNil(t, e.TickID, "attribution reaches every entry")
	}

	require.Equal(t, core.Centipoints(0), sumEntries(p))
	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(p),
		"nobody's balance decreases and the bank is exempt from floors, so NonNegative would "+
			"constrain nothing and is deliberately not declared")
}

// TestFixedPrice_PlanAttendance_WeightIsAMultiplierAndZeroIsDropped covers the two things a weight
// can do that a flat tick cannot.
func TestFixedPrice_PlanAttendance_WeightIsAMultiplierAndZeroIsDropped(t *testing.T) {
	t.Parallel()

	p, err := strategy.FixedPrice{}.PlanAttendance(
		newCtx(t, 3, 0, `{"tick_award_cp": 100}`), strategy.AttendanceEvent{
			Attendees: []strategy.Share{
				{AccountID: acct(0), Weight: 2},
				{AccountID: acct(1), Weight: 0},
				{AccountID: acct(2), Weight: 1},
			},
			EffectiveAt: fixedNow,
		})
	require.NoError(t, err)

	require.Len(t, p.Entries, 3, "the zero-weight attendee is dropped, not written as a zero entry")
	require.Equal(t, core.Centipoints(-300), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(200), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(100), p.Entries[2].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestFixedPrice_PlanAdjustment_MovesPointsAgainstACounterparty asserts an adjustment is two entries
// and never one.
func TestFixedPrice_PlanAdjustment_MovesPointsAgainstACounterparty(t *testing.T) {
	t.Parallel()

	p, err := strategy.FixedPrice{}.PlanAdjustment(newCtx(t, 2, 1_000, ""), strategy.AdjustmentEvent{
		Account:     strategy.AccountRef{ID: acct(0), Kind: "person"},
		AmountCp:    -250,
		EffectiveAt: fixedNow,
		Reason:      "double-credited tick on 2024-05-30",
	})
	require.NoError(t, err)

	require.Equal(t, "adjustment", p.Kind)
	require.Len(t, p.Entries, 2)
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-250), p.Entries[0].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID,
		"an adjustment with no named counterparty is funded by the guild bank, never minted")
	require.Equal(t, core.Centipoints(250), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))
}

// TestFixedPrice_PlanDecay_ReadsPositionallyAndFloorsTheAmount covers the three things decay must get
// right: the seq it reads at, the direction it rounds, and where the points go.
func TestFixedPrice_PlanDecay_ReadsPositionallyAndFloorsTheAmount(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"decay_bp": 1000}`)
	ctx.balances[acct(0)] = 1_005 // 10% is 100.5 -> 100, floored
	ctx.balances[acct(1)] = 9     // 10% is 0.9 -> 0, so no entry at all
	ctx.balances[acct(2)] = -500  // already negative: nothing to decay

	p, err := strategy.FixedPrice{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey:   "2024-06",
		AsOfSeq:     4,
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Equal(t, "decay", p.Kind)
	require.Equal(t, "decay 2024-06", p.Reason)
	require.Len(t, p.Entries, 2, "one decayed account plus the bank")
	require.Equal(t, acct(0), p.Entries[0].AccountID)
	require.Equal(t, core.Centipoints(-100), p.Entries[0].AmountCp,
		"decay floors: rounding to nearest would take a centipoint the rate did not ask for")
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[1].AccountID)
	require.Equal(t, core.Centipoints(100), p.Entries[1].AmountCp)
	require.Equal(t, core.Centipoints(0), sumEntries(p))

	require.NotEmpty(t, ctx.readAtSeq)

	for _, seq := range ctx.readAtSeq {
		require.Equal(t, int64(4), seq,
			"every balance must be read AT THE RUN'S SEQ. Reading the head would let a batch "+
				"committed mid-run change what the run decayed.")
	}
}

// TestFixedPrice_PlanDecay_UsesTheRosterWhenTheRunNamesNoAccounts covers the façade read the run
// falls back to, and that system accounts are excluded from it.
func TestFixedPrice_PlanDecay_UsesTheRosterWhenTheRunNamesNoAccounts(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 1_000, `{"decay_bp": 2500}`)

	// The guild bank holds a balance too. Decaying it would GROW the debt of the account that funds
	// every tick, which is why system accounts are skipped rather than merely uncommon.
	ctx.balances[ledger.AccountIDGuildBank] = 50_000

	p, err := strategy.FixedPrice{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2024-06", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	require.Len(t, p.Entries, 3, "two raiders decayed, one credit to the bank")
	require.Equal(t, core.Centipoints(-250), p.Entries[0].AmountCp)
	require.Equal(t, core.Centipoints(-250), p.Entries[1].AmountCp)
	require.Equal(t, ledger.AccountIDGuildBank, p.Entries[2].AccountID)
	require.Equal(t, core.Centipoints(500), p.Entries[2].AmountCp,
		"the bank receives the decayed points; it is not itself decayed")
}

// TestFixedPrice_PlanReversal_NegatesAndRestampsTheEffectiveTime covers the one thing a reversal must
// not inherit.
func TestFixedPrice_PlanReversal_NegatesAndRestampsTheEffectiveTime(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 5_000, `{"default_price_cp": 700}`)
	original, err := strategy.FixedPrice{}.PlanAward(ctx, strategy.AwardEvent{
		Buyer:       strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:        strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		EffectiveAt: fixedNow.Add(-72 * 60 * 60 * 1_000_000_000), // three days ago
	})
	require.NoError(t, err)

	batchID := acct(70)

	reversal, err := strategy.FixedPrice{}.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              batchID,
		Kind:            original.Kind,
		StrategyID:      original.StrategyID,
		StrategyVersion: original.StrategyVersion,
		EffectiveAt:     original.EffectiveAt,
		Entries:         original.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, strategy.KindReversal, reversal.Kind)
	require.NotNil(t, reversal.ReversesBatchID)
	require.Equal(t, batchID, *reversal.ReversesBatchID)
	require.Equal(t, fixedNow, reversal.EffectiveAt,
		"a reversal is a new economic event at the time it is decided; backdating it to the "+
			"original's effective time would silently rewrite every intermediate balance's meaning")
	require.NotEqual(t, original.EffectiveAt, reversal.EffectiveAt)

	require.Len(t, reversal.Entries, len(original.Entries))

	for i, e := range reversal.Entries {
		require.Equal(t, -original.Entries[i].AmountCp, e.AmountCp)
		require.Equal(t, original.Entries[i].ItemID, e.ItemID,
			"a reversal of an award for an item is still about that item")
	}
}

// TestFixedPrice_PlanReversal_DeclaresNoFloor_SoACorrectionIsAlwaysPostable is the regression test
// for a defect found in review of this PR.
//
// The scenario, which is an ordinary Tuesday for a volunteer officer:
//
//	an officer credits a tick to the wrong raider  ->  Alice +500
//	Alice spends it on an item                     ->  Alice 0
//	the officer reverses the erroneous tick        ->  Alice -500
//
// A NonNegative floor on that third batch makes the ledger REJECT it. The ledger is append-only —
// there is no UPDATE, no DELETE, and a reversal is the only repair primitive that exists — so the
// floor would not prevent the debt, it would prevent the CORRECTION, leaving a mistake everybody can
// see and nobody can fix. The negative balance is the correct outcome: Alice spent points she was
// never owed, and the ledger says so.
//
// The end-to-end half of this — that ledger.Commit really does accept the exact inverse after the
// spend, and really would have rejected it with the floor — is
// TestCommit_ReversalBelowTheFloor_IsAcceptedWhenNoFloorIsDeclared in internal/ledger, which needs a
// database this package may not have.
func TestFixedPrice_PlanReversal_DeclaresNoFloor_SoACorrectionIsAlwaysPostable(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, `{"tick_award_cp": 500, "floor_cp": 0}`)
	s := strategy.FixedPrice{}

	erroneous, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees:   []strategy.Share{{AccountID: acct(0), Weight: 1}},
		EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	// Alice spends it, so her balance is 0 and the exact inverse must take her below the floor.
	ctx.balances[acct(0)] = 0

	reversal, err := s.PlanReversal(ctx, strategy.LedgerBatch{
		ID:              acct(70),
		Kind:            erroneous.Kind,
		StrategyID:      erroneous.StrategyID,
		StrategyVersion: erroneous.StrategyVersion,
		EffectiveAt:     erroneous.EffectiveAt,
		Entries:         erroneous.Entries,
	})
	require.NoError(t, err)

	require.Equal(t, []strategy.InvariantKind{strategy.InvariantSumZero}, invariantKinds(reversal),
		"a reversal must declare SumZero and NOTHING else. A floor here does not stop a debt — it "+
			"stops the correction, and an append-only ledger has no other repair primitive.")

	// The inverse really is exact, and it really does go below the floor. Without both halves the
	// assertion above is a claim about a batch that never needed the exemption.
	var alice core.Centipoints

	for _, e := range reversal.Entries {
		if e.AccountID == acct(0) {
			alice += e.AmountCp
		}
	}

	require.Equal(t, core.Centipoints(-500), alice,
		"the reversal is the exact inverse of the erroneous credit")
	require.Negative(t, ctx.balances[acct(0)]+alice,
		"and it takes a spent-out account below zero, which is the case the floor would have blocked")
}

// TestFixedPrice_PlanReversal_ForeignBatch_IsRefused: a reversal must be planned by the strategy that
// planned the original, because only that strategy knows whether negation is the right inverse.
func TestFixedPrice_PlanReversal_ForeignBatch_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.FixedPrice{}.PlanReversal(newCtx(t, 1, 0, ""), strategy.LedgerBatch{
		ID:         acct(70),
		StrategyID: "suicide_kings",
		Entries: []strategy.EntryProposal{
			{AccountID: acct(0), BalanceKind: "sk_position", AmountCp: 3},
		},
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "suicide_kings")
}

// --- Spendable, Priority and the three refusals ---------------------------------------------------

// TestFixedPrice_Spendable_ReadsTheHeadSeq asserts the balance is read positionally at the pool head
// and is not adjusted by anything computed.
func TestFixedPrice_Spendable_ReadsTheHeadSeq(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 1_234, `{"decay_bp": 5000}`)

	got, err := strategy.FixedPrice{}.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)
	require.Equal(t, core.Centipoints(1_234), got,
		"decay is POSTED as batches, so it is already in the sum. A Spendable that also applied the "+
			"rate would apply it twice, invisibly.")
	require.Equal(t, []int64{7}, ctx.readAtSeq)
}

// TestFixedPrice_Priority_RanksBySpendableWithADeterministicTiebreak covers both halves of the rank.
func TestFixedPrice_Priority_RanksBySpendableWithADeterministicTiebreak(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 2, 0, "")
	ctx.balances[acct(0)] = 900
	ctx.balances[acct(1)] = 900

	first, err := strategy.FixedPrice{}.Priority(ctx, strategy.AccountRef{ID: acct(0)})
	require.NoError(t, err)

	second, err := strategy.FixedPrice{}.Priority(ctx, strategy.AccountRef{ID: acct(1)})
	require.NoError(t, err)

	require.Equal(t, int64(900), first.Rank)
	require.Equal(t, first.Rank, second.Rank)
	require.Less(t, first.Tiebreak, second.Tiebreak,
		"equal ranks must break on the account id, ascending — a random tiebreak would make two "+
			"replays of the same loot decision differ")
	require.Equal(t, "spendable balance", first.Reason)
}

// TestFixedPrice_BiddingMethods_ReturnErrUnsupported asserts the three optional methods refuse
// explicitly rather than returning a zero value that reads like an answer.
func TestFixedPrice_BiddingMethods_ReturnErrUnsupported(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 1, 0, "")
	s := strategy.FixedPrice{}

	hint, err := s.PriceHint(ctx, strategy.ItemRef{Name: "Cloak of Flames"})
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Nil(t, hint)
	require.ErrorContains(t, err, "fixed_price",
		"the refusal must name the strategy: it crosses a package boundary on its way to a 501")

	require.ErrorIs(t, s.ValidateBid(ctx, strategy.AccountRef{ID: acct(0)},
		strategy.Bid{AccountID: acct(0), AmountCp: 100, Sealed: true}), strategy.ErrUnsupported)

	resolution, err := s.SettleAuction(ctx, strategy.Session{ID: acct(60), SeqAtOpen: 7}, nil)
	require.ErrorIs(t, err, strategy.ErrUnsupported)
	require.Empty(t, resolution.Winners)
}

// TestFixedPrice_Identity_IsStableAndDeclared covers the three values written onto every batch.
func TestFixedPrice_Identity_IsStableAndDeclared(t *testing.T) {
	t.Parallel()

	s := strategy.FixedPrice{}

	require.Equal(t, "fixed_price", s.ID(),
		"the id is written onto every batch and is public API: renaming it orphans history")
	require.Equal(t, "0.1.0", s.Version())
	require.Equal(t, []string{"dkp"}, s.BalanceKinds())
	require.NotEmpty(t, s.Invariants(), "a strategy that declares no invariants is a red flag")

	// The schema is a copy: a caller that could mutate it could change what every pool validates
	// against.
	first, second := s.ConfigSchema(), s.ConfigSchema()
	require.JSONEq(t, string(first), string(second))
	first[0] = 'X'
	require.NotEqual(t, first[0], s.ConfigSchema()[0])
}

// --- Rejections ------------------------------------------------------------------------------

// TestFixedPrice_Planners_RejectUnplannableEvents is the table of everything a planner refuses.
//
// Each row is a mistake that would otherwise become a committed batch or a rejected one whose error
// names a row rather than a planner. They are grouped because the assertion is identical — the error
// must carry the right sentinel — and the value is in the coverage of the list, not in the shape.
func TestFixedPrice_Planners_RejectUnplannableEvents(t *testing.T) {
	t.Parallel()

	s := strategy.FixedPrice{}

	for _, tc := range []struct {
		name    string
		config  string
		plan    func(ctx strategy.Ctx) error
		wantErr error
	}{
		{
			name:   "award with no buyer",
			config: `{"default_price_cp": 100}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{Item: strategy.ItemRef{Name: "x"}})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a system account cannot buy",
			config: `{"default_price_cp": 100}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer: strategy.AccountRef{ID: ledger.AccountIDGuildBank, Kind: "system"},
					Item:  strategy.ItemRef{Name: "x"},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "an unpriced item is refused rather than awarded for nothing",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:  strategy.ItemRef{Name: "Unpriced"},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a negative beneficiary weight",
			config: `{"default_price_cp": 100, "proceeds": "attendees"}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{Name: "x"},
					Beneficiaries: []strategy.Share{{AccountID: acct(1), Weight: -1}},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "a beneficiary with no account",
			config: `{"default_price_cp": 100, "proceeds": "attendees"}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{Name: "x"},
					Beneficiaries: []strategy.Share{{Weight: 1}},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "attendance with no attendees",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "attendance whose override awards nothing",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				zero := core.Centipoints(0)
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: shares(2), AmountCp: &zero,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "attendance where every weight is zero",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{{AccountID: acct(0)}, {AccountID: acct(1)}},
				})

				return err
			},
			wantErr: strategy.ErrNothingToPlan,
		},
		{
			name:   "an attendance tick that overflows int64",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				huge := core.Centipoints(math.MaxInt64 / 2)
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{{AccountID: acct(0), Weight: 4}},
					AmountCp:  &huge,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "an attendance batch whose credits sum past int64",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				huge := core.Centipoints(math.MaxInt64 / 2)
				_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 1},
						{AccountID: acct(1), Weight: 1},
						{AccountID: acct(2), Weight: 1},
					},
					AmountCp: &huge,
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment with no account",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{AmountCp: 100})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment of zero",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account: strategy.AccountRef{ID: acct(0)},
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "adjustment against itself",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
					Account:      strategy.AccountRef{ID: acct(0)},
					AmountCp:     100,
					Counterparty: acct(0),
				})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "decay against a pool that does not decay",
			config: `{}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2024-06"})

				return err
			},
			wantErr: strategy.ErrInvalidConfig,
		},
		{
			name:   "decay with no period key",
			config: `{"decay_bp": 1000}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{})

				return err
			},
			wantErr: strategy.ErrInvalidEvent,
		},
		{
			name:   "decay that rounds to nothing for everybody",
			config: `{"decay_bp": 1}`,
			plan: func(ctx strategy.Ctx) error {
				_, err := s.PlanDecay(ctx, strategy.DecayRun{
					PeriodKey: "2024-06",
					Accounts:  []strategy.AccountRef{{ID: acct(0), Kind: "person"}},
				})

				return err
			},
			wantErr: strategy.ErrNothingToPlan,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tc.plan(newCtx(t, 3, 1, tc.config)), tc.wantErr)
		})
	}
}

// TestFixedPrice_Config_RejectsWhatTheSchemaWouldHaveRejected asserts the planner re-validates rather
// than defaulting.
//
// The API edge validates what a human typed into the settings form. This validates what actually
// reached the planner — which includes a config written by the importer, by a backfill or by a test.
// A planner that defaulted a bad value would run a DKP system nobody chose.
//
// EVERY PLANNER is run against every bad config, not just one. Config parsing is the first line of
// all five, and "all five call it" is exactly the kind of fact that stops being true when a sixth
// planner is added and copies the wrong one.
func TestFixedPrice_Config_RejectsWhatTheSchemaWouldHaveRejected(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{`,
		`{"proceeds": "the_officers_pocket"}`,
		`{"solo_policy": "delete_them"}`,
		`{"default_price_cp": -1}`,
		`{"tick_award_cp": 0}`,
		`{"decay_bp": 10001}`,
		`{"decay_bp": -5}`,

		// The three the SCHEMA rejects and a lax parser would not, all found in review of this PR.
		// A knob nobody typed correctly, a null knob, and a document that is not an object must not
		// read as "the defaults" — that is a pool silently running rules its officers did not choose.
		`{"decay_pb": 1000}`,
		`null`,

		// The null MEMBERS, which are the subtle ones: encoding/json treats a null decoded into a
		// non-pointer field as a no-op, so each of these used to read back as the default it was
		// preloaded with — 100, 0, "guild_bank", 0 — while the schema types admit no null at all.
		// One per Go kind in the struct, because the no-op is a property of the decode target.
		`{"tick_award_cp": null}`,
		`{"decay_bp": null}`,
		`{"proceeds": null}`,
		`{"floor_cp": null}`,
		`{"default_price_cp": 250, "solo_policy": null}`,
		`{"tick_award_cp":` + "\n" + `   null }`,

		// The rest of the not-an-object family, and the decimal that canonical §1 bans.
		`[]`,
		`"attendees"`,
		`{"decay_bp": 1000}{"decay_bp": 2000}`,
		`{"decay_bp": 1.5}`,
		`{"default_price_cp": "250"}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			for name, plan := range everyPlanner() {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					require.ErrorIs(t, plan(newCtx(t, 1, 0, config)), strategy.ErrInvalidConfig)
				})
			}
		})
	}
}

// everyPlanner returns one minimal, otherwise-legal call per planner that reads the pool's config or
// the façade. It exists so that a property true of all of them — config validation, façade-failure
// propagation — is asserted over all of them rather than over the one somebody remembered.
//
// PlanReversal is deliberately ABSENT. It reads neither the current config nor any façade value it
// could fail on, and that is a property rather than an oversight: a batch must stay reversible
// whatever the pool looks like today, because an append-only ledger has no other repair primitive.
// Including it here would assert the opposite, which is what this table used to do — see
// TestFixedPrice_PlanReversal_IgnoresTodaysPoolConfig, which asserts the correct behaviour and uses
// these same configs as its control.
func everyPlanner() map[string]func(ctx strategy.Ctx) error {
	s := strategy.FixedPrice{}

	return map[string]func(ctx strategy.Ctx) error{
		"attendance": func(ctx strategy.Ctx) error {
			_, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{Attendees: shares(2)})

			return err
		},
		"award": func(ctx strategy.Ctx) error {
			price := core.Centipoints(100)
			_, err := s.PlanAward(ctx, strategy.AwardEvent{
				Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:    strategy.ItemRef{Name: "x"},
				PriceCp: &price,
			})

			return err
		},
		"adjustment": func(ctx strategy.Ctx) error {
			_, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
				Account: strategy.AccountRef{ID: acct(0)}, AmountCp: 10,
			})

			return err
		},
		"decay": func(ctx strategy.Ctx) error {
			_, err := s.PlanDecay(ctx, strategy.DecayRun{
				PeriodKey: "2024-06",
				Accounts:  []strategy.AccountRef{{ID: acct(0), Kind: "person"}},
			})

			return err
		},
	}
}

// reversalOfATinyBatch plans the reversal of a minimal committed batch. It is NOT part of
// everyPlanner: a reversal deliberately does not read the pool's current config, so it is the one
// planner that must SUCCEED where the others fail. See
// TestFixedPrice_PlanReversal_IgnoresTodaysPoolConfig.
func reversalOfATinyBatch(ctx strategy.Ctx) (strategy.BatchProposal, error) {
	return strategy.FixedPrice{}.PlanReversal(ctx, strategy.LedgerBatch{
		ID:         acct(70),
		StrategyID: "fixed_price",
		Entries: []strategy.EntryProposal{
			{AccountID: acct(0), BalanceKind: "dkp", AmountCp: 10},
			{AccountID: acct(1), BalanceKind: "dkp", AmountCp: -10},
		},
	})
}

// TestFixedPrice_PlanReversal_IgnoresTodaysPoolConfig is the regression test for a defect found in
// review of this PR, and it is the direct inverse of the assertion it replaces.
//
// PlanReversal used to parse Ctx.ConfigJSON() "to validate", which made reversing a batch depend on
// a document with nothing to do with it. The façade documents ConfigJSON as the CURRENT config, so a
// guild that switched strategies — or that added a knob a later version introduced — would have a
// pool config fixed_price cannot parse, and every fixed_price batch in that pool's history would
// become unreversible the moment the config changed. History is immutable and the only repair
// primitive there is must not be contingent on the present.
//
// The configs below are the two real shapes of that: another strategy's knobs, and a knob from a
// future version of this one. Both must reverse cleanly, and the reversal must still carry the
// ORIGINAL's snapshot rather than today's.
func TestFixedPrice_PlanReversal_IgnoresTodaysPoolConfig(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		`{"ep_per_tick": 100, "gp_decay_bp": 500}`,
		`{"decay_bp": 1000, "a_knob_from_a_later_version": true}`,
		`{`,
		`null`,
		`{"decay_bp": null}`,
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()

			ctx := newCtx(t, 2, 0, config)

			// The control: every other planner refuses this config, so the reversal's success is a
			// property of the reversal and not of the config being fine after all.
			_, err := strategy.FixedPrice{}.PlanAttendance(ctx, strategy.AttendanceEvent{
				Attendees: shares(2),
			})
			require.ErrorIs(t, err, strategy.ErrInvalidConfig,
				"the config must be one this strategy genuinely cannot parse")

			reversal, err := reversalOfATinyBatch(ctx)
			require.NoError(t, err,
				"a batch must stay reversible whatever the pool's config says today; an append-only "+
					"ledger has no other repair primitive")
			require.Equal(t, strategy.KindReversal, reversal.Kind)
			require.Empty(t, reversal.ConfigSnapshotJSON,
				"the reversal carries the ORIGINAL batch's snapshot — here empty, since the fixture "+
					"batch had none — and never today's config")
		})
	}
}

// TestFixedPrice_Config_AbsentIsTheDefaults_AndTypoedIsNot is the other direction of the strict
// decoding, and it is what stops the fix from being a regression.
//
// A pool that has set NOTHING must still plan: an empty column and the schema default '{}' both mean
// "the shipped defaults", and rejecting them would break every pool created before its officers
// opened the settings form. What must not be accepted is a document that LOOKS configured and is not.
func TestFixedPrice_Config_AbsentIsTheDefaults_AndTypoedIsNot(t *testing.T) {
	t.Parallel()

	for _, config := range []string{"", "{}", "  ", "\n{}\n"} {
		t.Run(fmt.Sprintf("%q", config), func(t *testing.T) {
			t.Parallel()

			p, err := strategy.FixedPrice{}.PlanAttendance(
				newCtx(t, 1, 0, config), strategy.AttendanceEvent{Attendees: shares(1)})
			require.NoError(t, err)
			require.Equal(t, core.Centipoints(100), p.Entries[1].AmountCp,
				"an unset config runs the shipped default tick award of 100 centipoints")
		})
	}

	// And the two near-misses it is contrasted with. Both used to read back as the default, and both
	// must name the knob: "invalid config" sends an officer to read the whole form.
	t.Run("a transposed knob names itself", func(t *testing.T) {
		t.Parallel()

		_, err := strategy.FixedPrice{}.PlanDecay(
			newCtx(t, 1, 1_000, `{"decay_pb": 1000}`),
			strategy.DecayRun{PeriodKey: "2024-06", AsOfSeq: 7})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "decay_pb")
	})

	t.Run("a null knob names itself", func(t *testing.T) {
		t.Parallel()

		// Two null knobs, so the message is also asserted to be STABLE: the members are reported in
		// sorted order, and a map range would name whichever key Go visited first.
		_, err := strategy.FixedPrice{}.PlanAttendance(
			newCtx(t, 1, 0, `{"tick_award_cp": null, "decay_bp": null}`),
			strategy.AttendanceEvent{Attendees: shares(1)})
		require.ErrorIs(t, err, strategy.ErrInvalidConfig)
		require.ErrorContains(t, err, "decay_bp",
			"with several null knobs the first in sorted order is named, on every run")
	})
}

// TestFixedPrice_ConfigSchema_EveryKnobAgreesWithTheParser derives its cases FROM THE SCHEMA, so a
// knob added later is covered without anybody remembering to add a row.
//
// This exists because the same class of defect was found twice in review of this PR: an unknown key,
// then a null member, each accepted by the parser while the schema rejects it, each leaving the pool
// running a default nobody chose. A third hand-written case would have closed the third instance and
// nothing else. Both directions are asserted, because schema/parser drift has two of them:
//
//   - every declared knob REJECTS null — the schema gives each one a type, and null is a value of
//     none of them. encoding/json decodes a null into a non-pointer field as a no-op, so this is the
//     failure that looks exactly like "the officer left it alone";
//   - every declared knob ACCEPTS a legal value of its declared type — a knob the settings form
//     offers and the planner refuses is the same drift seen from the other side.
func TestFixedPrice_ConfigSchema_EveryKnobAgreesWithTheParser(t *testing.T) {
	t.Parallel()

	var schema struct {
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(strategy.FixedPrice{}.ConfigSchema(), &schema))
	require.NotEmpty(t, schema.Properties)

	// plan runs the cheapest planner that parses the config and nothing else of interest.
	plan := func(t *testing.T, config string) error {
		t.Helper()

		_, err := strategy.FixedPrice{}.PlanAttendance(
			newCtx(t, 1, 0, config), strategy.AttendanceEvent{Attendees: shares(1)})

		return err
	}

	for name, prop := range schema.Properties {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := plan(t, fmt.Sprintf(`{%q: null}`, name))
			require.ErrorIs(t, err, strategy.ErrInvalidConfig,
				"knob %q accepts null. The schema types it, so null is not one of its values — and a "+
					"null decoded into a non-pointer field is a no-op, which means the pool silently "+
					"runs the default instead.", name)
			require.ErrorContains(t, err, name, "the rejection must name the knob")

			// A legal value of the declared type. 1 satisfies every integer bound this schema
			// declares (minimum 0, minimum 1, maximum 10000); an enum takes its first member.
			var legal string

			switch {
			case len(prop.Enum) > 0:
				legal = fmt.Sprintf("%q", prop.Enum[0])
			case prop.Type == "integer":
				legal = "1"
			default:
				t.Fatalf("knob %q is declared as %q, which this test has no legal value for — add "+
					"one here in the same change that adds the type", name, prop.Type)
			}

			require.NoError(t, plan(t, fmt.Sprintf(`{%q: %s}`, name, legal)),
				"knob %q is declared in the schema but the parser refuses %s, so the settings form "+
					"offers a control that cannot be set", name, legal)
		})
	}
}

// TestFixedPrice_Planners_PropagateFacadeFailures asserts a failing façade read stops the plan rather
// than producing a batch built on a zero.
//
// This is the failure mode a fake makes easy to get wrong: a Balance that returns (0, err) and a
// planner that ignores the error decays nobody, which looks exactly like a successful run.
func TestFixedPrice_Planners_PropagateFacadeFailures(t *testing.T) {
	t.Parallel()

	s := strategy.FixedPrice{}
	boom := fmt.Errorf("the read pool is closed")

	t.Run("balance", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, `{"decay_bp": 1000}`)
		ctx.balanceErr = boom

		_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2024-06", AsOfSeq: 3})
		require.ErrorIs(t, err, boom)

		_, err = s.Spendable(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)

		_, err = s.Priority(ctx, strategy.AccountRef{ID: acct(0)})
		require.ErrorIs(t, err, boom)
	})

	t.Run("roster", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 2, 1_000, `{"decay_bp": 1000}`)
		ctx.rosterErr = boom

		_, err := s.PlanDecay(ctx, strategy.DecayRun{PeriodKey: "2024-06"})
		require.ErrorIs(t, err, boom)
	})

	t.Run("system account", func(t *testing.T) {
		t.Parallel()

		// Both proceeds paths resolve a system account and they resolve DIFFERENT ones — the bank
		// for a sink award, the solo-policy account for a split — so a failure has to be propagated
		// from two places rather than one.
		for _, config := range []string{
			`{"default_price_cp": 100, "decay_bp": 1000, "proceeds": "guild_bank"}`,
			`{"default_price_cp": 100, "decay_bp": 1000, "proceeds": "attendees"}`,
		} {
			t.Run(config, func(t *testing.T) {
				t.Parallel()

				for name, plan := range everyPlanner() {
					t.Run(name, func(t *testing.T) {
						t.Parallel()

						ctx := newCtx(t, 3, 1_000, config)
						ctx.systemErr = boom

						require.ErrorIs(t, plan(ctx), boom)
					})
				}

				t.Run("award to attendees", func(t *testing.T) {
					t.Parallel()

					ctx := newCtx(t, 3, 1_000, config)
					ctx.systemErr = boom

					_, err := s.PlanAward(ctx, strategy.AwardEvent{
						Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
						Item:          strategy.ItemRef{Name: "x"},
						Beneficiaries: shares(2),
					})
					require.ErrorIs(t, err, boom)
				})
			})
		}
	})
}

// TestFixedPrice_PlanAward_UnallocatableSplit_FailsInThePlanner covers the allocator refusing, which
// must surface as a planner error naming the strategy rather than as a nil credit list.
//
// Weights that sum past int64 are the case a per-share check cannot catch: each weight is
// individually legal and non-negative, and only their total is impossible. ledger.Allocate rejects
// it, and the planner has to pass that rejection up rather than treating an empty allocation as an
// award with no credits — which would be a batch that debited the buyer and gave the points to
// nobody.
func TestFixedPrice_PlanAward_UnallocatableSplit_FailsInThePlanner(t *testing.T) {
	t.Parallel()

	price := core.Centipoints(1_000)

	_, err := strategy.FixedPrice{}.PlanAward(
		newCtx(t, 2, 0, `{"proceeds": "attendees"}`), strategy.AwardEvent{
			Buyer:   strategy.AccountRef{ID: acct(0), Kind: "person"},
			Item:    strategy.ItemRef{Name: "x"},
			PriceCp: &price,
			Beneficiaries: []strategy.Share{
				{AccountID: acct(0), Weight: math.MaxInt64},
				{AccountID: acct(1), Weight: math.MaxInt64},
			},
		})
	require.ErrorIs(t, err, ledger.ErrWeightOverflow)
	require.ErrorContains(t, err, "fixed_price",
		"the planner must name itself: an allocator error alone does not say which strategy asked "+
			"for the impossible split")
}

// TestFixedPrice_PlanReversal_EmptyBatch_IsRefused covers the one way the shared negation helper
// fails.
//
// A batch with no entries cannot be reversed because it should never have been committed:
// entry_count carries CHECK (entry_count > 0). Refusing here names the strategy and the batch; the
// alternative is a proposal with no entries that the BatchNonEmpty invariant rejects at commit time
// with no idea where it came from.
func TestFixedPrice_PlanReversal_EmptyBatch_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.FixedPrice{}.PlanReversal(newCtx(t, 1, 0, ""), strategy.LedgerBatch{
		ID:         acct(70),
		StrategyID: "fixed_price",
	})
	require.ErrorIs(t, err, strategy.ErrEmptyProposal)
	require.ErrorContains(t, err, acct(70).String())
}

// TestFixedPrice_PlanAdjustment_UnnegatableAmount_IsRefused covers the planner-side balance
// assertion, on the one input that defeats it arithmetically.
//
// math.MinInt64 is the single int64 with no representable negation: -MinInt64 wraps back to
// MinInt64, so an adjustment of it would produce two entries of the SAME sign whose sum wraps. The
// planner's own sum check catches it, which is the point of asserting in the planner as well as at
// commit time — a wrapped sum satisfies a zero-sum check by arithmetic accident.
func TestFixedPrice_PlanAdjustment_UnnegatableAmount_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := strategy.FixedPrice{}.PlanAdjustment(newCtx(t, 2, 0, ""), strategy.AdjustmentEvent{
		Account:      strategy.AccountRef{ID: acct(0)},
		AmountCp:     math.MinInt64,
		Counterparty: acct(1),
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "sum past int64")
}

// TestFixedPrice_Planners_RejectARepeatedAccount is the regression test for a defect found in review
// of this PR: a list naming the same account twice was charged, or credited, twice.
//
// The decay case is the damaging one. Each occurrence reads the SAME as-of balance and posts a full
// debit against it, so `[A, A]` takes two periods' decay in one run — and it commits, because both
// declared invariants still hold: the batch sums to zero and the account stays above its floor. The
// arithmetic is self-consistent and simply wrong, which is the only kind of wrong that survives an
// invariant engine.
//
// All three list-taking planners are covered, under one rule: an account may appear at most once in
// an event. A repeat is indistinguishable from a weight — `[{A,1},{A,1}]` and `[{A,2}]` are the same
// arithmetic — so it is never somebody asking for a bigger share, it is a list that was assembled
// twice, from two sources or from a join that fanned out.
func TestFixedPrice_Planners_RejectARepeatedAccount(t *testing.T) {
	t.Parallel()

	s := strategy.FixedPrice{}

	t.Run("decay", func(t *testing.T) {
		t.Parallel()

		ctx := newCtx(t, 1, 1_000, `{"decay_bp": 1000}`)

		_, err := s.PlanDecay(ctx, strategy.DecayRun{
			PeriodKey: "2024-06",
			AsOfSeq:   7,
			Accounts: []strategy.AccountRef{
				{ID: acct(0), Kind: "person"},
				{ID: acct(0), Kind: "person"},
			},
		})
		require.ErrorIs(t, err, strategy.ErrInvalidEvent)
		require.ErrorContains(t, err, acct(0).String())

		// The control: the same run with the account named once is legal, so the rejection is about
		// the repeat and not about the run.
		single, err := s.PlanDecay(ctx, strategy.DecayRun{
			PeriodKey: "2024-06",
			AsOfSeq:   7,
			Accounts:  []strategy.AccountRef{{ID: acct(0), Kind: "person"}},
		})
		require.NoError(t, err)
		require.Equal(t, core.Centipoints(-100), single.Entries[0].AmountCp,
			"one occurrence, one period's decay: 10% of 1000")
	})

	t.Run("attendance", func(t *testing.T) {
		t.Parallel()

		_, err := s.PlanAttendance(newCtx(t, 2, 0, ""), strategy.AttendanceEvent{
			Attendees: []strategy.Share{
				{AccountID: acct(0), Weight: 1},
				{AccountID: acct(1), Weight: 1},
				{AccountID: acct(0), Weight: 1},
			},
		})
		require.ErrorIs(t, err, strategy.ErrInvalidEvent)
		require.ErrorContains(t, err, acct(0).String())
	})

	t.Run("award beneficiaries", func(t *testing.T) {
		t.Parallel()

		_, err := s.PlanAward(newCtx(t, 2, 5_000, `{"default_price_cp": 300, "proceeds": "attendees"}`),
			strategy.AwardEvent{
				Buyer: strategy.AccountRef{ID: acct(0), Kind: "person"},
				Item:  strategy.ItemRef{Name: "Cloak of Flames"},
				Beneficiaries: []strategy.Share{
					{AccountID: acct(1), Weight: 1},
					{AccountID: acct(1), Weight: 1},
				},
			})
		require.ErrorIs(t, err, strategy.ErrInvalidEvent)
		require.ErrorContains(t, err, acct(1).String())
	})
}

// TestFixedPrice_PlanDecay_TotalOverflow_IsRefused covers the decay accumulator running out of int64.
//
// Every individual balance fits and every individual decay amount fits; it is the total credited back
// to the bank that does not. A wrapped total would land on the bank's entry, so the batch would
// either be rejected with an arithmetic message that named no cause, or — with a different sign
// pattern — balance. Refusing here names the period and the account the accumulator gave out on.
func TestFixedPrice_PlanDecay_TotalOverflow_IsRefused(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 0, `{"decay_bp": 10000}`) // 100%: each account's whole balance decays

	const nearlyHalf = core.Centipoints(4_600_000_000_000_000_000)

	for i := range 3 {
		ctx.balances[acct(i)] = nearlyHalf
	}

	_, err := strategy.FixedPrice{}.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2024-06", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "2024-06")
}

// TestFixedPrice_PlanAttendance_RejectsABadShare covers the share validation on the attendance path.
// It is the same helper the award path uses, and "the same helper" is a claim that stops being true
// the first time somebody inlines one of them.
func TestFixedPrice_PlanAttendance_RejectsABadShare(t *testing.T) {
	t.Parallel()

	_, err := strategy.FixedPrice{}.PlanAttendance(newCtx(t, 2, 0, ""), strategy.AttendanceEvent{
		Attendees: []strategy.Share{
			{AccountID: acct(0), Weight: 1},
			{AccountID: acct(1), Weight: -3},
		},
	})
	require.ErrorIs(t, err, strategy.ErrInvalidEvent)
	require.ErrorContains(t, err, "negative weight")
}

// --- The properties ----------------------------------------------------------------------------
//
// Named TestProperty_* because `make test-property` selects on that prefix, and the target fails when
// the selector matches nothing rather than reporting a green run of zero tests.
//
// ON THE GENERATOR. internal/ledger's properties drive `testing/quick`, whose generator interface
// takes a *math/rand.Rand — and importing math/rand ANYWHERE under internal/strategy trips repo gate
// PURE002, test files included. That is the gate working as designed rather than an obstacle to route
// around: the rule is that this package gets its randomness from the injected seeded Rng, and a test
// is not exempt from the rule it exists to prove.
//
// So the cases here are drawn from ledger.NewRng — the same PCG source a strategy would be handed —
// and the BASE SEED IS PRINTED, which makes a failure reproducible in a way a time-seeded quick.Check
// is not: `DKP_PROPERTY_SEED=<the printed number> go test ./internal/strategy` replays the exact run.

// defaultPropertyChecks is the per-PR count from PR 10's acceptance criterion.
const defaultPropertyChecks = 200

// defaultPropertySeed is the base seed the cases are drawn from when DKP_PROPERTY_SEED is unset.
//
// FIXED rather than time-derived, deliberately. A property suite whose inputs change every run finds
// slightly more over a month and is unreproducible on the morning it goes red, and "it passed when I
// re-ran it" is how a real counterexample gets closed as flaky. The nightly 20,000-case lane is where
// the breadth comes from, and it varies the seed by running more cases from the same base.
const defaultPropertySeed = 1_717_243_200

// propertySeed is the base seed for this run, overridable to replay a reported failure.
func propertySeed(tb testing.TB) int64 {
	tb.Helper()

	raw, ok := os.LookupEnv("DKP_PROPERTY_SEED")
	if !ok || raw == "" {
		return defaultPropertySeed
	}

	seed, err := strconv.ParseInt(raw, 10, 64)
	require.NoError(tb, err, "DKP_PROPERTY_SEED=%q is not a number", raw)

	return seed
}

// forEachCase runs check over `propertyChecks` generated awards, failing with the seed that
// reproduces the first counterexample.
//
// One Rng per case, seeded base+i, rather than one Rng for the whole run: a counterexample is then
// replayable on its own (`DKP_PROPERTY_SEED=<base+i>` with one check) without replaying the i cases
// before it, which is what makes shrinking-by-hand practical.
func forEachCase(t *testing.T, check func(c awardCase) error) {
	t.Helper()

	base := propertySeed(t)
	checks := propertyChecks(t)

	t.Logf("%d cases from base seed %d", checks, base)

	for i := range checks {
		seed := base + int64(i)

		c := generateAwardCase(ledger.NewRng(seed))
		if err := check(c); err != nil {
			t.Fatalf("counterexample at seed %d (price %d, %d weights): %v\n"+
				"replay with: DKP_PROPERTY_SEED=%d DKP_PROPERTY_CHECKS=1 go test ./internal/strategy",
				seed, c.PriceCp, len(c.Weights), err, seed)
		}
	}
}

// propertyChecks is the number of random cases each property runs, overridable with
// DKP_PROPERTY_CHECKS for the nightly 20,000-case lane. It is the twin of the identical helper in
// internal/ledger/property_test.go; the two cannot be shared because a test helper is not importable,
// and both fail loudly on a malformed value rather than silently falling back to 200 — a nightly deep
// run that quietly ran the PR count is worse than no nightly run.
func propertyChecks(tb testing.TB) int {
	tb.Helper()

	raw, ok := os.LookupEnv("DKP_PROPERTY_CHECKS")
	if !ok || raw == "" {
		return defaultPropertyChecks
	}

	n, err := strconv.Atoi(raw)
	require.NoError(tb, err, "DKP_PROPERTY_CHECKS=%q is not a number", raw)
	require.Positive(tb, n, "DKP_PROPERTY_CHECKS must be positive, got %d", n)

	return n
}

// awardCase is one generated award: a price and the weights it is split across.
type awardCase struct {
	PriceCp core.Centipoints
	Weights []int64
}

// generateAwardCase draws one case from a seeded Rng.
//
// The distribution is CHOSEN rather than uniform, for the reason P2 states: N = 1, P < N and P prime
// all have to appear, and a uniform draw over int64 produces none of them in 200 cases.
func generateAwardCase(rng strategy.Rng) awardCase {
	n := rng.IntN(40) + 1

	weights := make([]int64, n)
	for i := range weights {
		// Zero weights are included: a raider with no attendance in the window is a real input, and
		// the all-zero case is what routes a split to the residue account.
		weights[i] = int64(rng.IntN(8))
	}

	var price core.Centipoints

	switch rng.IntN(4) {
	case 0:
		price = core.Centipoints(rng.IntN(30) + 1) // frequently below N
	case 1:
		price = core.Centipoints(propertyPrimes[rng.IntN(len(propertyPrimes))])
	case 2:
		price = core.Centipoints(rngInt64(rng, 1_000_000) + 1)
	default:
		// Large enough that price * weight overflows int64 if the allocator multiplies before
		// dividing, which is the bug the 128-bit product in ledger.Allocate exists to prevent.
		price = core.Centipoints(rngInt64(rng, 1<<60) + 1)
	}

	return awardCase{PriceCp: price, Weights: weights}
}

// rngInt64 returns a value in [0, bound) built from two 30-bit draws.
//
// Composed rather than taken from a single IntN(bound) call because `int` is 32 bits on one of the
// platforms this repository ships (linux/arm/v7 — the older Raspberry Pis half this audience runs
// on), where a constant of 1<<60 does not compile. The modulo skews the distribution very slightly
// toward small values, which for a test generator is a non-issue and is worth stating rather than
// leaving for somebody to notice.
func rngInt64(rng strategy.Rng, bound int64) int64 {
	const half = 1 << 30

	v := int64(rng.IntN(half))<<30 | int64(rng.IntN(half))

	return v % bound
}

// propertyPrimes are the prime prices P2 requires: a prime shares no factor with any plausible weight
// sum, so every quota has a non-zero remainder and the +1 pass is maximally exercised.
var propertyPrimes = []int{2, 3, 5, 7, 11, 13, 17, 19, 23, 97, 101, 997, 7919, 65_537, 1_000_003}

// shares turns a generated case into beneficiaries.
func (c awardCase) shares() []strategy.Share {
	out := make([]strategy.Share, len(c.Weights))
	for i, w := range c.Weights {
		out[i] = strategy.Share{AccountID: acct(i), Weight: w}
	}

	return out
}

// event turns a generated case into a planable award.
func (c awardCase) event() strategy.AwardEvent {
	price := c.PriceCp

	return strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Generated"},
		PriceCp:       &price,
		Beneficiaries: c.shares(),
		EffectiveAt:   fixedNow,
	}
}

// TestProperty_P2_FixedPriceAward_CreditsSumToTheDebitExactly is P2 at the strategy level: for every
// (price, N) the credits sum to exactly the price, and the batch therefore sums to exactly zero.
//
// internal/ledger's P2 proves the allocator. This proves the PLANNER — that it splits the whole
// price, debits the whole price, and adds no entry of its own that breaks the equality. A planner
// that rounded its own debit, or that emitted a zero credit, would pass the allocator's property and
// fail this one.
func TestProperty_P2_FixedPriceAward_CreditsSumToTheDebitExactly(t *testing.T) {
	t.Parallel()

	const config = `{"proceeds": "attendees"}`

	forEachCase(t, func(c awardCase) error {
		p, err := strategy.FixedPrice{}.PlanAward(newCtx(t, 0, 0, config), c.event())
		if err != nil {
			return err
		}

		if p.Entries[0].AmountCp != -c.PriceCp {
			return fmt.Errorf("the debit is %d, want the whole price %d — a planner that rounded "+
				"its own debit would balance against rounded credits and still be wrong",
				p.Entries[0].AmountCp, -c.PriceCp)
		}

		var credits core.Centipoints

		for i, e := range p.Entries[1:] {
			if e.AmountCp == 0 {
				return fmt.Errorf("credit %d moves 0 centipoints; CHECK (amount_cp <> 0) means a "+
					"zero share must be dropped, never written", i)
			}

			credits += e.AmountCp
		}

		if credits != c.PriceCp {
			return fmt.Errorf("the credits sum to %d, want exactly the price %d", credits, c.PriceCp)
		}

		if net, ok := p.NetAmountCp(); !ok || net != 0 {
			return fmt.Errorf("the batch nets to %d (ok=%v), want exactly 0", net, ok)
		}

		return nil
	})
}

// TestProperty_P5_FixedPriceReversal_IsAnExactInverse is P5 at the strategy level: planning an award
// and then its reversal leaves every account exactly where it started.
//
// "Exactly" is the whole claim. A reversal that is off by a centipoint on one account leaves a
// permanent, unexplainable discrepancy in a member's statement — and nobody finds it, because the
// award and its reversal both look right individually.
func TestProperty_P5_FixedPriceReversal_IsAnExactInverse(t *testing.T) {
	t.Parallel()

	const config = `{"proceeds": "attendees"}`

	// Counted across the run rather than asserted per case. A one-attendee award whose sole
	// beneficiary IS the buyer nets every account to zero — a legal batch that records the loot with
	// its provenance and moves no points — and for that case "restored to where it started" is
	// vacuously true. Failing it would be wrong; letting the whole property consist of such cases
	// would make it prove nothing. So the vacuous ones are counted and the run must contain
	// non-vacuous ones.
	nonTrivial := 0

	forEachCase(t, func(c awardCase) error {
		ctx := newCtx(t, 0, 0, config)

		original, err := strategy.FixedPrice{}.PlanAward(ctx, c.event())
		if err != nil {
			return err
		}

		balances := map[core.ULID]core.Centipoints{}
		for _, e := range original.Entries {
			balances[e.AccountID] += e.AmountCp
		}

		for _, v := range balances {
			if v != 0 {
				nonTrivial++

				break
			}
		}

		reversal, err := strategy.FixedPrice{}.PlanReversal(ctx, strategy.LedgerBatch{
			ID:              acct(70),
			Kind:            original.Kind,
			StrategyID:      original.StrategyID,
			StrategyVersion: original.StrategyVersion,
			EffectiveAt:     original.EffectiveAt,
			Entries:         original.Entries,
		})
		if err != nil {
			return err
		}

		if reversal.Kind != strategy.KindReversal || reversal.ReversesBatchID == nil {
			return fmt.Errorf("the reversal is kind %q with target %v; a reversal that points at "+
				"nothing is an ordinary batch wearing the word", reversal.Kind, reversal.ReversesBatchID)
		}

		for _, e := range reversal.Entries {
			balances[e.AccountID] += e.AmountCp
		}

		for id, v := range balances {
			if v != 0 {
				return fmt.Errorf("account %s is %d centipoints from where it started", id, v)
			}
		}

		return nil
	})

	require.Positive(t, nonTrivial,
		"every generated award netted every account to zero, so the property held vacuously — the "+
			"generator, not the reversal, is what is broken")
}

// TestProperty_P8_FixedPricePlan_IsByteIdenticalAcrossRunsAndInputOrder is P8 at the strategy level.
//
// Two claims, and the second is the one that catches real bugs. Planning the same event twice must
// produce byte-identical canonical bytes — which a planner that ranged over a map would fail
// intermittently — and planning the same event with its beneficiaries SHUFFLED must produce the same
// bytes too, because a set of attendees is a set and the officer's upload order is not part of it.
//
// The second claim is what makes the first non-trivial: a planner that preserved input order would
// pass the first perfectly and produce a different hash for the same raid depending on how the
// roster was sorted when it was parsed.
func TestProperty_P8_FixedPricePlan_IsByteIdenticalAcrossRunsAndInputOrder(t *testing.T) {
	t.Parallel()

	const config = `{"proceeds": "attendees"}`

	forEachCase(t, func(c awardCase) error {
		first, err := planCanonical(t, config, c.event())
		if err != nil {
			return err
		}

		second, err := planCanonical(t, config, c.event())
		if err != nil {
			return err
		}

		if string(first) != string(second) {
			return fmt.Errorf("two plans of the same event differ:\n\t%s\n\t%s", first, second)
		}

		shuffled := c.event()

		// Shuffled with a NEGATIVE seed, so the permutation is not the identity and the seeded Rng's
		// whole int64 range is exercised: a generator that collapsed negative seeds onto positive
		// ones would still shuffle, but would silently halve the space a replay can address.
		rng := ledger.NewRng(-int64(len(shuffled.Beneficiaries)) - 1)
		rng.Shuffle(len(shuffled.Beneficiaries), func(i, j int) {
			shuffled.Beneficiaries[i], shuffled.Beneficiaries[j] = shuffled.Beneficiaries[j], shuffled.Beneficiaries[i]
		})

		third, err := planCanonical(t, config, shuffled)
		if err != nil {
			return err
		}

		if string(first) != string(third) {
			return fmt.Errorf("the same attendees in a different order planned differently:\n\t%s\n\t%s",
				first, third)
		}

		return nil
	})
}

// planCanonical plans one award and returns its canonical bytes.
func planCanonical(tb testing.TB, config string, ev strategy.AwardEvent) ([]byte, error) {
	tb.Helper()

	p, err := strategy.FixedPrice{}.PlanAward(newCtx(tb, 0, 0, config), ev)
	if err != nil {
		return nil, err
	}

	return p.Canonical()
}

// TestProperty_NoFloat_AppearsAnywhereInAProposal walks the proposal's TYPE graph and fails on any
// floating-point field.
//
// It walks the type rather than a value on purpose. A value-based check only sees the fields a test
// happened to populate, so a float32 added to EntryProposal and left at its zero value would pass;
// the type graph contains every field whether or not anything set it. Point arithmetic is
// core.Centipoints (int64) only — canonical §1 — and this is the assertion that survives somebody
// adding a "rate" field in eighteen months.
//
// The schema is checked too: a JSON Schema that said `number` where it means `integer` would let a
// decimal into the config, and the config is snapshotted onto every batch.
func TestProperty_NoFloat_AppearsAnywhereInAProposal(t *testing.T) {
	t.Parallel()

	for _, root := range []reflect.Type{
		reflect.TypeOf(strategy.BatchProposal{}),
		reflect.TypeOf(strategy.EntryProposal{}),
		reflect.TypeOf(strategy.Invariant{}),
		reflect.TypeOf(strategy.Allocation{}),
		reflect.TypeOf(strategy.Share{}),
		reflect.TypeOf(strategy.Priority{}),
	} {
		t.Run(root.Name(), func(t *testing.T) {
			t.Parallel()

			requireNoFloat(t, root, root.Name(), map[reflect.Type]bool{})
		})
	}

	t.Run("config schema declares no number", func(t *testing.T) {
		t.Parallel()

		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}

		require.NoError(t, json.Unmarshal(strategy.FixedPrice{}.ConfigSchema(), &schema))
		require.NotEmpty(t, schema.Properties)

		for name, prop := range schema.Properties {
			require.NotEqual(t, "number", prop.Type,
				"config knob %q is declared as `number`, which permits a decimal. Money is integer "+
					"centipoints and ratios are integer basis points.", name)
		}
	})
}

// requireNoFloat recurses through a type, failing on any float kind. The `seen` set makes a
// self-referential type terminate.
func requireNoFloat(t *testing.T, typ reflect.Type, path string, seen map[reflect.Type]bool) {
	t.Helper()

	if seen[typ] {
		return
	}

	seen[typ] = true

	switch typ.Kind() {
	case reflect.Float32, reflect.Float64:
		t.Fatalf("%s is a %s. Point arithmetic is core.Centipoints (int64) only (canonical §1); a "+
			"float in the point path silently converts the ledger to floating point.", path, typ.Kind())
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		requireNoFloat(t, typ.Elem(), path+"[]", seen)
	case reflect.Struct:
		for i := range typ.NumField() {
			f := typ.Field(i)
			requireNoFloat(t, f.Type, path+"."+f.Name, seen)
		}
	default:
	}
}

// TestFixedPrice_Planners_ConsumeNoRandomness asserts the injected Rng is offered and refused.
//
// It is the honest form of P8's anti-tautology half for this strategy. A planner that consumed
// randomness would need its seed persisted onto the batch for a replay to be byte-identical;
// fixed_price consumes none, so its proposals carry no seed — and the way to state that as a fact
// rather than an assumption is to count the calls and to require the seed to be absent.
func TestFixedPrice_Planners_ConsumeNoRandomness(t *testing.T) {
	t.Parallel()

	ctx := newCtx(t, 3, 10_000, `{"default_price_cp": 999, "proceeds": "attendees", "decay_bp": 500}`)
	s := strategy.FixedPrice{}

	award, err := s.PlanAward(ctx, strategy.AwardEvent{
		Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
		Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
		Beneficiaries: shares(3),
		EffectiveAt:   fixedNow,
	})
	require.NoError(t, err)

	attendance, err := s.PlanAttendance(ctx, strategy.AttendanceEvent{
		Attendees: shares(3), EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	adjustment, err := s.PlanAdjustment(ctx, strategy.AdjustmentEvent{
		Account: strategy.AccountRef{ID: acct(1)}, AmountCp: 42, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	decay, err := s.PlanDecay(ctx, strategy.DecayRun{
		PeriodKey: "2024-06", AsOfSeq: 7, EffectiveAt: fixedNow,
	})
	require.NoError(t, err)

	for _, p := range []strategy.BatchProposal{award, attendance, adjustment, decay} {
		require.Nil(t, p.RngSeed,
			"%s carries a seed it never consumed; a seed asserts that replaying from it reproduces "+
				"the plan, which would be true here only by irrelevance", p.Kind)
	}

	require.Zero(t, ctx.rng.calls,
		"fixed_price must consume no randomness: its only tie-break is the allocator's account_id "+
			"ordering, which is deliberately NOT random so that two replays agree")
}

// --- Declaration and the goldens ---------------------------------------------------------------

// invariantKinds lists the invariant kinds a proposal declares.
func invariantKinds(p strategy.BatchProposal) []strategy.InvariantKind {
	out := make([]strategy.InvariantKind, 0, len(p.Invariants))
	for _, inv := range p.Invariants {
		out = append(out, inv.Kind)
	}

	return out
}

// TestFixedPrice_EveryPlannerInvariant_IsDeclared keeps the strategy-level catalogue and the
// per-proposal sets in step, in both directions.
//
// The catalogue is what a reviewer reads to see what constrains this strategy, and the proposal's set
// is what the ledger actually executes. A rule attached to a batch but missing from the catalogue
// makes the catalogue a lie; a rule in the catalogue that no planner ever attaches is a claim of
// protection nothing provides.
func TestFixedPrice_EveryPlannerInvariant_IsDeclared(t *testing.T) {
	t.Parallel()

	declared := map[strategy.InvariantKind]bool{}
	for _, inv := range (strategy.FixedPrice{}).Invariants() {
		require.Equal(t, strategy.BalanceKindDKP, inv.BalanceKind,
			"every declared invariant must be scoped to the one balance kind this strategy moves; "+
				"the commit-time engine rejects an invariant scoped to a kind the batch does not touch")
		declared[inv.Kind] = true
	}

	attached := map[strategy.InvariantKind]bool{}

	for _, p := range allPlannerProposals(t) {
		require.NotEmpty(t, p.Invariants, "the %s planner declares nothing", p.Kind)

		for _, inv := range p.Invariants {
			require.True(t, declared[inv.Kind],
				"the %s planner attaches %s, which Invariants() does not list", p.Kind, inv.Kind)

			if inv.Kind == strategy.InvariantNonNegative {
				require.NotNil(t, inv.FloorCp,
					"NonNegative with no floor is rejected at commit time: 'nobody may go below "+
						"zero' and 'somebody forgot' must not be the same declaration")
			}

			attached[inv.Kind] = true
		}
	}

	for kind := range declared {
		require.True(t, attached[kind],
			"Invariants() lists %s, which no planner attaches to a proposal — the ledger executes "+
				"the proposal's set, so this rule protects nothing", kind)
	}
}

// goldenCase is one planner's canonical proposal.
type goldenCase struct {
	name string
	plan func(tb testing.TB) strategy.BatchProposal
}

// goldenConfig is the config every golden is planned under: every knob set to a non-default value, so
// that a knob that stopped being read shows up as a changed golden rather than as nothing.
const goldenConfig = `{"default_price_cp":250,"tick_award_cp":150,"proceeds":"attendees",` +
	`"solo_policy":"write_off","floor_cp":-500,"decay_bp":1000}`

// goldenCases is one case per planner, exactly as PR 10's acceptance criterion asks.
func goldenCases() []goldenCase {
	s := strategy.FixedPrice{}
	tick, raid, itemAward := acct(80), acct(81), acct(82)

	return []goldenCase{
		{
			name: "attendance",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAttendance(goldenCtx(tb), strategy.AttendanceEvent{
					Attendees: []strategy.Share{
						{AccountID: acct(0), Weight: 1},
						{AccountID: acct(1), Weight: 2},
						{AccountID: acct(2), Weight: 1},
					},
					TickID:      &tick,
					RaidID:      &raid,
					EffectiveAt: fixedNow,
					Reason:      "Vox, tick 3",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "award",
			plan: func(tb testing.TB) strategy.BatchProposal {
				character := acct(50)
				p, err := s.PlanAward(goldenCtx(tb), strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person", Label: "Raider 0"},
					CharacterID:   &character,
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					Beneficiaries: shares(3),
					RaidID:        &raid,
					ItemAwardID:   &itemAward,
					EffectiveAt:   fixedNow,
					Reason:        "Nagafen, roll 97",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "adjustment",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanAdjustment(goldenCtx(tb), strategy.AdjustmentEvent{
					Account:     strategy.AccountRef{ID: acct(1), Kind: "person"},
					AmountCp:    -750,
					EffectiveAt: fixedNow,
					Reason:      "double-credited tick on 2024-05-30",
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "decay",
			plan: func(tb testing.TB) strategy.BatchProposal {
				p, err := s.PlanDecay(goldenCtx(tb), strategy.DecayRun{
					PeriodKey:   "2024-06",
					AsOfSeq:     7,
					EffectiveAt: fixedNow,
				})
				require.NoError(tb, err)

				return p
			},
		},
		{
			name: "reversal",
			plan: func(tb testing.TB) strategy.BatchProposal {
				ctx := goldenCtx(tb)

				original, err := s.PlanAward(ctx, strategy.AwardEvent{
					Buyer:         strategy.AccountRef{ID: acct(0), Kind: "person"},
					Item:          strategy.ItemRef{ID: acct(90), Name: "Cloak of Flames"},
					Beneficiaries: shares(3),
					EffectiveAt:   fixedNow.Add(-24 * 60 * 60 * 1_000_000_000),
					Reason:        "Nagafen, roll 97",
				})
				require.NoError(tb, err)

				p, err := s.PlanReversal(ctx, strategy.LedgerBatch{
					ID:                 acct(70),
					Kind:               original.Kind,
					StrategyID:         original.StrategyID,
					StrategyVersion:    original.StrategyVersion,
					ConfigSnapshotJSON: original.ConfigSnapshotJSON,
					Reason:             original.Reason,
					EffectiveAt:        original.EffectiveAt,
					Entries:            original.Entries,
				})
				require.NoError(tb, err)

				return p
			},
		},
	}
}

// goldenCtx is the façade every golden is planned against: three raiders with balances that decay
// unevenly, so the decay golden exercises the flooring rather than a round number.
func goldenCtx(tb testing.TB) *fakeCtx {
	tb.Helper()

	ctx := newCtx(tb, 3, 0, goldenConfig)
	ctx.balances[acct(0)] = 12_345
	ctx.balances[acct(1)] = 6_789
	ctx.balances[acct(2)] = 9

	return ctx
}

// allPlannerProposals plans one proposal per planner, for the declaration test above.
func allPlannerProposals(tb testing.TB) []strategy.BatchProposal {
	tb.Helper()

	out := make([]strategy.BatchProposal, 0, len(goldenCases()))
	for _, c := range goldenCases() {
		out = append(out, c.plan(tb))
	}

	return out
}

// TestFixedPrice_Planners_MatchTheirCanonicalGolden compares the WHOLE proposal, not three fields.
//
// Asserting on a handful of fields hides the fourth that changed, and the fourth is exactly the one
// nobody thought to assert. The canonical form is the byte string the determinism property hashes, so
// a golden over it pins entry order, provenance pointers, the config snapshot, the declared
// invariants and the effective time in one comparison.
//
// -update is refused under CI for the reason it is refused for every golden here: the run that would
// have caught a changed proposal must not be the run that overwrites the evidence.
func TestFixedPrice_Planners_MatchTheirCanonicalGolden(t *testing.T) {
	t.Parallel()

	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.plan(t).Canonical()
			require.NoError(t, err)

			path := filepath.Join(goldenDir, tc.name+".json")

			if *updateGolden {
				if os.Getenv("CI") != "" {
					t.Fatal("refusing -update under CI: a golden CI can rewrite proves nothing")
				}

				require.NoError(t, os.MkdirAll(goldenDir, 0o755))
				require.NoError(t, os.WriteFile(path, append(got, '\n'), 0o644))
				t.Logf("wrote %s", path)

				return
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "read the committed golden at %s", path)

			require.JSONEq(t, string(want), string(got),
				"the %s proposal changed shape. If you meant it, re-run with -update on a laptop "+
					"(never CI) and read the diff before committing it.", tc.name)
			require.Equal(t, string(want), string(got)+"\n",
				"the %s proposal's CANONICAL BYTES changed. Equivalent JSON is not enough: the "+
					"canonical form is what the determinism property hashes, so field order and "+
					"entry order are part of the contract.", tc.name)
		})
	}
}

// TestFixedPrice_Goldens_CoverEveryPlanner is the anti-drift half: a planner added without a golden
// would leave the whole-proposal assertion covering four planners out of five, silently.
func TestFixedPrice_Goldens_CoverEveryPlanner(t *testing.T) {
	t.Parallel()

	kinds := map[string]bool{}
	for _, p := range allPlannerProposals(t) {
		kinds[p.Kind] = true
	}

	want := []string{"adjustment", "attendance", "award", "decay", "reversal"}

	got := make([]string, 0, len(kinds))
	for k := range kinds {
		got = append(got, k)
	}

	sort.Strings(got)
	require.Equal(t, want, got, "every planner must contribute a golden case")

	entries, err := os.ReadDir(goldenDir)
	require.NoError(t, err, "the golden directory must exist and be committed")

	files := make([]string, 0, len(entries))

	for _, e := range entries {
		files = append(files, e.Name())
	}

	sort.Strings(files)
	require.Equal(t,
		[]string{"adjustment.json", "attendance.json", "award.json", "decay.json", "reversal.json"},
		files,
		"the committed goldens and the planners must be the same set — a deleted golden is how a "+
			"whole-proposal assertion quietly stops covering a planner")
}
