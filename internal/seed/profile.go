package seed

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// ErrInvalidProfile is returned for a Profile that cannot describe a dataset — no accounts, a
// maximum attendance below the minimum, an attendance larger than the roster. Sentinels live in the
// owning package (.claude/rules/go-idioms.md).
var ErrInvalidProfile = errors.New("invalid seed profile")

// PerfEntryFloor is the ledger size the Perf profile must reach: item V3's ~520,000 entries.
//
// A FLOOR, not a target, and the direction matters. Being wrong downward — generating fewer entries
// than a real guild has — costs the measurement its whole point, because every budget in the
// roadmap is stated at this scale. Being wrong upward costs seconds of generation. So the profile
// is tuned to sit just above the line and TestSeed_PerfProfile_MeetsEntryFloor holds it there.
const PerfEntryFloor = 520_000

// Profile is the deterministic description of a synthetic dataset. Two Profiles that compare equal
// describe byte-identical batch content, which is why every field is a value type and none is a
// function.
type Profile struct {
	// Name identifies the profile on the command line ("perf") and in a source_ref.
	Name string

	// PoolID is the pool every batch lands in. The migration seeds exactly one.
	PoolID core.ULID

	// Accounts is the roster size: how many person accounts exist. Item V3 says ~280.
	Accounts int

	// Raids is how many raid nights the ledger covers. Item V3 says ~3,400.
	Raids int

	// TicksPerRaid is how many attendance ticks each raid awards.
	TicksPerRaid int

	// MinAttendees and MaxAttendees bound the roster present at a tick, inclusive. A real raid
	// night is not the same forty people every time, and an attendance set that never changed would
	// give the covering index a locality it will not have in production.
	MinAttendees int
	MaxAttendees int

	// ZeroSumAwardsPerRaid is how many items are awarded through a zero-sum split — one debit on the
	// winner and credits across everybody else, allocated by largest remainder.
	ZeroSumAwardsPerRaid int

	// FixedPriceAwardsPerRaid is how many items are awarded at a fixed price: a single debit, with
	// the points leaving the economy rather than being redistributed.
	FixedPriceAwardsPerRaid int

	// DecayEveryRaids posts a decay batch over the whole roster once every N raids. Zero disables
	// decay. Decay is POSTED, never computed (.claude/rules/ledger-and-strategy.md), so it is real
	// entries here exactly as it is in production.
	DecayEveryRaids int

	// StartPointsCp is the opening balance every account is credited in the first batch.
	StartPointsCp core.Centipoints

	// TickValueCp is what one attendance tick pays.
	TickValueCp core.Centipoints

	// MinItemPriceCp and MaxItemPriceCp bound an item's price, inclusive.
	MinItemPriceCp core.Centipoints
	MaxItemPriceCp core.Centipoints

	// MinDecayCp and MaxDecayCp bound the per-account debit a decay batch posts, inclusive. Both are
	// positive magnitudes; the sign is applied when the entry is built.
	MinDecayCp core.Centipoints
	MaxDecayCp core.Centipoints

	// Seed drives every choice the walk makes: who attended, what an item cost, how much decayed.
	Seed int64

	// FirstRaidAt is the effective_at of the first raid. GAME truth, and deliberately in the past —
	// recorded_at comes from the injected clock and is stamped by the ledger.
	FirstRaidAt core.Micros

	// BetweenRaids is the effective_at spacing between raid nights.
	BetweenRaids time.Duration
}

// Perf returns the V3-scale profile: 280 accounts, 3,400 raids, just over 520,000 entries.
//
// The numbers are item V3's claim about a large P99 guild, not a measurement of one — V3 is still
// open, and the honest thing is to say so here rather than let the constants imply a survey nobody
// ran. If V3 comes back saying real guilds are five times this, the profile changes and every
// budget measured against it is re-measured; that is exactly why the generator is parameterised and
// the raid count is the knob (Scaled below).
//
// The composition, and where the 520k comes from:
//
//	opening        1 batch  × 280 accounts                     =        280
//	attendance     3,400 raids × 3 ticks × ~37 attendees        ≈    377,000
//	zero-sum       3,400 raids × 1 award × (1 + ~36 credits)    ≈    126,000
//	fixed price    3,400 raids × 2 awards × 1 debit             =      6,800
//	decay          60 runs × 280 accounts                       =     16,800
//
// Counts() computes the exact figure by walking the plan, so the arithmetic above is a reader's aid
// and never the authority.
//
// BetweenRaids is 13 hours, which puts 3,400 raids across a little over five years at roughly
// thirteen a week. That is a hard-raiding P99 guild running several targets a day, which is the
// population that reaches 520k entries in the first place.
func Perf() Profile {
	return Profile{
		Name:                    "perf",
		PoolID:                  ledger.DefaultPoolID,
		Accounts:                280,
		Raids:                   3_400,
		TicksPerRaid:            3,
		MinAttendees:            30,
		MaxAttendees:            44,
		ZeroSumAwardsPerRaid:    1,
		FixedPriceAwardsPerRaid: 2,
		DecayEveryRaids:         56,
		StartPointsCp:           5_000,
		TickValueCp:             1_000,
		MinItemPriceCp:          10_000,
		MaxItemPriceCp:          50_000,
		MinDecayCp:              500,
		MaxDecayCp:              2_500,
		Seed:                    190,
		FirstRaidAt:             core.FromTime(time.Date(2019, time.January, 2, 2, 0, 0, 0, time.UTC)),
		BetweenRaids:            13 * time.Hour,
	}
}

// Scaled returns the same profile over a different number of raids.
//
// This is the ONE knob, and having exactly one is the point. The perf suite runs the identical code
// path at every size — a small raid count on every PR so the generator cannot rot, the full 3,400
// under `make test-perf` and nightly — which is the shape DKP_PROPERTY_CHECKS already uses. A second
// code path for "the fast version" would mean the cheap run and the real run could compile
// differently, and then the cheap one proves nothing about the expensive one.
//
// The roster does NOT scale: a guild is 280 people whether you look at one month of its ledger or
// five years of it. Scaling the roster too would change the standings row count, which is the one
// number the V5 budget is stated per.
func (p Profile) Scaled(raids int) Profile {
	p.Raids = raids

	return p
}

// Validate rejects a profile that cannot describe a dataset, before anything is written.
func (p Profile) Validate() error {
	switch {
	case p.Name == "":
		return fmt.Errorf("profile has no name: %w", ErrInvalidProfile)
	case p.PoolID == "":
		return fmt.Errorf("profile %q has no pool id: %w", p.Name, ErrInvalidProfile)
	case p.Accounts <= 0:
		return fmt.Errorf("profile %q has %d accounts; a ledger needs somebody to hang entries on: %w",
			p.Name, p.Accounts, ErrInvalidProfile)
	case p.Raids < 0:
		return fmt.Errorf("profile %q has %d raids: %w", p.Name, p.Raids, ErrInvalidProfile)
	case p.MinAttendees <= 0 || p.MaxAttendees < p.MinAttendees:
		return fmt.Errorf("profile %q attendance range [%d, %d] is empty or negative: %w",
			p.Name, p.MinAttendees, p.MaxAttendees, ErrInvalidProfile)
	case p.MaxAttendees > p.Accounts:
		return fmt.Errorf("profile %q seats %d attendees from a roster of %d: %w",
			p.Name, p.MaxAttendees, p.Accounts, ErrInvalidProfile)
	case p.MinItemPriceCp <= 0 || p.MaxItemPriceCp < p.MinItemPriceCp:
		return fmt.Errorf("profile %q item price range [%d, %d] is empty or non-positive: %w",
			p.Name, p.MinItemPriceCp, p.MaxItemPriceCp, ErrInvalidProfile)
	case p.MinDecayCp <= 0 || p.MaxDecayCp < p.MinDecayCp:
		return fmt.Errorf("profile %q decay range [%d, %d] is empty or non-positive: %w",
			p.Name, p.MinDecayCp, p.MaxDecayCp, ErrInvalidProfile)
	case p.TickValueCp == 0 || p.StartPointsCp == 0:
		// ledger_entry carries CHECK (amount_cp <> 0), and a profile that plans a zero entry would
		// fail at the INSERT after thousands of good batches had already been written.
		return fmt.Errorf("profile %q plans a zero tick value or zero start points; "+
			"ledger_entry rejects a zero amount: %w", p.Name, ErrInvalidProfile)
	}

	return nil
}

// AccountID returns the deterministic account id for roster index i, and CharacterID / PersonID the
// deterministic character and person it belongs to. They are functions rather than a stored slice so
// that Counts() costs nothing and a caller can name an account without generating anything.
func (p Profile) AccountID(i int) core.ULID   { return DeterministicID(tagAccount, i) }
func (p Profile) CharacterID(i int) core.ULID { return DeterministicID(tagCharacter, i) }
func (p Profile) PersonID(i int) core.ULID    { return DeterministicID(tagPerson, i) }

// Counts is how much data a profile describes.
type Counts struct {
	Accounts int
	Batches  int
	Entries  int
}

// Counts walks the plan without a database and reports exactly what Generate would write.
//
// PURE, and that is what makes it worth having: the perf suite asserts the row counts in the
// database equal the counts computed here, so an agreement between them is evidence rather than a
// tautology. It is also what gives the profile a row-count floor to be held to (ROADMAP Phase 1:
// "seed_profile_test: row-count floors per profile, non-decreasing") without generating 520k rows
// to find out.
func (p Profile) Counts() (Counts, error) {
	c := Counts{Accounts: p.Accounts}

	err := p.walk(func(b batchPlan) error {
		c.Batches++
		c.Entries += len(b.Proposal.Entries)

		return nil
	})
	if err != nil {
		return Counts{}, err
	}

	return c, nil
}

// batchPlan is one planned batch: a strategy.BatchProposal plus the request-side fields the ledger
// needs and a proposal may not choose. It is the walk's unit of output, and Generate turns it into
// exactly one ledger.CommitRequest.
type batchPlan struct {
	Proposal  strategy.BatchProposal
	SourceRef string
}

// walk drives the deterministic plan, calling emit once per batch in commit order.
//
// ONE implementation, two consumers — Counts sums what it yields and Generate commits it — so the
// two can never disagree about what a profile contains. The seeded Rng is constructed here rather
// than passed in, so both consumers consume the same sequence in the same order.
func (p Profile) walk(emit func(batchPlan) error) error {
	if err := p.Validate(); err != nil {
		return err
	}

	rng := ledger.NewRng(p.Seed)

	// roster is a scratch permutation of account indices, partially shuffled in place per tick. It
	// is reused across the whole walk rather than reallocated per tick: the shuffle is deterministic
	// given the Rng sequence, and carrying the previous night's order into the next draw is closer
	// to a real roster (last night's raiders are likelier to be near the front) than a fresh
	// permutation would be. Nothing depends on that resemblance; what matters is that it is
	// deterministic and allocation-free.
	roster := make([]int, p.Accounts)
	for i := range roster {
		roster[i] = i
	}

	if err := p.emitOpening(emit); err != nil {
		return err
	}

	at := p.FirstRaidAt
	step := core.Micros(p.BetweenRaids.Microseconds())

	for r := range p.Raids {
		if err := p.emitRaid(emit, rng, roster, r, at); err != nil {
			return err
		}

		if p.DecayEveryRaids > 0 && (r+1)%p.DecayEveryRaids == 0 {
			if err := p.emitDecay(emit, rng, (r+1)/p.DecayEveryRaids, at); err != nil {
				return err
			}
		}

		at += step
	}

	return nil
}

// emitOpening posts the start-points batch: every account credited its opening balance.
//
// kind='seed' is the one place the seed vocabulary is used as a batch kind, and it is the honest
// place for it: an opening balance handed out by the generator is not attendance, not an award and
// not an import. Every other batch below carries the kind a real guild's would, because a profile
// whose 520k entries were all kind='seed' would give ix_batch_kind a selectivity production will
// never see and make every measurement against it optimistic.
func (p Profile) emitOpening(emit func(batchPlan) error) error {
	entries := make([]strategy.EntryProposal, 0, p.Accounts)

	for i := range p.Accounts {
		entries = append(entries, strategy.EntryProposal{
			AccountID:   p.AccountID(i),
			BalanceKind: strategy.BalanceKindDKP,
			AmountCp:    p.StartPointsCp,
		})
	}

	return emit(batchPlan{
		SourceRef: "seed:" + p.Name + ":opening",
		Proposal: strategy.BatchProposal{
			Kind:            kinds.KindSeed,
			StrategyID:      "start_points",
			StrategyVersion: seedStrategyVersion,
			Reason:          "seed.Perf opening balances",
			EffectiveAt:     p.FirstRaidAt,
			Entries:         entries,
		},
	})
}

// emitRaid posts one raid night: its attendance ticks, then its zero-sum awards, then its
// fixed-price awards.
func (p Profile) emitRaid(
	emit func(batchPlan) error, rng *ledger.Rng, roster []int, raid int, at core.Micros,
) error {
	raidID := DeterministicID(tagRaid, raid)

	// The night's roster, drawn once and reused by the ticks and the awards — an item is won by
	// somebody who was there, and the split goes to the people who were there.
	attendees := p.drawAttendees(rng, roster)

	for t := range p.TicksPerRaid {
		if err := p.emitTick(emit, attendees, raidID, raid, t, at); err != nil {
			return err
		}
	}

	for a := range p.ZeroSumAwardsPerRaid {
		if err := p.emitZeroSumAward(emit, rng, attendees, raidID, raid, a, at); err != nil {
			return err
		}
	}

	for a := range p.FixedPriceAwardsPerRaid {
		if err := p.emitFixedPriceAward(emit, rng, attendees, raidID, raid, a, at); err != nil {
			return err
		}
	}

	return nil
}

// drawAttendees returns the roster present at this raid, as account indices in ascending order.
//
// A partial Fisher-Yates over the scratch slice: n draws, no allocation, and every account equally
// likely, which a rotating contiguous window would not be. Contiguity would matter: account ids are
// order-preserving, so a window would make each night's entries cluster into a narrow range of
// ix_entry_balance and give the index a write locality production does not have.
//
// The result is SORTED before it is returned, and that is not cosmetic. Entries then arrive in
// account order, which fixes the order of the snapshot upserts inside the commit (commit.go
// iterates first-appearance order for exactly this reason) and therefore the statement order a
// budget or an EXPLAIN golden would otherwise see change run to run.
func (p Profile) drawAttendees(rng *ledger.Rng, roster []int) []int {
	n := p.MinAttendees + rng.IntN(p.MaxAttendees-p.MinAttendees+1)

	for i := range n {
		j := i + rng.IntN(len(roster)-i)
		roster[i], roster[j] = roster[j], roster[i]
	}

	attendees := make([]int, n)
	copy(attendees, roster[:n])
	slices.Sort(attendees)

	return attendees
}

// emitTick posts one attendance tick: a flat credit to everybody present.
func (p Profile) emitTick(
	emit func(batchPlan) error, attendees []int, raidID core.ULID, raid, tick int, at core.Micros,
) error {
	tickID := DeterministicID(tagTick, raid*p.TicksPerRaid+tick)
	entries := make([]strategy.EntryProposal, 0, len(attendees))

	for _, i := range attendees {
		characterID := p.CharacterID(i)

		entries = append(entries, strategy.EntryProposal{
			AccountID: p.AccountID(i),
			// Attribution only — it never affects a balance, and it is here because a real tick
			// records which character was in the raid dump.
			CharacterID: &characterID,
			BalanceKind: strategy.BalanceKindDKP,
			AmountCp:    p.TickValueCp,
			RaidID:      &raidID,
			TickID:      &tickID,
		})
	}

	return emit(batchPlan{
		SourceRef: fmt.Sprintf("seed:%s:tick:%d:%d", p.Name, raid, tick),
		Proposal: strategy.BatchProposal{
			Kind:            kinds.KindAttendance,
			StrategyID:      "tick",
			StrategyVersion: seedStrategyVersion,
			EffectiveAt:     at,
			Entries:         entries,
		},
	})
}

// emitZeroSumAward posts an item won under a closed economy: the winner is debited and everybody
// else present is credited, with the credits summing to EXACTLY the debit.
//
// The split goes through ledger.Allocate rather than through arithmetic written here, so the seeded
// ledger contains real largest-remainder allocations with the account_id tiebreak actually applied —
// remainders and all. A generator that divided evenly and dropped the remainder would produce a
// ledger in which SumZero happens to hold, which is the one dataset that could never catch a
// conservation bug.
func (p Profile) emitZeroSumAward(
	emit func(batchPlan) error, rng *ledger.Rng, attendees []int,
	raidID core.ULID, raid, award int, at core.Micros,
) error {
	price := p.drawPrice(rng)
	winner := attendees[rng.IntN(len(attendees))]

	itemID := DeterministicID(tagItem, raid*16+award)
	awardID := DeterministicID(tagAward, raid*16+award)

	// Everybody present except the winner. A guild that credits the winner too is a legal variant;
	// excluding them is the classic reading and it keeps one account out of two entries in the same
	// batch, so the snapshot delta per account stays one row.
	shares := make([]ledger.Share, 0, len(attendees))

	for _, i := range attendees {
		if i == winner {
			continue
		}

		shares = append(shares, ledger.Share{AccountID: p.AccountID(i), Weight: 1})
	}

	credits, err := ledger.Allocate(price, shares, ledger.AccountIDGuildBank)
	if err != nil {
		return fmt.Errorf("allocate %d centipoints across %d shares on raid %d: %w",
			price, len(shares), raid, err)
	}

	winnerCharacter := p.CharacterID(winner)

	entries := make([]strategy.EntryProposal, 0, len(credits)+1)
	entries = append(entries, strategy.EntryProposal{
		AccountID:   p.AccountID(winner),
		CharacterID: &winnerCharacter,
		BalanceKind: strategy.BalanceKindDKP,
		AmountCp:    -price,
		ItemID:      &itemID,
		ItemAwardID: &awardID,
		RaidID:      &raidID,
	})

	for _, c := range credits {
		entries = append(entries, strategy.EntryProposal{
			AccountID:   c.AccountID,
			BalanceKind: strategy.BalanceKindDKP,
			AmountCp:    c.AmountCp,
			ItemAwardID: &awardID,
			RaidID:      &raidID,
		})
	}

	return emit(batchPlan{
		SourceRef: fmt.Sprintf("seed:%s:zerosum:%d:%d", p.Name, raid, award),
		Proposal: strategy.BatchProposal{
			Kind:            kinds.KindAward,
			StrategyID:      "zero_sum",
			StrategyVersion: seedStrategyVersion,
			EffectiveAt:     at,
			Entries:         entries,
			// Declared, not decorative: the ledger's engine checks both at commit time, so every
			// zero-sum batch in the seeded dataset has been through the conservation rules rather
			// than merely been written by code that meant to satisfy them.
			Invariants: []strategy.Invariant{
				{Kind: strategy.InvariantSumZero, BalanceKind: strategy.BalanceKindDKP},
				{Kind: strategy.InvariantLargestRemainderSumsToDebit, BalanceKind: strategy.BalanceKindDKP},
			},
		},
	})
}

// emitFixedPriceAward posts an item bought at a fixed price: one debit, and the points leave the
// economy. It is the other half of the realistic mix — a ledger of nothing but zero-sum batches has
// a net_amount_cp of zero on every row, and the standings distribution it produces is far flatter
// than a real guild's.
func (p Profile) emitFixedPriceAward(
	emit func(batchPlan) error, rng *ledger.Rng, attendees []int,
	raidID core.ULID, raid, award int, at core.Micros,
) error {
	price := p.drawPrice(rng)
	buyer := attendees[rng.IntN(len(attendees))]

	itemID := DeterministicID(tagItem, raid*16+p.ZeroSumAwardsPerRaid+award)
	awardID := DeterministicID(tagAward, raid*16+p.ZeroSumAwardsPerRaid+award)
	buyerCharacter := p.CharacterID(buyer)

	return emit(batchPlan{
		SourceRef: fmt.Sprintf("seed:%s:fixed:%d:%d", p.Name, raid, award),
		Proposal: strategy.BatchProposal{
			Kind:            kinds.KindAward,
			StrategyID:      "fixed_price",
			StrategyVersion: seedStrategyVersion,
			EffectiveAt:     at,
			Entries: []strategy.EntryProposal{{
				AccountID:   p.AccountID(buyer),
				CharacterID: &buyerCharacter,
				BalanceKind: strategy.BalanceKindDKP,
				AmountCp:    -price,
				ItemID:      &itemID,
				ItemAwardID: &awardID,
				RaidID:      &raidID,
			}},
		},
	})
}

// emitDecay posts one decay run over the whole roster.
//
// No character attribution: decay is not something a character did. The debit is drawn from the
// seeded Rng rather than computed as a percentage of a balance, which is the "synthetic economics"
// trade the package comment argues — computing it would mean reading 280 balances per run, and it
// would make the plan depend on the database, which is what makes Counts() worth having.
func (p Profile) emitDecay(emit func(batchPlan) error, rng *ledger.Rng, period int, at core.Micros) error {
	entries := make([]strategy.EntryProposal, 0, p.Accounts)
	span := int(p.MaxDecayCp - p.MinDecayCp + 1)

	for i := range p.Accounts {
		entries = append(entries, strategy.EntryProposal{
			AccountID:   p.AccountID(i),
			BalanceKind: strategy.BalanceKindDKP,
			AmountCp:    -(p.MinDecayCp + core.Centipoints(rng.IntN(span))),
		})
	}

	return emit(batchPlan{
		// Real decay's idempotency key is (pool_id, cadence_period); the source_ref carries the
		// period here for the same reason — a second run of the same period must be a collision.
		SourceRef: fmt.Sprintf("seed:%s:decay:%d", p.Name, period),
		Proposal: strategy.BatchProposal{
			Kind:            kinds.KindDecay,
			StrategyID:      "decay_percent",
			StrategyVersion: seedStrategyVersion,
			Reason:          fmt.Sprintf("seeded decay run, period %d", period),
			EffectiveAt:     at,
			Entries:         entries,
		},
	})
}

// drawPrice returns an item price in the profile's range, inclusive at both ends.
func (p Profile) drawPrice(rng *ledger.Rng) core.Centipoints {
	span := int(p.MaxItemPriceCp - p.MinItemPriceCp + 1)

	return p.MinItemPriceCp + core.Centipoints(rng.IntN(span))
}

// seedStrategyVersion is the strategy_version stamped on every seeded batch.
//
// 0.0.0 on purpose. The strategies these batches name — tick, zero_sum, fixed_price, decay_percent,
// start_points — are Phase 1 deliverables that mostly do not exist yet, and stamping a plausible
// semver would claim a seeded batch was planned by a planner that never ran. A batch that says it
// was planned by version zero of a strategy is saying it was not planned by one at all.
const seedStrategyVersion = "0.0.0"
