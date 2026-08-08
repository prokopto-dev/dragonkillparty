package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
)

// TestFake_Now_ReturnsSetInstantInUTC proves the fake reads back exactly what it was set to, in UTC
// — the location normalisation that matches System.Now and keeps a test honest against a bug that
// only manifests outside UTC.
func TestFake_Now_ReturnsSetInstantInUTC(t *testing.T) {
	t.Parallel()

	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)

	instant := time.Date(2024, 7, 4, 12, 0, 0, 0, chicago)
	f := clock.NewFake(instant)

	got := f.Now()
	require.True(t, got.Equal(instant), "must represent the same instant")
	require.Equal(t, time.UTC, got.Location(), "Now must return UTC, like System.Now")
}

// TestFake_SetAndAdvance_MoveTime proves Set and Advance move the clock, including backward, which
// the real clock cannot and a "two events at the same instant" test needs.
func TestFake_SetAndAdvance_MoveTime(t *testing.T) {
	t.Parallel()

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	f := clock.NewFake(start)

	f.Advance(90 * time.Minute)
	require.True(t, f.Now().Equal(start.Add(90*time.Minute)))

	f.Advance(-90 * time.Minute)
	require.True(t, f.Now().Equal(start), "Advance with a negative duration moves backward")

	other := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	f.Set(other)
	require.True(t, f.Now().Equal(other))
}

// TestFake_Concurrent_IsRaceFree proves the fake is safe to share across goroutines — the case a
// ULID generator driven by many goroutines exercises. Run under -race, an unlocked field would flag
// here.
func TestFake_Concurrent_IsRaceFree(t *testing.T) {
	t.Parallel()

	f := clock.NewFake(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	var wg sync.WaitGroup

	wg.Add(20)
	for range 20 {
		go func() {
			defer wg.Done()
			for range 1000 {
				_ = f.Now()
				f.Advance(time.Microsecond)
			}
		}()
	}

	wg.Wait()
}
