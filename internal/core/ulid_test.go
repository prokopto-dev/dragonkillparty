package core_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// fixedInstant is an arbitrary but fixed time used to pin the millisecond in the monotonicity test.
var fixedInstant = time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC)

// TestULID_SameMillisecond_IsMonotonic is named verbatim in the acceptance criteria: over 10⁵
// generations pinned to ONE millisecond, every id must be strictly greater than the one before.
//
// The clock is frozen with a Fake so every call reads the same millisecond — which is precisely the
// case that would otherwise sort by the random tail and lose creation order. oklog's monotonic
// entropy increments rather than redraws, and this test is the proof that the wiring uses it.
func TestULID_SameMillisecond_IsMonotonic(t *testing.T) {
	t.Parallel()

	const n = 100_000

	g := core.NewGenerator(clock.NewFake(fixedInstant))

	prev, err := g.New()
	require.NoError(t, err)
	require.True(t, prev.Valid())

	for i := 1; i < n; i++ {
		cur, err := g.New()
		require.NoError(t, err)

		require.Less(t, string(prev), string(cur),
			"ulid %d was not strictly greater than its predecessor within one millisecond", i)

		prev = cur
	}
}

// TestULID_Generate_Is26CharCrockfordBase32 asserts the shape from canonical §3: 26 characters,
// Crockford base32, valid under strict parsing.
func TestULID_Generate_Is26CharCrockfordBase32(t *testing.T) {
	t.Parallel()

	g := core.NewGenerator(clock.NewFake(fixedInstant))

	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	for range 1000 {
		id, err := g.New()
		require.NoError(t, err)

		require.Len(t, string(id), core.ULIDLength)
		require.Equal(t, 26, core.ULIDLength, "canonical §3 fixes the length at 26")
		require.True(t, id.Valid(), "a freshly minted id must be valid: %q", id)

		for _, r := range string(id) {
			require.Contains(t, crockford, string(r),
				"id %q contains %q, which is not in the Crockford base32 alphabet", id, string(r))
		}
	}
}

// TestULID_EmbeddedTime_MatchesTheClock proves the generator stamps the injected clock's instant
// into the id, so ULID.Time() recovers it — the property that makes lexical order time order.
func TestULID_EmbeddedTime_MatchesTheClock(t *testing.T) {
	t.Parallel()

	g := core.NewGenerator(clock.NewFake(fixedInstant))

	id, err := g.New()
	require.NoError(t, err)

	got, ok := id.Time()
	require.True(t, ok)

	// ULID timestamps have millisecond resolution, so compare at the millisecond.
	wantMs := fixedInstant.UnixMilli()
	gotMs := got.Time().UnixMilli()
	require.Equal(t, wantMs, gotMs)
}

// TestULID_AdvancingClock_ProducesOrderedIDs proves the time-order property across milliseconds:
// ids minted at increasing instants sort in creation order even without the monotonic entropy,
// because the timestamp prefix dominates.
func TestULID_AdvancingClock_ProducesOrderedIDs(t *testing.T) {
	t.Parallel()

	fake := clock.NewFake(fixedInstant)
	g := core.NewGenerator(fake)

	prev, err := g.New()
	require.NoError(t, err)

	for range 1000 {
		fake.Advance(2 * time.Millisecond)

		cur, err := g.New()
		require.NoError(t, err)

		require.Less(t, string(prev), string(cur))
		prev = cur
	}
}

// TestULID_Valid_RejectsMalformed proves the validator is strict about length and alphabet.
func TestULID_Valid_RejectsMalformed(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":        "",
		"too short":    "01ARZ3NDEKTSV4RRFFQ69G5FA",   // 25 chars
		"too long":     "01ARZ3NDEKTSV4RRFFQ69G5FAVX", // 27 chars
		"bad alphabet": "01ARZ3NDEKTSV4RRFFQ69G5FAI",  // 'I' is not in Crockford base32
	}

	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.False(t, core.ULID(s).Valid(), "%q must be invalid", s)
		})
	}

	require.True(t, core.ULID("01ARZ3NDEKTSV4RRFFQ69G5FAV").Valid(), "a canonical example must be valid")
}

// TestULID_Concurrent_NoDuplicates proves the mutex actually serialises the shared monotonic
// entropy: many goroutines minting at once, all in the same millisecond, must still produce
// distinct, valid ids. Without the lock, MonotonicRead's unlocked counter races and yields
// duplicates — which -race would flag and this set-size assertion would catch even without it.
func TestULID_Concurrent_NoDuplicates(t *testing.T) {
	t.Parallel()

	g := core.NewGenerator(clock.NewFake(fixedInstant))

	const goroutines = 16
	const perGoroutine = 2000

	var (
		mu   sync.Mutex
		seen = make(map[string]struct{}, goroutines*perGoroutine)
		wg   sync.WaitGroup
	)

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()

			local := make([]string, 0, perGoroutine)
			for range perGoroutine {
				id, err := g.New()
				require.NoError(t, err)
				local = append(local, string(id))
			}

			mu.Lock()
			for _, s := range local {
				seen[s] = struct{}{}
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	require.Len(t, seen, goroutines*perGoroutine, "every generated id must be unique")
}
