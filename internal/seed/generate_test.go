package seed_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger/kinds"
	"github.com/prokopto-dev/dragonkillparty/internal/seed"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The generator's tests: what actually reaches the database.
//
// Everything here runs at a TINY scale — a handful of raids, a short decay cadence, a roster of a
// few dozen — because what is under test is the code path, not the volume. The volume is
// internal/ledger/standings_perf_test.go's subject, and it pays for it: `make test` runs `-race`,
// and the detector instruments modernc.org/sqlite, which is the SQLite engine compiled to Go, so
// every seeded entry costs roughly thirty times what it costs uninstrumented.
//
// The division of labour is deliberate. These tests cover every batch kind the walk emits, both
// degenerate paths and both refusal paths, in under a second. The perf suite covers one thing these
// cannot: that the properties still hold after twenty thousand transactions.

// tinyProfile is the shape these tests use: small enough to be fast, wide enough to emit every kind
// of batch the profile knows how to plan — including decay, whose real cadence of one run per 56
// raids would otherwise never fire at this size.
func tinyProfile() seed.Profile {
	p := seed.Perf().Scaled(4)
	p.Accounts = 24
	p.MinAttendees = 6
	p.MaxAttendees = 12
	p.DecayEveryRaids = 2

	return p
}

// seedClock is the clock the generator stamps recorded_at from. Fixed, and LATER than the profile's
// effective_at dates: recorded_at is system truth and is never backdated, while the seeded raids are
// game truth and deliberately in the past.
func seedClock() clock.Clock {
	return clock.NewFake(time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC))
}

// TestSeedGenerate_OverASeededDatabase asserts everything that is true of a database the generator
// has finished writing — seven properties over ONE seeded fixture.
//
// One fixture rather than seven, and the reason is measured rather than tidy-minded: `make test`
// runs `-race`, which instruments modernc.org/sqlite — the SQLite engine compiled to Go — so a seed
// costs roughly thirty times what it costs uninstrumented. Seven independent seeds of the same
// profile were the largest single contributor to this PR's suite cost: cold `make test` measured
// 44.4 s on main and 53.9 s with them, and 45.1 s with this one fixture. Same seven assertions,
// same rows, nine seconds back.
//
// Every subtest here is READ-ONLY, which is what makes sharing safe. The three tests below that are
// not — determinism needs three separate databases, the refusal needs a populated one, and the
// validation test needs an empty one — keep their own, because sharing a fixture with a test that
// writes is how a suite starts depending on its own order.
func TestSeedGenerate_OverASeededDatabase(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	p := tinyProfile()

	report, err := seed.Generate(t.Context(), s, seedClock(), p, nil)
	require.NoError(t, err)

	t.Run("wrote exactly what the profile planned", func(t *testing.T) {
		testWritesExactlyWhatTheProfilePlanned(t, s, p, report)
	})

	t.Run("emits every batch kind the profile plans", func(t *testing.T) {
		testEmitsEveryBatchKindTheProfilePlans(t, s)
	})

	t.Run("zero-sum batches conserve exactly", func(t *testing.T) {
		testZeroSumBatchesConserveExactly(t, s)
	})

	t.Run("the snapshot matches the ledger", func(t *testing.T) {
		testSnapshotMatchesTheLedger(t, s, p, report)
	})

	t.Run("entries carry their provenance", func(t *testing.T) {
		testEntriesCarryTheirProvenance(t, s)
	})

	t.Run("source refs are unique per pool", func(t *testing.T) {
		testSourceRefsAreUniquePerPool(t, s, report)
	})

	t.Run("effective and recorded times tell the truth", func(t *testing.T) {
		testEffectiveAndRecordedTimesTellTheTruth(t, s)
	})
}

// testWritesExactlyWhatTheProfilePlanned asserts the database ends up holding the rows Counts()
// predicted — and that they went in through the real commit path, which is what makes them worth
// measuring against.
func testWritesExactlyWhatTheProfilePlanned(t *testing.T, s *store.Store, p seed.Profile, report seed.Report) {
	planned, err := p.Counts()
	require.NoError(t, err)

	require.Equal(t, planned.Batches, report.Batches)
	require.Equal(t, planned.Entries, report.Entries)
	require.Equal(t, p.Accounts, report.Accounts)

	require.Equal(t, int64(planned.Batches), count(t, s, `SELECT count(*) FROM ledger_batch`))
	require.Equal(t, int64(planned.Entries), count(t, s, `SELECT count(*) FROM ledger_entry`))
	require.Equal(t, int64(p.Accounts+len(ledger.SystemAccountIDs())),
		count(t, s, `SELECT count(*) FROM account`))

	head, err := ledger.MaxPoolSeq(t.Context(), s.Q(), p.PoolID)
	require.NoError(t, err)
	require.Equal(t, int64(planned.Batches), head, "one seq per committed batch")
	require.Equal(t, head, report.HeadSeq)

	// The hash chain advanced, which is only true if every batch went through Commit rather than
	// through an insert somebody wrote to go faster.
	require.Equal(t, int64(1),
		count(t, s, `SELECT count(*) FROM dkp_meta WHERE key = 'ledger_head:`+p.PoolID.String()+`'`),
		"the pool's hash-chain head exists, so the batches were committed rather than inserted")

	// One audit row and one outbox event per batch, for the same reason: Commit writes them, and
	// nothing else in this package can.
	require.Equal(t, int64(planned.Batches), count(t, s, `SELECT count(*) FROM audit_log`))
	require.Equal(t, int64(planned.Batches), count(t, s, `SELECT count(*) FROM event_outbox`))
}

// testEmitsEveryBatchKindTheProfilePlans asserts the seeded ledger has the mix a real
// guild's does, rather than 500,000 rows of one kind.
//
// It matters for what the dataset is FOR: ix_batch_kind is a real index, and a profile whose every
// batch shared a kind would give it a selectivity production never sees — so a measurement taken
// against it would flatter every query that filters on kind.
func testEmitsEveryBatchKindTheProfilePlans(t *testing.T, s *store.Store) {
	for _, kind := range []string{kinds.KindSeed, kinds.KindAttendance, kinds.KindAward, kinds.KindDecay} {
		require.Positive(t,
			count(t, s, `SELECT count(*) FROM ledger_batch WHERE kind = '`+kind+`'`),
			"the profile must emit at least one %q batch", kind)
	}

	// And both award strategies, since they produce structurally different batches: one debit that
	// leaves the economy, versus a debit and a fan of credits that sum back to it.
	for _, strategyID := range []string{"tick", "zero_sum", "fixed_price", "decay_percent", "start_points"} {
		require.Positive(t,
			count(t, s, `SELECT count(*) FROM ledger_batch WHERE strategy_id = '`+strategyID+`'`),
			"the profile must emit at least one %q batch", strategyID)
	}
}

// testZeroSumBatchesConserveExactly asserts every zero-sum award nets to exactly zero.
//
// Checked at the COLUMN rather than in the planner: net_amount_cp is written by Commit as the sum of
// the entries it actually wrote, so a split that minted or destroyed a centipoint shows up here even
// though the batch committed. The SumZero invariant the profile declares would have rejected it at
// commit time — this is the independent confirmation that it did.
func testZeroSumBatchesConserveExactly(t *testing.T, s *store.Store) {
	require.Positive(t, count(t, s, `SELECT count(*) FROM ledger_batch WHERE strategy_id = 'zero_sum'`),
		"there is something to check")
	require.Zero(t,
		count(t, s, `SELECT count(*) FROM ledger_batch WHERE strategy_id = 'zero_sum' AND net_amount_cp <> 0`),
		"every zero-sum award nets to exactly zero")

	// A fixed-price award is the opposite and must NOT net to zero: the points leave the economy.
	require.Zero(t,
		count(t, s, `SELECT count(*) FROM ledger_batch WHERE strategy_id = 'fixed_price' AND net_amount_cp >= 0`),
		"a fixed-price award is a debit; a non-negative net means the buyer was not charged")
}

// testSnapshotMatchesTheLedger asserts the cache the commit path maintained equals the
// sum over the log, for every account.
//
// The perf suite runs this property at 520k entries; running it here too is what makes a failure
// legible — a break shows up in a second, against 24 accounts whose numbers a human can read, rather
// than after three minutes of seeding.
func testSnapshotMatchesTheLedger(t *testing.T, s *store.Store, p seed.Profile, report seed.Report) {
	limit := int64(p.Accounts + len(ledger.SystemAccountIDs()))

	cached, err := ledger.StandingsFromSnapshot(
		t.Context(), s.Q(), p.PoolID, strategy.BalanceKindDKP, limit)
	require.NoError(t, err)
	require.NotEmpty(t, cached)

	folded, err := ledger.StandingsFromLedger(
		t.Context(), s.Q(), p.PoolID, strategy.BalanceKindDKP, report.HeadSeq, limit)
	require.NoError(t, err)

	require.Equal(t, folded, cached,
		"the cache and the fold agree on every account, in the same order, to the centipoint")
}

// TestSeedGenerate_IsDeterministic asserts two runs of the same profile produce the same BALANCES.
//
// Not the same ids and not the same hashes: core.Generator draws its ULID entropy from crypto/rand,
// so every row's primary key differs between runs and so does the chain. What must not differ is the
// arithmetic, because a perf comparison across two runs of the same profile is meaningless if the
// datasets were not the same dataset.
func TestSeedGenerate_IsDeterministic(t *testing.T) {
	t.Parallel()

	p := tinyProfile()

	first := standingsAfterSeeding(t, p)
	second := standingsAfterSeeding(t, p)

	require.Equal(t, first, second, "the same profile seeds the same balances")

	other := p
	other.Seed = p.Seed + 1

	require.NotEqual(t, first, standingsAfterSeeding(t, other),
		"a different seed seeds different balances; otherwise the seed is decoration")
}

// standingsAfterSeeding seeds a fresh database with p and returns its standings.
func standingsAfterSeeding(tb testing.TB, p seed.Profile) []ledger.Standing {
	tb.Helper()

	s := store.NewDB(tb)

	report, err := seed.Generate(tb.Context(), s, seedClock(), p, nil)
	require.NoError(tb, err)

	rows, err := ledger.StandingsFromSnapshot(tb.Context(), s.Q(), p.PoolID,
		strategy.BalanceKindDKP, int64(report.Accounts+len(ledger.SystemAccountIDs())))
	require.NoError(tb, err)

	return rows
}

// TestSeedGenerate_NonEmptyPool_Refuses asserts a second seed onto the same pool is refused rather
// than appended.
//
// The ledger is append-only, so there is no way back from a top-up: the resulting database matches
// no profile's row counts and every measurement taken against it is unattributable. The recovery is
// to delete the file, which is a thing a developer can do to a seeded database and can never do to a
// guild's.
func TestSeedGenerate_NonEmptyPool_Refuses(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	p := tinyProfile().Scaled(1)

	_, err := seed.Generate(t.Context(), s, seedClock(), p, nil)
	require.NoError(t, err)

	before := count(t, s, `SELECT count(*) FROM ledger_batch`)

	_, err = seed.Generate(t.Context(), s, seedClock(), p, nil)
	require.ErrorIs(t, err, seed.ErrPoolNotEmpty)

	require.Equal(t, before, count(t, s, `SELECT count(*) FROM ledger_batch`),
		"the refused run wrote nothing")
}

// TestSeedGenerate_InvalidProfile_WritesNothing asserts validation happens before the first write.
// A profile rejected halfway through would leave a partial dataset in an append-only table.
func TestSeedGenerate_InvalidProfile_WritesNothing(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)

	p := tinyProfile()
	p.MaxAttendees = p.Accounts + 1

	_, err := seed.Generate(t.Context(), s, seedClock(), p, nil)
	require.ErrorIs(t, err, seed.ErrInvalidProfile)

	require.Zero(t, count(t, s, `SELECT count(*) FROM ledger_batch`))
	require.Zero(t, count(t, s, `SELECT count(*) FROM account WHERE kind = 'person'`),
		"not even the roster: validation runs before the accounts are inserted")
}

// testEntriesCarryTheirProvenance asserts the seeded entries look like real ones —
// raid, tick, character and item attribution where a real batch would carry it, and none where it
// would not.
//
// It is not cosmetic. Those columns are nullable TEXT of ULID width, four of them per entry, and
// they are most of a ledger_entry row's size. A profile that left them null would produce a table
// and a set of indexes materially smaller than production's, and the page counts the V5 verdict
// rests on are counted off exactly that.
func testEntriesCarryTheirProvenance(t *testing.T, s *store.Store) {
	require.Positive(t, count(t, s,
		`SELECT count(*) FROM ledger_entry WHERE raid_id IS NOT NULL AND tick_id IS NOT NULL
		   AND character_id IS NOT NULL`),
		"attendance entries name the raid, the tick and the character that was in the dump")

	require.Positive(t, count(t, s,
		`SELECT count(*) FROM ledger_entry WHERE item_id IS NOT NULL AND item_award_id IS NOT NULL`),
		"an item's debit names the item and the award")

	// Decay is not something a character did, so a decay entry carries no character.
	require.Zero(t, count(t, s,
		`SELECT count(*) FROM ledger_entry e JOIN ledger_batch b ON b.id = e.batch_id
		 WHERE b.kind = 'decay' AND e.character_id IS NOT NULL`),
		"decay is posted against an account, not against a character")
}

// testSourceRefsAreUniquePerPool asserts every batch carries a distinct, greppable
// source_ref.
//
// ux_batch_srcref makes a duplicate a constraint violation, so this passing at all means the walk
// never planned the same receipt twice — and the "seed:" prefix is how an operator looking at a
// database tells generated history from real history.
func testSourceRefsAreUniquePerPool(t *testing.T, s *store.Store, report seed.Report) {
	require.Equal(t, int64(report.Batches),
		count(t, s, `SELECT count(DISTINCT source_ref) FROM ledger_batch`),
		"every batch has its own source_ref")
	require.Equal(t, int64(report.Batches),
		count(t, s, `SELECT count(*) FROM ledger_batch WHERE source_ref LIKE 'seed:%'`),
		"every seeded batch is identifiable as one")
}

// testEffectiveAndRecordedTimesTellTheTruth asserts the two clocks say different, correct things:
// the raids happened in the past (game truth, backdated) and the rows were written now (system
// truth, never backdated).
func testEffectiveAndRecordedTimesTellTheTruth(t *testing.T, s *store.Store) {
	recordedAt := strconv.FormatInt(int64(core.FromTime(seedClock().Now())), 10)

	require.Zero(t, count(t, s,
		`SELECT count(*) FROM ledger_batch WHERE recorded_at <> `+recordedAt),
		"recorded_at is the injected clock's, on every batch")

	require.Zero(t, count(t, s,
		`SELECT count(*) FROM ledger_batch WHERE effective_at >= recorded_at`),
		"every seeded raid is backdated: it happened before the software heard about it")
}

// count runs a single-value count query through the read pool.
func count(tb testing.TB, s *store.Store, query string) int64 {
	tb.Helper()

	var n int64
	require.NoError(tb, s.QueryRowForTest(tb, query).Scan(&n))

	return n
}
