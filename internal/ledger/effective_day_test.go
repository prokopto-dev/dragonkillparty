package ledger_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/ledger"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// effective_day is bucketed in the guild's timezone, not UTC. Phase 1, #203.
//
// THE BUG THIS CLOSES. `ledger_batch.effective_day` was written as the UTC calendar day, while
// db/schema.hcl says the column is "'YYYY-MM-DD' guild-local, computed in Go" and guild.timezone is a
// real column rather than a settings_json key precisely because it "buckets every *_day column". A
// raid starting at 19:00 America/Los_Angeles was recorded on the NEXT day: every guild west of UTC had
// its raid nights split across two buckets and every guild east of it had some mornings folded
// backwards. The column exists to be grouped by, so anything that groups it was wrong by one bucket
// for part of every day.
//
// THE ROWS ARE NOT FIXABLE AFTERWARDS, which is why this is Phase 1 rather than later. ledger_batch is
// append-only and a trigger enforces it, so a repair is not an UPDATE — it is a migration-time rebuild
// of a table that carries the append-only triggers, or living with a column that means "UTC day" for
// rows before the fix and "guild day" after, with nothing in the row saying which.

// laRaidNight is 2024-06-02T02:00:00Z — 19:00 on 2024-06-01 in America/Los_Angeles, which is a
// perfectly ordinary P99 raid start time and lands on a different date in the two zones. The whole
// point of the instant is that UTC and the guild disagree about which day it is.
var laRaidNight = time.Date(2024, 6, 2, 2, 0, 0, 0, time.UTC)

// setGuild writes the singleton guild row with the given IANA zone.
//
// Raw SQL through store.ExecForTest, like every other seed in this package: SQL001/SQL002 allow a raw
// Exec only under internal/store, so the call lives there and this package stays free of raw SQL.
func setGuild(tb testing.TB, s *store.Store, timezone string) {
	tb.Helper()

	s.ExecForTest(tb,
		`INSERT INTO guild
		   (id, name, tag, timezone, week_start, points_label, points_precision,
		    inactive_after_days, auto_set_inactive, hide_inactive, created_at, updated_at)
		 VALUES (1, 'Wandering Gnomes', 'WG', ?, 1, 'DKP', 2, NULL, 0, 0, 1704067200000000, 1704067200000000)`,
		timezone)
}

// commitAt commits one minimal batch effective at the given instant and returns its effective_day.
func commitAt(tb testing.TB, svc *ledger.Service, s *store.Store, at time.Time) string {
	tb.Helper()

	accounts := seedPersonAccounts(tb, s, 1)

	p := award(ledger.AccountIDGuildBank, []ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})
	p.EffectiveAt = core.FromTime(at)

	_, err := svc.Commit(tb.Context(), request(p))
	require.NoError(tb, err)

	return textValue(tb, s, `SELECT effective_day FROM ledger_batch WHERE seq = 1`)
}

// TestCommit_EffectiveDay_IsBucketedInTheGuildTimezone is #203, in one table.
//
// The two interesting rows are the ones either side of UTC: a guild west of it must not have its
// Saturday-night raid recorded on Sunday, and a guild east of it must not have its Sunday morning
// recorded on Saturday. The UTC row is the control — the same instant, the same code path, and the
// day the old implementation would have written for all three.
func TestCommit_EffectiveDay_IsBucketedInTheGuildTimezone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		timezone string
		want     string
		why      string
	}{
		{
			name:     "a guild west of UTC keeps its raid night on the night it raided",
			timezone: "America/Los_Angeles",
			want:     "2024-06-01",
			why:      "19:00 on the 1st, local: the raid that starts a P99 Saturday night",
		},
		{
			name:     "a guild east of UTC does not have its morning folded backwards",
			timezone: "Australia/Sydney",
			want:     "2024-06-02",
			why:      "12:00 on the 2nd, local",
		},
		{
			name:     "a guild in UTC is unchanged",
			timezone: "UTC",
			want:     "2024-06-02",
			why: "the control: UTC and the guild agree, and this is what the column used to say " +
				"for all three rows",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, s := newService(t)
			setGuild(t, s, tc.timezone)

			require.Equal(t, tc.want, commitAt(t, svc, s, laRaidNight), tc.why)
		})
	}
}

// TestCommit_EffectiveDay_WithNoGuildRow_FallsBackToUTC.
//
// The setup flow that writes the guild row is Phase 2, so "no row" is the ordinary case today: every
// test in this package, every seeded dataset, and every install between `dkp migrate` and first setup.
// A commit must not fail for it — refusing a raid tick because the guild has not been named yet would
// be a far worse bug than a day bucket in the column's own documented default zone.
func TestCommit_EffectiveDay_WithNoGuildRow_FallsBackToUTC(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)

	require.Equal(t, "2024-06-02", commitAt(t, svc, s, laRaidNight),
		"UTC is the fallback, and 'UTC' is what db/schema.hcl calls the safe default: the one zone "+
			"that is always valid")
}

// TestCommit_EffectiveDay_WithAnUnloadableZone_FallsBackToUTC.
//
// Nothing validates guild.timezone on write today (#216), so a hand-edited database or a typo'd
// settings PATCH can put anything in the column — and a fork that built its own image without the
// tzdata database would fail to load a perfectly valid zone name. Neither may stop a commit: the
// officer's day buckets are silently UTC until it is corrected, which is what the WARN line in
// guildZone exists to make findable.
func TestCommit_EffectiveDay_WithAnUnloadableZone_FallsBackToUTC(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	setGuild(t, s, "Middle_Earth/Rivendell")

	require.Equal(t, "2024-06-02", commitAt(t, svc, s, laRaidNight))
}

// TestCommit_EffectiveDay_FollowsAZoneChangedWithoutARestart is why the zone is read inside the
// transaction rather than resolved once at boot.
//
// An officer fixing the guild's timezone in the settings form expects tonight's raid to land in the
// right bucket. A zone captured at construction would be stale until somebody restarted the process,
// and "the day bucket is wrong until a reboot" is precisely the class of silent wrongness this issue
// is about. The cost of the alternative is one indexed single-row read per commit.
func TestCommit_EffectiveDay_FollowsAZoneChangedWithoutARestart(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	setGuild(t, s, "UTC")

	accounts := seedPersonAccounts(t, s, 1)

	commit := func(seq int64, key string) string {
		p := award(ledger.AccountIDGuildBank, []ledger.Allocation{{AccountID: accounts[0], AmountCp: 100}})
		p.EffectiveAt = core.FromTime(laRaidNight)

		req := request(p)
		req.IdempotencyKey = &key

		_, err := svc.Commit(t.Context(), req)
		require.NoError(t, err)

		return textValue(t, s, `SELECT effective_day FROM ledger_batch WHERE seq = ?`, seq)
	}

	require.Equal(t, "2024-06-02", commit(1, "before"))

	// The same service, the same clock, no restart — only the row an officer just edited.
	s.ExecForTest(t, `UPDATE guild SET timezone = 'America/Los_Angeles' WHERE id = 1`)

	require.Equal(t, "2024-06-01", commit(2, "after"),
		"the next batch buckets in the zone that is now in force, not the one that was when the "+
			"process started")
}

// TestCommit_EffectiveDay_IsNotTheRecordedDay guards the other half of the pair.
//
// effective_at is GAME truth and may be backdated; recorded_at is SYSTEM truth and never is. A tick
// credited the morning after the raid belongs to the raid's day, and a bucketing change is exactly the
// kind of edit that could quietly start using the wrong one of the two — at which point every
// backdated correction lands in today's bucket and the attendance history silently shifts.
func TestCommit_EffectiveDay_IsNotTheRecordedDay(t *testing.T) {
	t.Parallel()

	svc, s := newService(t)
	setGuild(t, s, "UTC")

	// The clock says 2024-06-01T12:00:00Z (fixedNow); the raid was three days earlier.
	backdated := time.Date(2024, 5, 29, 20, 0, 0, 0, time.UTC)

	require.Equal(t, "2024-05-29", commitAt(t, svc, s, backdated),
		"effective_day buckets GAME truth. recorded_at is the system's and is what the clock decides")

	require.Equal(t, core.FromTime(clock.NewFake(fixedNow).Now()).String(),
		core.Micros(countRow(t, s, `SELECT recorded_at FROM ledger_batch WHERE seq = 1`)).String(),
		"and recorded_at is still the injected clock's instant, unaffected by the backdating")
}
