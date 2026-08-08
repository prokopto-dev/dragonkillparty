package guild_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/guild"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
	"github.com/prokopto-dev/dragonkillparty/internal/store/sqlitegen"
)

// These tests exercise the guild service against a real SQLite database — there is no fake Queries
// implementation and a lint rule forbids adding one (.claude/rules/go-idioms.md). TestMain (main_test.go)
// builds the template once; each test clones it through store.NewDB.

// fixedClock is a clock.Clock frozen at a chosen instant. internal/clock has no Fixed helper until
// Phase 0 PR 8 adds clock/fake.go, so the tests that need a deterministic updated_at define their own
// — a two-line struct is cheaper than a dependency that does not exist yet.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var _ clock.Clock = fixedClock{}

// newService opens a fresh database, seeds the singleton guild row, and returns a Service backed by
// it. The migration creates the guild table but not the row — production seeds it in Phase 2's setup
// flow — so the tests insert it through the store's typed InsertGuild path.
func newService(t *testing.T, clk clock.Clock) *guild.Service {
	t.Helper()

	s := store.NewDB(t)

	err := s.Tx(t.Context(), func(ctx context.Context, q store.Queries) error {
		_, insErr := q.InsertGuild(ctx, sqlitegen.InsertGuildParams{
			Name:            "Kittens Who Say Ni",
			Tag:             "KWSN",
			Timezone:        "America/New_York",
			WeekStart:       1,
			PointsLabel:     "DKP",
			PointsPrecision: 2,
			AutoSetInactive: 0,
			HideInactive:    0,
			CreatedAt:       1_000,
			UpdatedAt:       1_000,
		})

		return insErr
	})
	require.NoError(t, err, "seed the guild row")

	return guild.NewService(s, clk)
}

// TestGet_Singleton_ReturnsTheRow reads the seeded guild and checks the fields round-trip, including
// the nullable inactive_after_days as a nil pointer.
func TestGet_Singleton_ReturnsTheRow(t *testing.T) {
	t.Parallel()

	svc := newService(t, fixedClock{})

	g, err := svc.Get(t.Context())
	require.NoError(t, err)

	require.Equal(t, int64(1), g.ID)
	require.Equal(t, "Kittens Who Say Ni", g.Name)
	require.Equal(t, "KWSN", g.Tag)
	require.Equal(t, "America/New_York", g.Timezone)
	require.Nil(t, g.InactiveAfterDays, "an unset inactive_after_days must read back as a nil pointer")
	require.False(t, g.AutoSetInactive)
}

// TestGet_NoRow_ReturnsNotFound covers the never-seeded database: a migrated table with no guild row
// is ErrNotFound, which the handler renders as a 404, not a 500.
func TestGet_NoRow_ReturnsNotFound(t *testing.T) {
	t.Parallel()

	s := store.NewDB(t)
	svc := guild.NewService(s, fixedClock{})

	_, err := svc.Get(t.Context())
	require.ErrorIs(t, err, guild.ErrNotFound)
}

// TestGetAndUpdate_NoStore_ReturnErrNoStore covers the degraded boot state: cmd/dkp keeps the process
// up so /healthz answers even when the database could not be opened, and passes a nil store. Both
// operations must return ErrNoStore (which the handler maps to 503) rather than dereferencing nil.
func TestGetAndUpdate_NoStore_ReturnErrNoStore(t *testing.T) {
	t.Parallel()

	svc := guild.NewService(nil, fixedClock{})

	_, getErr := svc.Get(t.Context())
	require.ErrorIs(t, getErr, guild.ErrNoStore)

	_, updateErr := svc.Update(t.Context(), guild.UpdateInput{IfMatch: `"x"`})
	require.ErrorIs(t, updateErr, guild.ErrNoStore)
}

// TestUpdate_CurrentIfMatch_AppliesPatchAndChangesETag is the positive control: a PATCH with the
// current ETag succeeds, changes only the named field, and yields a DIFFERENT ETag.
func TestUpdate_CurrentIfMatch_AppliesPatchAndChangesETag(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	svc := newService(t, fixedClock{t: stamp})

	before, err := svc.Get(t.Context())
	require.NoError(t, err)

	newName := "New Name"
	after, err := svc.Update(t.Context(), guild.UpdateInput{
		IfMatch: guild.ETagOf(before),
		Name:    &newName,
	})
	require.NoError(t, err)

	require.Equal(t, "New Name", after.Name, "the patched field must change")
	require.Equal(t, before.Tag, after.Tag, "an unpatched field must be preserved")
	require.Equal(t, stamp.UnixMicro(), after.UpdatedAt, "updated_at must be stamped from the clock")
	require.NotEqual(t, guild.ETagOf(before), guild.ETagOf(after),
		"a successful PATCH must yield a different ETag, or If-Match cannot detect the next race")
}

// TestUpdate_StaleIfMatch_ReturnsPreconditionFailedWithCurrent asserts a stale If-Match is rejected,
// nothing is written, and the returned Guild is the CURRENT representation so the handler can put it
// in meta.current.
func TestUpdate_StaleIfMatch_ReturnsPreconditionFailedWithCurrent(t *testing.T) {
	t.Parallel()

	svc := newService(t, fixedClock{t: time.Unix(0, 0).UTC()})

	before, err := svc.Get(t.Context())
	require.NoError(t, err)

	newName := "Should Not Persist"
	cur, err := svc.Update(t.Context(), guild.UpdateInput{
		IfMatch: `"stale-etag"`,
		Name:    &newName,
	})
	require.ErrorIs(t, err, guild.ErrPreconditionFailed)
	require.Equal(t, before, cur, "a stale If-Match must return the current representation unchanged")

	reread, err := svc.Get(t.Context())
	require.NoError(t, err)
	require.Equal(t, before.Name, reread.Name, "a rejected PATCH must not have written anything")
}

// TestUpdate_SetsAndClearsInactiveAfterDays proves the three-state nullable field: setting it to a
// value writes that value, and setting it to a pointer-to-nil writes NULL (turns the sweep off).
func TestUpdate_SetsAndClearsInactiveAfterDays(t *testing.T) {
	t.Parallel()

	svc := newService(t, fixedClock{t: time.Unix(1, 0).UTC()})

	before, err := svc.Get(t.Context())
	require.NoError(t, err)
	require.Nil(t, before.InactiveAfterDays)

	thirty := int64(30)
	set := &thirty
	afterSet, err := svc.Update(t.Context(), guild.UpdateInput{
		IfMatch:           guild.ETagOf(before),
		InactiveAfterDays: &set,
	})
	require.NoError(t, err)
	require.NotNil(t, afterSet.InactiveAfterDays)
	require.Equal(t, int64(30), *afterSet.InactiveAfterDays)

	var cleared *int64
	afterClear, err := svc.Update(t.Context(), guild.UpdateInput{
		IfMatch:           guild.ETagOf(afterSet),
		InactiveAfterDays: &cleared,
	})
	require.NoError(t, err)
	require.Nil(t, afterClear.InactiveAfterDays, "setting the field to a nil pointer must write NULL")
}

// TestUpdate_AbsentInactiveAfterDays_LeavesItUnchanged proves the other half: an absent field
// (nil double pointer) must not touch inactive_after_days.
func TestUpdate_AbsentInactiveAfterDays_LeavesItUnchanged(t *testing.T) {
	t.Parallel()

	svc := newService(t, fixedClock{t: time.Unix(2, 0).UTC()})

	before, err := svc.Get(t.Context())
	require.NoError(t, err)

	fifteen := int64(15)
	set := &fifteen
	seeded, err := svc.Update(t.Context(), guild.UpdateInput{
		IfMatch:           guild.ETagOf(before),
		InactiveAfterDays: &set,
	})
	require.NoError(t, err)

	name := "Renamed"
	after, err := svc.Update(t.Context(), guild.UpdateInput{
		IfMatch: guild.ETagOf(seeded),
		Name:    &name,
	})
	require.NoError(t, err)
	require.NotNil(t, after.InactiveAfterDays)
	require.Equal(t, int64(15), *after.InactiveAfterDays,
		"an absent InactiveAfterDays must leave the stored value alone")
}

// TestETagOf_IsDeterministicAndFieldSensitive asserts the strong validator's two required
// properties: the same representation always hashes the same, and any field change changes it.
func TestETagOf_IsDeterministicAndFieldSensitive(t *testing.T) {
	t.Parallel()

	base := guild.Guild{
		ID: 1, Name: "A", Tag: "T", Timezone: "UTC", WeekStart: 1,
		PointsLabel: "DKP", PointsPrecision: 2, AutoSetInactive: false, HideInactive: false,
		CreatedAt: 100, UpdatedAt: 100,
	}

	require.Equal(t, guild.ETagOf(base), guild.ETagOf(base), "the ETag must be deterministic")

	changed := base
	changed.Name = "B"
	require.NotEqual(t, guild.ETagOf(base), guild.ETagOf(changed), "a name change must change the ETag")

	changedTime := base
	changedTime.UpdatedAt = 101
	require.NotEqual(t, guild.ETagOf(base), guild.ETagOf(changedTime),
		"an updated_at change must change the ETag")

	// Length-prefixing: Name "ab"+Tag "c" must not collide with Name "a"+Tag "bc".
	left := base
	left.Name, left.Tag = "ab", "c"
	right := base
	right.Name, right.Tag = "a", "bc"
	require.NotEqual(t, guild.ETagOf(left), guild.ETagOf(right),
		"field boundaries must be encoded, or two representations could share an ETag")
}

// compile-time reminder that the generated model keeps the nullable shape as *int64; if sqlc ever
// regressed inactive_after_days to interface{}, this reference would stop compiling.
var _ = sqlitegen.Guild{InactiveAfterDays: (*int64)(nil)}
