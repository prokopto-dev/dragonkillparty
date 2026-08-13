package seed_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/seed"
)

// The profile's own tests: what a profile PROMISES, checked without a database.
//
// Profile.Counts() walks the same plan Generate commits, so everything asserted here is a fact about
// the dataset that will be written — and generate_test.go then asserts the database agrees. Keeping
// the two apart is what makes the second one evidence: if the plan and the rows were computed by the
// same code reading the same database, their agreement would prove nothing.

// TestSeedProfile_Perf_MeetsItsRowCountFloors is the row-count floor the ROADMAP asks every profile
// to carry ("seed_profile_test: row-count floors per profile, non-decreasing").
//
// The floor is item V3's guild: ~280 accounts, ~3,400 raids, ~520,000 ledger entries. Being wrong
// DOWNWARD is what costs something — every latency and statement budget in the roadmap is stated at
// this scale, so a profile that quietly shrank would turn the whole perf suite into a measurement of
// a smaller guild while still reporting green. Being wrong upward costs seconds of generation.
//
// The exact figure is asserted alongside the floor, because it is deterministic and a change to it
// should be a change somebody typed. A drift in either direction shows up here with both numbers.
func TestSeedProfile_Perf_MeetsItsRowCountFloors(t *testing.T) {
	t.Parallel()

	p := seed.Perf()

	require.Equal(t, 280, p.Accounts, "V3's roster")
	require.Equal(t, 3_400, p.Raids, "V3's raid count")

	counts, err := p.Counts()
	require.NoError(t, err)

	require.GreaterOrEqual(t, counts.Entries, seed.PerfEntryFloor,
		"the Perf profile must reach V3's ledger size; every budget measured against it assumes %d entries",
		seed.PerfEntryFloor)

	require.Equal(t, 527_164, counts.Entries,
		"the Perf profile's entry count is deterministic. If you changed the composition on purpose, "+
			"update this number and re-measure V5 — the standings verdict was taken at the old one")
	require.Equal(t, 20_461, counts.Batches, "and its batch count")
	require.Equal(t, 280, counts.Accounts)
}

// TestSeedProfile_Counts_AreNonDecreasingInRaids asserts a bigger profile is never a smaller
// dataset. It is the "non-decreasing" half of the ROADMAP's floor requirement, and it is what
// catches an off-by-one in the walk that made a scaled-down profile emit MORE than the full one.
func TestSeedProfile_Counts_AreNonDecreasingInRaids(t *testing.T) {
	t.Parallel()

	base := seed.Perf()

	var (
		prevBatches int
		prevEntries int
	)

	for _, raids := range []int{0, 1, 8, 64, 128, 512} {
		counts, err := base.Scaled(raids).Counts()
		require.NoError(t, err, "raids=%d", raids)

		require.GreaterOrEqual(t, counts.Batches, prevBatches, "batches at raids=%d", raids)
		require.GreaterOrEqual(t, counts.Entries, prevEntries, "entries at raids=%d", raids)

		prevBatches, prevEntries = counts.Batches, counts.Entries
	}
}

// TestSeedProfile_ZeroRaids_IsTheOpeningBatchAlone asserts the degenerate profile is still a legal
// dataset: no raids means the opening balances and nothing else. It is the boundary Scaled(0) hits,
// and a walk that divided by the raid count would fail here rather than in somebody's `make seed`.
func TestSeedProfile_ZeroRaids_IsTheOpeningBatchAlone(t *testing.T) {
	t.Parallel()

	counts, err := seed.Perf().Scaled(0).Counts()
	require.NoError(t, err)

	require.Equal(t, 1, counts.Batches, "the opening batch")
	require.Equal(t, 280, counts.Entries, "one opening entry per account")
}

// TestSeedProfile_Scaled_KeepsTheRoster asserts scaling changes the ledger's DEPTH and not the
// guild's size. The V5 budget is stated per 280 members; a scaled profile that also shrank the
// roster would measure a different question at every size.
func TestSeedProfile_Scaled_KeepsTheRoster(t *testing.T) {
	t.Parallel()

	full := seed.Perf()
	small := full.Scaled(4)

	require.Equal(t, full.Accounts, small.Accounts)
	require.Equal(t, full.Seed, small.Seed)
	require.Equal(t, 4, small.Raids)
	require.Equal(t, 3_400, full.Raids, "Scaled returns a copy and does not mutate its receiver")
}

// TestSeedProfile_Walk_IsDeterministic asserts two walks of the same profile plan the same dataset,
// batch for batch and entry for entry.
//
// Determinism is the whole basis for comparing one perf run against another: a measurement over a
// dataset that differed run to run would drift for reasons nobody could attribute. The walk draws
// every choice — who attended, what an item cost, how much decayed — from ledger.NewRng(p.Seed), so
// this holds by construction and this test is what keeps it holding.
func TestSeedProfile_Walk_IsDeterministic(t *testing.T) {
	t.Parallel()

	p := seed.Perf().Scaled(6)

	first, err := p.Counts()
	require.NoError(t, err)

	second, err := p.Counts()
	require.NoError(t, err)

	require.Equal(t, first, second, "two walks of the same profile plan the same dataset")

	// And a different seed plans a different one — otherwise the first assertion would hold for a
	// generator that ignored its seed entirely, which is the failure it exists to exclude.
	other := p
	other.Seed = p.Seed + 1

	differing, err := other.Counts()
	require.NoError(t, err)
	require.NotEqual(t, first.Entries, differing.Entries,
		"a different seed draws a different attendance and therefore a different entry count")
}

// TestSeedProfile_Validate_RejectsAnImpossibleProfile is the table of ways a profile cannot describe
// a dataset. Each one is rejected BEFORE anything is written, because the alternative is discovering
// it from a CHECK constraint twelve thousand batches into a seed that cannot be undone.
func TestSeedProfile_Validate_RejectsAnImpossibleProfile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		mutfn func(*seed.Profile)
		want  string
	}{
		{"no name", func(p *seed.Profile) { p.Name = "" }, "no name"},
		{"no pool", func(p *seed.Profile) { p.PoolID = "" }, "no pool id"},
		{"no accounts", func(p *seed.Profile) { p.Accounts = 0 }, "a ledger needs somebody"},
		{"negative raids", func(p *seed.Profile) { p.Raids = -1 }, "-1 raids"},
		{"empty attendance range", func(p *seed.Profile) { p.MaxAttendees = p.MinAttendees - 1 }, "attendance range"},
		{"more attendees than roster", func(p *seed.Profile) { p.MaxAttendees = p.Accounts + 1 }, "seats"},
		{"empty price range", func(p *seed.Profile) { p.MaxItemPriceCp = 0 }, "item price range"},
		{"empty decay range", func(p *seed.Profile) { p.MaxDecayCp = 0 }, "decay range"},
		{"zero tick value", func(p *seed.Profile) { p.TickValueCp = 0 }, "zero tick value"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := seed.Perf()
			tc.mutfn(&p)

			err := p.Validate()
			require.ErrorIs(t, err, seed.ErrInvalidProfile)
			require.Contains(t, err.Error(), tc.want)

			// And the walk refuses too, so an invalid profile cannot reach the database by any route.
			_, countErr := p.Counts()
			require.ErrorIs(t, countErr, seed.ErrInvalidProfile)
		})
	}
}

// TestDeterministicID_EveryTag_IsValidULID asserts every id the generator mints is a real ULID.
//
// This is the test that catches a tag containing an I, an L, an O or a U — none of which is in the
// Crockford alphabet, all of which look perfectly reasonable in a Go constant. "RAID" and "ITEM" are
// both illegal tags for exactly that reason, which is why the raid tag is "RAD" and the item tag is
// "GEAR".
func TestDeterministicID_EveryTag_IsValidULID(t *testing.T) {
	t.Parallel()

	// Every tag the package uses, named here so the test fails if one is added without being
	// checked. They are the values, not the unexported constants, because the test is an external
	// package — which is the right side of the boundary to be checking a published id format from.
	tags := []string{"ACCT", "PRSN", "CHAR", "RAD", "TCK", "GEAR", "AWRD"}

	for _, tag := range tags {
		for _, n := range []int{0, 1, 17, 18, 21, 24, 30, 31, 32, 279, 3_400, 1 << 20} {
			id := seed.DeterministicID(tag, n)

			require.Len(t, string(id), core.ULIDLength, "%s/%d has the wrong length", tag, n)
			require.True(t, id.Valid(),
				"%s/%d produced %q, which is not a valid Crockford ULID", tag, n, id)
			require.Contains(t, string(id), tag, "%s/%d should be recognisable", tag, n)
		}
	}
}

// TestDeterministicID_IsOrderPreserving asserts id order follows index order.
//
// ledger.Allocate breaks a largest-remainder tie on account_id ASC. If the encoder's order did not
// follow the roster's, the split would depend on the encoding rather than on the accounts, and a
// change to the id format would silently move a centipoint between two raiders.
func TestDeterministicID_IsOrderPreserving(t *testing.T) {
	t.Parallel()

	prev := ""

	for n := range 1_000 {
		id := string(seed.DeterministicID("ACCT", n))
		require.Greater(t, id, prev, "id for %d must sort after the id for %d", n, n-1)
		prev = id
	}
}

// TestDeterministicID_UsesOnlyTheCrockfordAlphabet is the alphabet check stated directly, so a
// failure names the offending character rather than reporting an opaque parse error.
func TestDeterministicID_UsesOnlyTheCrockfordAlphabet(t *testing.T) {
	t.Parallel()

	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	for n := range 2_000 {
		id := string(seed.DeterministicID("ACCT", n))

		for i, c := range id {
			require.True(t, strings.ContainsRune(alphabet, c),
				"%q character %d is %q, which is not Crockford base32 (I, L, O and U are excluded)",
				id, i, string(c))
		}
	}
}

// TestSeedProfile_Perf_SpansAPlausibleCalendar asserts the profile's raids land across years rather
// than in an afternoon. effective_day is a real column, ix_batch_effective is a real index, and a
// dataset whose 3,400 raids all shared one day would give both a selectivity production will never
// see.
func TestSeedProfile_Perf_SpansAPlausibleCalendar(t *testing.T) {
	t.Parallel()

	p := seed.Perf()

	span := time.Duration(p.Raids) * p.BetweenRaids
	require.Greater(t, span, 4*365*24*time.Hour, "3,400 raids should span years, not months")
	require.Less(t, span, 8*365*24*time.Hour, "and not a decade")

	first := p.FirstRaidAt.Time()
	require.True(t, first.Before(time.Date(2021, time.January, 1, 0, 0, 0, 0, time.UTC)),
		"the first raid is backdated: effective_at is game truth, and a seeded guild has a past")
	require.Equal(t, core.Micros(0), p.FirstRaidAt%1_000_000,
		"the first raid starts on a whole second")
}
