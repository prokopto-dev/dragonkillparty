package ledger_test

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/seed"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/strategy"
)

// The V5 spike: what the standings read costs over a real guild's ledger, and whether
// balance_snapshot survives. Phase 1, issue #190.
//
// docs/development/verify-before-phase-0.md item V5 says: "Hand-write the four queries against a
// generated 520k-row ledger and measure, BEFORE any API exists." This is that measurement, promoted
// from a hand-written query to the product's own — internal/seed builds the ledger through
// ledger.Service.Commit, and the two arms measured here are ledger.StandingsFromSnapshot and
// ledger.StandingsFromLedger, so the thing under the stopwatch is the thing that will ship.
//
// FOUR QUESTIONS, one seeded database:
//
//  1. Did the seed write exactly what the profile planned? (Otherwise nothing below means anything.)
//  2. Is the standings read inside its statement budget?
//  3. Do the two arms — the cache and the definitional SUM — agree, account for account?
//  4. Does a full replay of every entry reproduce balance_snapshot EXACTLY: amount, entry count and
//     as-of-seq, for every row?
//
// and then the measurement itself, which is question 5: how long does each arm take, and how much
// I/O does each one need on storage that is not an NVMe SSD.
//
// ON SCALE. DKP_PERF_RAIDS sizes the run, and there is exactly ONE code path at every size: a small
// raid count on every PR so the generator and these assertions cannot rot unnoticed, and the full
// 3,400 under `make test-perf` and nightly. That is the shape DKP_PROPERTY_CHECKS already uses, and
// the reason is the same — a cheap lane that compiled differently from the expensive one would prove
// nothing about it. There is no skip here and no build tag: a suite that does not run is a suite
// nobody knows works, and #188 made a skip that lies a CI failure.
//
// ON THE WALL CLOCK. This file reads clock.System, which is the one sanctioned real-clock path out
// of internal/clock (CLOCK001 bans time.Now; CLOCK002 bans clock.System in internal/strategy, and
// this is neither). A latency measurement is the one thing in the repository that legitimately
// depends on how long something took.

const (
	// perfRaidsEnv sizes the seeded ledger. Unset means defaultPerfRaids.
	perfRaidsEnv = "DKP_PERF_RAIDS"

	// defaultPerfRaids is the per-PR size, and it is small on purpose.
	//
	// `make test` runs `-race`, and the detector instruments modernc.org/sqlite — the SQLite engine
	// compiled to Go — so seeding costs roughly thirty times what it costs uninstrumented. Eight
	// raids is ~1,400 entries: enough that the plan-versus-database assertion, the two-arm
	// comparison and the replay all run on real multi-batch data on every PR, and cheap enough not
	// to dominate the suite. The magnitude that V5 is actually about comes from
	// DKP_PERF_RAIDS=3400 under `make test-perf` and nightly.
	//
	// It does NOT cross a decay period (seed.Perf posts one every 56 raids), and that is deliberate
	// rather than an oversight: the decay path is covered by internal/seed's own tests, which
	// configure a short cadence and cost milliseconds. Paying eight times the suite time here to
	// re-cover it would buy nothing.
	defaultPerfRaids = 8

	// standingsStatementBudget is V5's number for the whole /standings page at 280 members
	// (.claude/rules/store-and-sql.md restates it). The balance half of that page is ONE statement
	// today; the other three are the headroom the endpoint spends on attendance and the envelope.
	standingsStatementBudget = 4

	// standingsReadStatements is what the balance half actually costs. Asserted exactly, so that a
	// regression from one statement to two is loud while there is still budget to absorb it — a
	// gate that only fires at four would let the headroom be spent silently.
	standingsReadStatements = 1

	// standingsP99Budget is V5's latency ceiling: 150 ms p99 on SD-card-class storage.
	standingsP99Budget = 150 * time.Millisecond

	// latencySamples is how many times the CACHE arm is timed. 200 puts two samples above the p99,
	// which is the fewest that makes the number mean anything at all, and it costs about a tenth of
	// a second in total.
	latencySamples = 200

	// controlSamples is how many times the LEDGER arm is timed. Far fewer, because at 520k entries
	// each run is hundreds of milliseconds and 200 of them would be most of the suite's wall clock
	// for a number nothing is asserted against. Twenty-five samples cannot support a p99 and this
	// file does not claim one for it: the control is reported as a median and a worst-of-25, which
	// is enough to establish an order of magnitude and is labelled as exactly that.
	controlSamples = 25

	// sdCardMillisPerPage models the storage V5 names, and it is the load-bearing assumption in this
	// file, so it is stated rather than implied.
	//
	// The SD Association's A1 rating floor is 1,500 random 4 KiB read IOPS, or 0.67 ms per page. An
	// unrated card — which is what is in most Raspberry Pis — does considerably worse. 2 ms per page
	// is deliberately pessimistic by roughly 3x against the A1 floor, because a p99 is the cold,
	// contended case: the tail is exactly where the page cache has not got the page and something
	// else is writing.
	//
	// It is a MODEL, not a measurement, and it cannot be anything else on this hardware — there is
	// no portable way to drop the OS page cache from a Go test. What makes the model honest is that
	// the quantity it multiplies IS measured: dbstat reports the exact number of pages each arm must
	// read, so the only estimate here is the per-page cost.
	sdCardMillisPerPage = 2

	// sqlitePageSize is the page size the database is built with. Asserted rather than assumed, so
	// the page arithmetic below cannot quietly be wrong.
	sqlitePageSize = 4096
)

// perfRaids returns the raid count for this run.
//
// A malformed value FAILS rather than falling back, for the reason propertyChecks gives: a nightly
// that quietly ran the 64-raid profile while reporting a 3,400-raid run is worse than no nightly.
func perfRaids(tb testing.TB) int {
	tb.Helper()

	raw, ok := os.LookupEnv(perfRaidsEnv)
	if !ok || raw == "" {
		return defaultPerfRaids
	}

	n, err := strconv.Atoi(raw)
	require.NoError(tb, err, "%s=%q is not a number", perfRaidsEnv, raw)
	require.Positive(tb, n, "%s must be positive, got %d", perfRaidsEnv, n)

	return n
}

// TestPerf_StandingsOverASeededLedger_MeetsItsBudgets is the V5 spike.
//
// One seeded database, shared by every subtest, because building it is the expensive part: at the
// full profile it is tens of seconds and a few hundred megabytes. The subtests do not run in
// parallel with each other — they share a statement counter, and a budget measured while another
// subtest was issuing queries would be noise.
func TestPerf_StandingsOverASeededLedger_MeetsItsBudgets(t *testing.T) {
	t.Parallel()

	profile := seed.Perf().Scaled(perfRaids(t))

	planned, err := profile.Counts()
	require.NoError(t, err)

	t.Logf("profile %q: %d raids, %d accounts, %d batches, %d entries",
		profile.Name, profile.Raids, planned.Accounts, planned.Batches, planned.Entries)

	// The seeded store carries NO statement counter, and that is not an optimisation detail worth
	// hiding: seeding the full profile executes ~2.5 million statements, and store.Counter retains
	// every one of their SQL texts (its own doc says so). Counting the fixture would cost a couple
	// of hundred megabytes to record work nothing measures. The counted handle below is opened
	// separately, on the same file, and has therefore only ever seen the reads under test.
	path := filepath.Join(t.TempDir(), "perf.db")
	store.CloneTemplate(t, path)

	seeded, err := store.Open(t.Context(), path)
	require.NoError(t, err, "open the uncounted seeding handle")
	t.Cleanup(func() { require.NoError(t, seeded.Close()) })

	report, err := seed.Generate(t.Context(), seeded, seedClock(), profile, nil)
	require.NoError(t, err, "generate the %s profile", profile.Name)

	counter := store.NewCounter()

	measured, err := store.Open(t.Context(), path, store.WithStatementCounter(counter))
	require.NoError(t, err, "open the counted measurement handle")
	t.Cleanup(func() { require.NoError(t, measured.Close()) })

	t.Run("the seed wrote exactly what the profile planned", func(t *testing.T) {
		requireSeedMatchesPlan(t, seeded, profile, planned, report)
	})

	t.Run("the standings read is one statement, inside a budget of four", func(t *testing.T) {
		requireStatementBudget(t, measured, counter, profile, planned)
	})

	t.Run("the cache and the definitional SUM agree on every account", func(t *testing.T) {
		requireArmsAgree(t, measured, profile, report)
	})

	t.Run("a full replay reproduces balance_snapshot exactly", func(t *testing.T) {
		requireReplayReproducesSnapshot(t, seeded, profile, planned)
	})

	// On the UNCOUNTED handle, like the replay above it and for the same reason: a full verification
	// issues one statement per batch plus one per page, which is ~41,000 at the full profile, and
	// store.Counter retains the SQL text of every one. The counted handle exists to measure the
	// standings read and must go on seeing only that.
	t.Run("dkp verify-ledger is clean over the whole seeded ledger", func(t *testing.T) {
		requireVerifyLedgerIsClean(t, seeded, profile, planned)
	})

	t.Run("the standings read meets its p99 on SD-card-class storage", func(t *testing.T) {
		requireLatencyBudget(t, measured, seeded, profile, report)
	})
}

// seedClock is the clock the generator stamps recorded_at and mints ids from. Fixed, so a run is
// reproducible; recorded_at is SYSTEM truth and a test's system did not exist in 2019, so the
// effective_at dates in the profile are deliberately older than this.
func seedClock() clock.Clock {
	return clock.NewFake(time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC))
}

// requireSeedMatchesPlan asserts the database contains exactly the rows Profile.Counts() predicted.
//
// This is the assertion that makes everything after it meaningful, and it is not a tautology: the
// plan is computed in Go without touching a database, and the rows were written by
// ledger.Service.Commit through the invariant engine, the seq allocator and the hash chain. A batch
// the engine rejected, an entry the CHECK dropped or a duplicate the unique index refused would all
// show up here as a shortfall.
func requireSeedMatchesPlan(
	tb testing.TB, s *store.Store, profile seed.Profile, planned seed.Counts, report seed.Report,
) {
	tb.Helper()

	require.Equal(tb, planned.Batches, report.Batches, "the generator committed every planned batch")
	require.Equal(tb, planned.Entries, report.Entries, "the generator wrote every planned entry")

	require.Equal(tb, int64(planned.Batches), countRows(tb, s, "ledger_batch"),
		"ledger_batch holds exactly the planned batches")
	require.Equal(tb, int64(planned.Entries), countRows(tb, s, "ledger_entry"),
		"ledger_entry holds exactly the planned entries")

	// The roster, plus the four system accounts the migration seeds.
	require.Equal(tb, int64(planned.Accounts+len(ledger.SystemAccountIDs())), countRows(tb, s, "account"),
		"account holds the roster plus the four seeded system accounts")

	// The head seq is the batch count, because the seed is the only writer and seq is per-pool.
	head, err := ledger.MaxPoolSeq(tb.Context(), s.Q(), profile.PoolID)
	require.NoError(tb, err)
	require.Equal(tb, int64(planned.Batches), head, "the pool head is one seq per committed batch")
	require.Equal(tb, head, report.HeadSeq, "the report's head matches the pool's")

	// Every zero-sum award really summed to zero, checked at the column rather than in the planner:
	// net_amount_cp is Sigma entries, written by Commit, so a split that minted a centipoint would
	// have a non-zero net here even though the batch committed.
	require.Zero(tb, countWhere(tb, s,
		`SELECT count(*) FROM ledger_batch WHERE strategy_id = 'zero_sum' AND net_amount_cp <> 0`),
		"every zero-sum batch has a net of exactly zero")
}

// requireStatementBudget asserts the standings read costs one statement, inside V5's budget of four.
func requireStatementBudget(
	tb testing.TB, s *store.Store, counter *store.Counter, profile seed.Profile, planned seed.Counts,
) {
	tb.Helper()

	counter.Reset()
	counter.Budget(tb, standingsStatementBudget)

	rows, err := ledger.StandingsFromSnapshot(
		tb.Context(), s.Q(), profile.PoolID, strategy.BalanceKindDKP, int64(planned.Accounts))
	require.NoError(tb, err)
	require.Len(tb, rows, planned.Accounts, "every account appears in the standings")

	require.Equal(tb, standingsReadStatements, counter.Count(),
		"the standings read must stay one statement; the budget of %d is the whole page's, and "+
			"spending it here leaves nothing for attendance\n%s", standingsStatementBudget, counter)

	// Highest first, with the account_id tiebreak. Asserted rather than assumed, because the whole
	// argument for reading the cache is that the index produces this order without a sort.
	require.True(tb, slices.IsSortedFunc(rows, func(a, b ledger.Standing) int {
		if a.AmountCp != b.AmountCp {
			// Descending by amount.
			return int(b.AmountCp - a.AmountCp)
		}

		// Ascending by account id.
		return int(a.AccountID[0]) - int(b.AccountID[0])
	}), "standings come back highest first")
}

// requireArmsAgree asserts the cache and the definitional SUM return the same balance and the same
// entry count for every account.
//
// This is the property that lets /standings read the cache at all. It is checked over the WHOLE
// roster rather than a sample, because a cache is not "mostly right" — one wrong account is one
// member being told a number they can disprove.
func requireArmsAgree(tb testing.TB, s *store.Store, profile seed.Profile, report seed.Report) {
	tb.Helper()

	limit := int64(report.Accounts + len(ledger.SystemAccountIDs()))

	cached, err := ledger.StandingsFromSnapshot(
		tb.Context(), s.Q(), profile.PoolID, strategy.BalanceKindDKP, limit)
	require.NoError(tb, err)

	folded, err := ledger.StandingsFromLedger(
		tb.Context(), s.Q(), profile.PoolID, strategy.BalanceKindDKP, report.HeadSeq, limit)
	require.NoError(tb, err)

	require.Equal(tb, len(folded), len(cached),
		"the cache holds a row for exactly the accounts the ledger has entries for")

	byAccount := make(map[core.ULID]ledger.Standing, len(folded))
	for _, f := range folded {
		byAccount[f.AccountID] = f
	}

	for _, c := range cached {
		f, ok := byAccount[c.AccountID]
		require.True(tb, ok, "account %s is in the cache but has no ledger entries", c.AccountID)
		require.Equal(tb, f.AmountCp, c.AmountCp, "cached balance for %s", c.AccountID)
		require.Equal(tb, f.EntryCount, c.EntryCount, "cached entry count for %s", c.AccountID)
	}

	// And the single-account read agrees with both, for the account holding the largest balance —
	// BalanceAsOfSeq is what a dispute is settled with, so it may not be a third opinion.
	top := cached[0]
	single, err := ledger.BalanceAsOfSeq(
		tb.Context(), s.Q(), profile.PoolID, top.AccountID, strategy.BalanceKindDKP, report.HeadSeq)
	require.NoError(tb, err)
	require.Equal(tb, top.AmountCp, single,
		"BalanceAsOfSeq agrees with the standings for %s", top.AccountID)
}

// foldedBalance is one account's balance rebuilt from the log: the sum, the number of entries that
// went into it, and the highest seq among them.
type foldedBalance struct {
	amountCp   core.Centipoints
	entryCount int64
	maxSeq     int64
}

// requireReplayReproducesSnapshot folds EVERY entry in the pool in Go and requires balance_snapshot
// to match it exactly — amount, entry count and as-of-seq — for every row, in both directions.
//
// This is TestSnapshot_TenThousandEntries_MatchesFold's property at guild scale and through the real
// write path. The 10k test writes its own rows and upserts its own deltas, which proves the upsert's
// arithmetic; this one never touches the cache at all — every row in it was written by
// ledger.Service.Commit while it was busy committing 520,000 entries — so what it proves is that the
// cache the PRODUCT maintains is still the fold after twenty thousand transactions.
//
// Three columns, not one. A cache that had folded the right total from the wrong number of entries,
// or that had stopped advancing its as-of-seq, would pass a sum-only comparison and be wrong in a
// way a member would eventually find.
//
// The fold does not order by seq, and does not need to: addition is commutative, so the sum is
// order-independent, and the ordering property of the log is what the hash chain covers rather than
// this. as_of_seq is reproduced as a MAX for the same reason — the cache advances it to the seq of
// the last batch that touched the account, which is order-independent too.
func requireReplayReproducesSnapshot(
	tb testing.TB, s *store.Store, profile seed.Profile, planned seed.Counts,
) {
	tb.Helper()

	// Keyed by the same type the cache is read into, so the two maps are compared key for key
	// rather than through a conversion that could itself be where they stopped disagreeing.
	fold := make(map[snapshotKey]foldedBalance, planned.Accounts)
	scanned := 0

	rows := s.QueryForTest(tb,
		`SELECT account_id, balance_kind, amount_cp, seq FROM ledger_entry WHERE pool_id = ?`,
		profile.PoolID.String())
	defer func() { require.NoError(tb, rows.Close()) }()

	for rows.Next() {
		var (
			accountID   string
			balanceKind string
			amountCp    int64
			seq         int64
		)

		require.NoError(tb, rows.Scan(&accountID, &balanceKind, &amountCp, &seq))

		k := snapshotKey{accountID: core.ULID(accountID), balanceKind: balanceKind}
		f := fold[k]
		f.amountCp += core.Centipoints(amountCp)
		f.entryCount++
		f.maxSeq = max(f.maxSeq, seq)
		fold[k] = f

		scanned++
	}

	require.NoError(tb, rows.Err())
	require.Equal(tb, planned.Entries, scanned, "the replay read every entry in the pool")

	cached := readAllSnapshots(tb, s, profile.PoolID)

	require.Equal(tb, len(fold), len(cached),
		"balance_snapshot holds a row for exactly the (account, balance kind) pairs the log has "+
			"entries for — an extra row is a cache entry with nothing behind it, a missing one is an "+
			"account whose balance the page would report as absent")

	for k, want := range fold {
		got, ok := cached[k]
		require.True(tb, ok, "no cached balance for %s kind %q", k.accountID, k.balanceKind)

		require.Equal(tb, want.amountCp, got.amountCp,
			"replayed balance for %s kind %q", k.accountID, k.balanceKind)
		require.Equal(tb, want.entryCount, got.entryCount,
			"replayed entry count for %s kind %q", k.accountID, k.balanceKind)
		require.Equal(tb, want.maxSeq, got.asOfSeq,
			"cached as_of_seq for %s kind %q must be the last seq that touched it",
			k.accountID, k.balanceKind)
	}
}

// requireVerifyLedgerIsClean runs the PRODUCT'S verifier — the engine behind `dkp verify-ledger`
// (issue #198) — over the whole seeded ledger and requires it to find nothing.
//
// It is deliberately not a replacement for requireReplayReproducesSnapshot above, and the difference
// is the point of having both. That one folds the entries with its own hand-written scan and its own
// map, so it is an INDEPENDENT second opinion about the cache; this one runs the code an operator
// runs, over the same rows, and so also covers the two hash chains, the dkp_meta heads and the
// per-batch summary columns, none of which a fold can see. Two implementations agreeing about the
// balances, and one of them additionally attesting the log they came from, is a stronger statement
// than either alone — and if they ever disagree, one of them is the bug.
//
// This is also the acceptance criterion of issue #198 executed at its real scale: `make test` runs
// it at eight raids on every PR, `make test-perf` and nightly-verify.yml's `replay / seed.Perf` job
// run it at 3,400, over rows that ledger.Service.Commit wrote through the invariant engine, the seq
// allocator and the hash chain.
//
// The counts are asserted, not just the verdict. A verifier that read nothing reports clean too, and
// "clean" is exactly the word that would hide it.
func requireVerifyLedgerIsClean(
	tb testing.TB, s *store.Store, profile seed.Profile, planned seed.Counts,
) {
	tb.Helper()

	// Through store.ReadTx, which is how the command runs it: one consistent snapshot for the whole
	// replay. Nothing writes to this database while the subtest runs, so isolation buys nothing here
	// — what it buys is that the path under test is the path that ships.
	var report ledger.Report

	require.NoError(tb, s.ReadTx(tb.Context(), func(ctx context.Context, q store.Queries) error {
		var err error

		report, err = ledger.Verify(ctx, q, ledger.VerifyOptions{})

		return err
	}), "the replay must be able to read the seeded database")

	require.True(tb, report.Clean(),
		"a ledger written entirely by ledger.Service.Commit must verify clean; %d finding(s):\n%v",
		report.FindingCount, report.Findings)

	require.Equal(tb, int64(planned.Batches), report.Batches(),
		"the replay walked every batch the profile planned")
	require.Equal(tb, int64(planned.Entries), report.Entries(),
		"the replay folded every entry the profile planned")
	require.Positive(tb, report.Snapshots(), "the replay compared the cached balances")
	require.Equal(tb, int64(planned.Batches), report.Audit.Rows,
		"the commit path writes one audit row per batch, so the audit chain is the same length")

	// The pool the profile seeded, and a chain head to show for it. The head is what a published
	// anchor will attest (docs/design/01-domain-model.md §9.6); today it is evidence that the walk
	// reached the end of the chain rather than stopping quietly at a page boundary.
	require.Len(tb, report.Pools, 1)
	require.Equal(tb, profile.PoolID, report.Pools[0].PoolID)
	require.Equal(tb, int64(planned.Batches), report.Pools[0].HeadSeq)
	require.Len(tb, report.Pools[0].Head, 2*sha256.Size, "the pool head is a hex SHA-256")
}

// requireLatencyBudget times both arms and holds the cache arm to V5's 150 ms p99 on SD-card-class
// storage.
//
// The measured number is a WARM number: the page cache holds the database, because a Go test cannot
// portably drop it. So the assertion is made against a DERIVED number instead — the measured p99
// plus the pages the query must read multiplied by a stated per-page cost for the storage V5 names.
// The page count is measured (dbstat), the per-page cost is modelled (sdCardMillisPerPage), and both
// halves are logged so the arithmetic can be checked by whoever reads the failure.
//
// The slow arm is measured but NOT asserted. It is the control: it is what /standings would cost
// with the cache deleted, and the gap between the two is the entire content of the V5 verdict. Its
// derived figure is logged so the verdict has a number rather than an adjective.
func requireLatencyBudget(
	tb testing.TB, measured, seeded *store.Store, profile seed.Profile, report seed.Report,
) {
	tb.Helper()

	limit := int64(report.Accounts + len(ledger.SystemAccountIDs()))
	wall := clock.System{}

	pages := pageCounts(tb, seeded)

	// The pages each arm must read.
	//
	// The cache arm walks ix_snapshot_standings and then probes the WITHOUT ROWID primary key for
	// as_of_seq and entry_count, which the index does not carry — so it touches both b-trees, and
	// both are counted. That probe is a measured cost rather than a suspected one, which is the
	// point of counting them separately below: if it ever mattered, the fix is a projection that
	// selects only what the page renders, not a migration.
	//
	// The ledger arm walks the whole of ix_entry_balance, which is a covering index over every entry
	// ever written.
	indexPages, tablePages := pages["ix_snapshot_standings"], pages["balance_snapshot"]
	cachedPages := indexPages + tablePages
	ledgerPages := pages["ix_entry_balance"]

	tb.Logf("standings over %d entries, %d accounts; database %s",
		report.Entries, report.Accounts, humanBytes(totalPages(pages)*sqlitePageSize))
	tb.Logf("  from balance_snapshot  %5d pages (%d index + %d table) -> %v of I/O on SD-card-class storage",
		cachedPages, indexPages, tablePages, pageCost(cachedPages))
	tb.Logf("  from the ledger SUM    %5d pages                     -> %v of I/O on SD-card-class storage",
		ledgerPages, pageCost(ledgerPages))
	tb.Logf("  model: %d ms per 4 KiB random read (the A1 rating floor is 0.67 ms; this is ~3x "+
		"pessimistic, because a p99 is the cold, contended case)", sdCardMillisPerPage)

	// THE I/O HALF, asserted in every build. A page count is a property of the schema and the data,
	// not of how the binary was compiled, so this is the part of the budget that means the same
	// thing under `-race` as without it — and it is the part that carries the SD-card claim, since
	// on storage that slow the query is I/O-bound and the CPU time is a rounding error.
	require.LessOrEqual(tb, pageCost(cachedPages), standingsP99Budget,
		"the standings read must read few enough pages to answer within %v on SD-card-class "+
			"storage (V5): %d pages at %d ms is %v",
		standingsP99Budget, cachedPages, sdCardMillisPerPage, pageCost(cachedPages))

	if raceEnabled {
		// Not a skip that hides anything: the assertion above ran, and the one below is not
		// measurable here. The race detector instruments modernc.org/sqlite — which IS the SQLite
		// engine, in Go — so a query's wall clock under `-race` is a measurement of the detector.
		// `make test-perf` runs without it and is where the timing gate lives.
		tb.Log("  wall clock not sampled: -race instruments the SQLite engine itself, so a latency " +
			"measured here would be the detector's. Run `make test-perf` for the timing half.")

		return
	}

	cached := sampleLatency(tb, wall, latencySamples, func() {
		_, err := ledger.StandingsFromSnapshot(
			tb.Context(), measured.Q(), profile.PoolID, strategy.BalanceKindDKP, limit)
		require.NoError(tb, err)
	})

	control := sampleLatency(tb, wall, controlSamples, func() {
		_, err := ledger.StandingsFromLedger(
			tb.Context(), measured.Q(), profile.PoolID, strategy.BalanceKindDKP, report.HeadSeq, limit)
		require.NoError(tb, err)
	})

	cachedP99 := quantile(cached, 99)
	controlWorst := control[len(control)-1]

	cachedDerived := cachedP99 + pageCost(cachedPages)
	controlDerived := controlWorst + pageCost(ledgerPages)

	tb.Logf("  from balance_snapshot  p50 %v  p99 %v  (n=%d)  -> %v with the page model",
		quantile(cached, 50), cachedP99, len(cached), cachedDerived)
	tb.Logf("  from the ledger SUM    p50 %v  worst %v  (n=%d)  -> %v with the page model",
		quantile(control, 50), controlWorst, len(control), controlDerived)

	// THE WALL-CLOCK HALF. The measured number is warm — a Go test cannot portably drop the OS page
	// cache — so the assertion is against the measurement PLUS the modelled I/O, which is the honest
	// composition of "what the CPU costs" and "what the storage costs".
	//
	// The control arm is measured and logged but never asserted. It is what /standings would cost
	// with the cache deleted, and the gap between the two lines above is the whole content of the
	// V5 verdict.
	require.LessOrEqual(tb, cachedDerived, standingsP99Budget,
		"the standings read must answer within %v p99 on SD-card-class storage (V5). "+
			"Measured p99 %v warm, plus %d pages at %d ms = %v",
		standingsP99Budget, cachedP99, cachedPages, sdCardMillisPerPage, cachedDerived)
}

// sampleLatency runs fn n times and returns the durations, sorted ascending.
//
// One warm-up run first, discarded: the first execution of a query through database/sql prepares the
// statement, and a prepare in the sample set measures the driver rather than the query.
func sampleLatency(tb testing.TB, wall clock.Clock, n int, fn func()) []time.Duration {
	tb.Helper()

	fn()

	samples := make([]time.Duration, n)

	for i := range samples {
		start := wall.Now()
		fn()
		samples[i] = wall.Now().Sub(start)
	}

	slices.Sort(samples)

	return samples
}

// quantile returns the q-th percentile of a sorted sample set, clamped to the last element.
func quantile(sorted []time.Duration, q int) time.Duration {
	i := len(sorted) * q / 100

	return sorted[min(i, len(sorted)-1)]
}

// pageCost is what n page reads cost under the SD-card model.
func pageCost(n int) time.Duration {
	return time.Duration(n*sdCardMillisPerPage) * time.Millisecond
}

// totalPages sums every b-tree in the database.
func totalPages(pages map[string]int) int {
	total := 0
	for _, n := range pages {
		total += n
	}

	return total
}

// humanBytes renders a byte count, for a log line a human reads rather than a value anything
// compares. KiB below a megabyte, so the small-scale run does not report "0 MiB".
func humanBytes(n int) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
	)

	if n < mib {
		return strconv.Itoa(n/kib) + " KiB"
	}

	return strconv.Itoa(n/mib) + " MiB"
}

// pageCounts returns the number of 4 KiB pages each table and index occupies, from SQLite's dbstat
// virtual table.
//
// This is the measured half of the SD-card model. dbstat walks every page in the database, so it is
// not cheap at 520k entries — it runs once, on the uncounted handle, and never inside a timed
// section.
func pageCounts(tb testing.TB, s *store.Store) map[string]int {
	tb.Helper()

	var pageSize int
	require.NoError(tb, s.QueryRowForTest(tb, `PRAGMA page_size`).Scan(&pageSize))
	require.Equal(tb, sqlitePageSize, pageSize,
		"the page arithmetic in this file assumes %d-byte pages", sqlitePageSize)

	pages := make(map[string]int)

	rows := s.QueryForTest(tb, `SELECT name, count(*) FROM dbstat GROUP BY name`)
	defer func() { require.NoError(tb, rows.Close()) }()

	for rows.Next() {
		var (
			name string
			n    int
		)

		require.NoError(tb, rows.Scan(&name, &n))
		pages[name] = n
	}

	require.NoError(tb, rows.Err())
	require.NotEmpty(tb, pages, "dbstat reported no pages; the page model below would be vacuous")

	return pages
}

// snapshotKey identifies one cached balance.
type snapshotKey struct {
	accountID   core.ULID
	balanceKind string
}

// snapshotRow is one balance_snapshot row, read whole so the replay can compare every column it
// caches rather than only the amount.
type snapshotRow struct {
	amountCp   core.Centipoints
	entryCount int64
	asOfSeq    int64
}

// readAllSnapshots reads every cached balance in a pool.
func readAllSnapshots(tb testing.TB, s *store.Store, poolID core.ULID) map[snapshotKey]snapshotRow {
	tb.Helper()

	out := make(map[snapshotKey]snapshotRow)

	rows := s.QueryForTest(tb,
		`SELECT account_id, balance_kind, amount_cp, entry_count, as_of_seq
		 FROM balance_snapshot WHERE pool_id = ?`, poolID.String())
	defer func() { require.NoError(tb, rows.Close()) }()

	for rows.Next() {
		var (
			accountID   string
			balanceKind string
			amountCp    int64
			entryCount  int64
			asOfSeq     int64
		)

		require.NoError(tb, rows.Scan(&accountID, &balanceKind, &amountCp, &entryCount, &asOfSeq))

		out[snapshotKey{accountID: core.ULID(accountID), balanceKind: balanceKind}] = snapshotRow{
			amountCp:   core.Centipoints(amountCp),
			entryCount: entryCount,
			asOfSeq:    asOfSeq,
		}
	}

	require.NoError(tb, rows.Err())

	return out
}

// countRows counts a table. The table name is a constant at every call site — it is interpolated
// because a table name cannot be a bound parameter.
func countRows(tb testing.TB, s *store.Store, table string) int64 {
	tb.Helper()

	return countWhere(tb, s, `SELECT count(*) FROM `+table)
}

// countWhere runs a single-value count query.
func countWhere(tb testing.TB, s *store.Store, query string) int64 {
	tb.Helper()

	var n int64
	require.NoError(tb, s.QueryRowForTest(tb, query).Scan(&n))

	return n
}
