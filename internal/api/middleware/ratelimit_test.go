package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// movableClock is a clock a test drives by hand.
//
// A fake clock rather than time.Sleep: .claude/rules/go-idioms.md bans sleeping in tests because it
// is the single largest flake source in a Go service, and a limiter is exactly the component whose
// tests would otherwise be "wait a bit and hope". Every refill below is an explicit advance.
type movableClock struct{ at time.Time }

func (c *movableClock) Now() time.Time { return c.at }

func (c *movableClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// testEpoch is a fixed instant so X-RateLimit-Reset is reproducible.
func testEpoch() time.Time { return time.Unix(1_754_524_800, 0).UTC() }

// smallPolicy is four requests per four seconds: one emission per second, which makes every
// arithmetic assertion below readable without a calculator.
func smallPolicy() Policy { return Policy{Name: "small", Quota: 4, Window: 4 * time.Second} }

// TestLimiter_FreshKey_AdmitsTheQuotaThenRefuses.
//
// The count is the whole contract, and the off-by-one it guards against is the one that matters: a
// meter that admits quota+1 has an advertised limit its own headers contradict.
func TestLimiter_FreshKey_AdmitsTheQuotaThenRefuses(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := smallPolicy()

	for i := range policy.Quota {
		decision := limiter.Take(policy, "k")

		require.True(t, decision.Allowed, "request %d of the quota was refused", i+1)
		require.Equal(t, policy.Quota-i-1, decision.Remaining,
			"remaining disagrees with the number of requests actually admitted")
	}

	refused := limiter.Take(policy, "k")

	require.False(t, refused.Allowed, "the bucket admitted one request more than its quota")
	require.Zero(t, refused.Remaining)
	require.Equal(t, time.Second, refused.RetryAfter,
		"a refusal must point at the next free slot, not at the end of the window")
	require.Equal(t, policy.Window, refused.Reset)
}

// TestLimiter_Refusal_DoesNotAdvanceTheMeter.
//
// A meter that charged refused requests would push a hammering client's retry time further out with
// every attempt — a lockout a badly written bot cannot escape. Bot authors are who this API is for,
// so the refusal is free and the retry time is a fact about the bucket rather than about how rude
// the caller has been.
func TestLimiter_Refusal_DoesNotAdvanceTheMeter(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := smallPolicy()

	for range policy.Quota {
		require.True(t, limiter.Take(policy, "k").Allowed)
	}

	first := limiter.Take(policy, "k")

	for range 20 {
		require.False(t, limiter.Take(policy, "k").Allowed)
	}

	last := limiter.Take(policy, "k")

	require.Equal(t, first.RetryAfter, last.RetryAfter,
		"twenty refused requests moved the retry time; a closed bucket must not punish knocking")
}

// TestLimiter_Refill_AdmitsOnePerEmissionInterval.
//
// The bucket refills continuously rather than all at once, which is what makes Retry-After a useful
// number: a shed client told to come back in one emission finds exactly one slot waiting.
func TestLimiter_Refill_AdmitsOnePerEmissionInterval(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := smallPolicy()

	for range policy.Quota {
		require.True(t, limiter.Take(policy, "k").Allowed)
	}

	require.False(t, limiter.Take(policy, "k").Allowed)

	clk.advance(time.Second)

	require.True(t, limiter.Take(policy, "k").Allowed, "one emission did not free one slot")
	require.False(t, limiter.Take(policy, "k").Allowed, "one emission freed more than one slot")

	clk.advance(policy.Window)

	refilled := limiter.Take(policy, "k")

	require.True(t, refilled.Allowed)
	require.Equal(t, policy.Quota-1, refilled.Remaining, "a full window did not refill the bucket")
}

// TestLimiter_WindowBoundary_DoesNotAdmitTwoQuotas is the property a fixed-window counter cannot
// hold, and the reason this is a continuous meter.
//
// A counter that resets at the top of each window admits a full quota at the end of one and another
// at the start of the next, so its real ceiling is twice its advertised one for anybody who aims at
// the seam. Over two windows this meter admits two quotas' worth in total and never more.
func TestLimiter_WindowBoundary_DoesNotAdmitTwoQuotas(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := smallPolicy()

	admitted := 0

	// Drain the bucket, then step to what a fixed-window implementation would call the next window
	// and try to drain it again. Half a window has passed, so half a quota is the correct answer.
	for range policy.Quota * 2 {
		if limiter.Take(policy, "k").Allowed {
			admitted++
		}
	}

	clk.advance(policy.Window / 2)

	for range policy.Quota * 2 {
		if limiter.Take(policy, "k").Allowed {
			admitted++
		}
	}

	require.Equal(t, policy.Quota+policy.Quota/2, admitted,
		"the meter admitted a burst across the window seam, which is the fixed-window failure")
}

// TestLimiter_DistinctKeys_AreIndependent. One client exhausting its budget must not shed another's
// traffic — the failure the whole client-IP resolver exists to prevent at the key level.
func TestLimiter_DistinctKeys_AreIndependent(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := smallPolicy()

	for range policy.Quota {
		require.True(t, limiter.Take(policy, "noisy").Allowed)
	}

	require.False(t, limiter.Take(policy, "noisy").Allowed)
	require.True(t, limiter.Take(policy, "quiet").Allowed, "one key's exhaustion shed another key")
}

// TestLimiter_KeyTable_SweepsRefilledBucketsRatherThanGrowing.
//
// The table is keyed by client address, so its size is chosen by whoever is sending requests. An
// entry whose bucket has refilled carries no information — deleting it and recreating it on the next
// request produces the same decision — so the sweep is lossless and is what keeps a long-running
// instance from accumulating one entry per address it has ever seen.
func TestLimiter_KeyTable_SweepsRefilledBucketsRatherThanGrowing(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := smallPolicy()

	for i := range 100 {
		require.True(t, limiter.Take(policy, "client-"+strconv.Itoa(i)).Allowed)
	}

	require.Len(t, limiter.entries, 100)

	// Past every bucket's refill and past the sweep interval, so the next Take sweeps.
	clk.advance(sweepInterval + policy.Window)

	require.True(t, limiter.Take(policy, "client-0").Allowed)
	require.Len(t, limiter.entries, 1, "refilled buckets were not swept")
	require.False(t, limiter.Overflowed())
}

// TestLimiter_KeyTableAtCap_StopsTrackingRatherThanGrowing.
//
// The cap is a memory bound on a table an attacker can grow, and reaching it has to degrade in a
// direction that is survivable. Keys ALREADY in the table stay enforced; a new key is admitted
// untracked. The alternative — refusing unknown keys — would hand an attacker with a large address
// pool a way to lock the guild out, which is a worse outcome than the enforcement gap it closes.
//
// Overflowed makes the degradation observable, because a limiter that has quietly stopped limiting
// is the failure mode nobody notices.
func TestLimiter_KeyTableAtCap_StopsTrackingRatherThanGrowing(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 4)

	// A long window, so that the second between filling the table and overflowing it refills nothing
	// and the sweep genuinely has no full bucket to drop. With smallPolicy's one-second emission the
	// sweep would find room and the branch under test would never run.
	policy := Policy{Name: "cap", Quota: 4, Window: time.Hour}

	// Four keys, each with a bucket that is NOT refilled, so the sweep cannot make room.
	for i := range 4 {
		key := "client-" + strconv.Itoa(i)
		for range policy.Quota {
			require.True(t, limiter.Take(policy, key).Allowed)
		}
	}

	require.Len(t, limiter.entries, 4)
	require.False(t, limiter.Overflowed())

	// A fifth key: past the cap, and the sweep has nothing to drop.
	clk.advance(pressureSweepInterval)

	for range policy.Quota * 3 {
		require.True(t, limiter.Take(policy, "overflow").Allowed,
			"an untracked key was refused; the cap must degrade to unmetered, never to locked out")
	}

	require.True(t, limiter.Overflowed(), "the table hit its cap without saying so")
	require.Len(t, limiter.entries, 4, "the table grew past its cap")

	// The keys that were already tracked are still enforced.
	require.False(t, limiter.Take(policy, "client-0").Allowed,
		"an already-tracked key stopped being enforced when the table filled")
}

// TestLimiter_ConcurrentTakes_AdmitExactlyTheQuota.
//
// Every request goes through one shared Limiter, so the meter is only correct if it is correct under
// -race. A quota that leaks under concurrency is a limit that holds in tests and not in production.
func TestLimiter_ConcurrentTakes_AdmitExactlyTheQuota(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := Policy{Name: "concurrent", Quota: 50, Window: time.Minute}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)

	for range 200 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if limiter.Take(policy, "k").Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	require.Equal(t, policy.Quota, allowed,
		"200 concurrent requests against a quota of %d admitted %d", policy.Quota, allowed)
}

// TestPolicy_NonPositiveQuota_FailsClosed.
//
// The shipped policies are compile-time constants, so this branch is unreachable in the binary. It
// exists because every other missing input in this codebase refuses rather than defaulting to allow,
// and a rate limit is not the place to invert that: a policy assembled from a bad configuration value
// must not silently turn the limiter off for that surface.
func TestPolicy_NonPositiveQuota_FailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy Policy
		// admits is what the clamp leaves: a non-positive quota clamps to one request, and a
		// non-positive window clamps to one second while the quota is honoured.
		admits int
	}{
		{name: "zero quota", policy: Policy{Name: "broken", Quota: 0, Window: time.Minute}, admits: 1},
		{name: "negative quota", policy: Policy{Name: "broken", Quota: -5, Window: time.Minute}, admits: 1},
		{name: "zero window", policy: Policy{Name: "broken", Quota: 10}, admits: 10},
		{name: "negative window", policy: Policy{Name: "broken", Quota: 3, Window: -time.Minute}, admits: 3},
		{name: "wholly zero", policy: Policy{}, admits: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			clk := &movableClock{at: testEpoch()}
			limiter := NewLimiter(clk, 0)

			for i := range tc.admits {
				require.True(t, limiter.Take(tc.policy, "k").Allowed,
					"a misconfigured policy refused request %d of its clamped quota", i+1)
			}

			require.False(t, limiter.Take(tc.policy, "k").Allowed,
				"a misconfigured policy stopped limiting instead of clamping")
		})
	}
}

// TestDecision_SetHeaders_EmitsBothFamilies.
//
// docs/design/02-api-design.md §5: "Both header families are emitted on every response, not just
// 429s: the IETF RateLimit/RateLimit-Policy fields and the de-facto X-RateLimit-* triplet, because
// every existing bot library already parses the latter."
//
// The IETF spelling is the structured one the design's own examples use
// (`RateLimit: "ingest";r=19;t=52`), not the older RateLimit-Limit/Remaining/Reset triplet from
// earlier revisions of the same draft.
func TestDecision_SetHeaders_EmitsBothFamilies(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := Policy{Name: "read", Quota: 600, Window: time.Minute}

	decision := limiter.Take(policy, "k")
	header := http.Header{}

	decision.SetHeaders(policy, header.Set)

	require.Equal(t, `"read";q=600;w=60`, header.Get(HeaderRateLimitPolicy))
	require.Equal(t, `"read";r=599;t=1`, header.Get(HeaderRateLimit))

	require.Equal(t, "600", header.Get(HeaderXRateLimitLimit))
	require.Equal(t, "599", header.Get(HeaderXRateLimitRemaining))

	// ABSOLUTE, not a delta: GitHub's and Twitter's X-RateLimit-Reset are epoch seconds, and the
	// libraries that read this family expect that. The IETF field beside it carries the delta.
	require.Equal(t, strconv.FormatInt(testEpoch().Add(time.Second).Unix(), 10),
		header.Get(HeaderXRateLimitReset))
}

// TestDecision_SetHeaders_OnARefusalReportsAnExhaustedBucket. The headers on the 429 have to agree
// with the refusal itself, or a client backs off against a budget the server has already spent.
func TestDecision_SetHeaders_OnARefusalReportsAnExhaustedBucket(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := smallPolicy()

	for range policy.Quota {
		require.True(t, limiter.Take(policy, "k").Allowed)
	}

	decision := limiter.Take(policy, "k")
	header := http.Header{}

	decision.SetHeaders(policy, header.Set)

	require.False(t, decision.Allowed)
	require.Equal(t, `"small";r=0;t=4`, header.Get(HeaderRateLimit))
	require.Equal(t, "0", header.Get(HeaderXRateLimitRemaining))
	require.Equal(t, int64(1), decision.RetryAfterSeconds())
}

// TestDecision_RetryAfterSeconds_IsNeverZero.
//
// RFC 9110 §10.2.3 makes Retry-After a non-negative integer of seconds, and rounding a sub-second
// wait down to `Retry-After: 0` invites an immediate retry that is refused again — the server
// contradicting its own header, which is how a bot author concludes the headers cannot be trusted.
func TestDecision_RetryAfterSeconds_IsNeverZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		after time.Duration
		want  int64
	}{
		{name: "sub-second rounds up to one", after: 40 * time.Millisecond, want: 1},
		{name: "exactly one second", after: time.Second, want: 1},
		{name: "fractional rounds up", after: 1500 * time.Millisecond, want: 2},
		{name: "zero is still one", after: 0, want: 1},
		{name: "negative is still one", after: -time.Second, want: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, Decision{RetryAfter: tc.after}.RetryAfterSeconds())
		})
	}
}

// TestPolicy_UnevenQuota_KeepsRemainingConsistentWithAdmissions.
//
// emission is an integer division, so a quota that does not divide its window evenly truncates. The
// capacity is derived from emission rather than being the window, which is what keeps `remaining`
// agreeing with the number of requests actually admitted — an off-by-one visible only for awkward
// quotas is the hardest kind of limit bug to reproduce from a bug report.
func TestPolicy_UnevenQuota_KeepsRemainingConsistentWithAdmissions(t *testing.T) {
	t.Parallel()

	clk := &movableClock{at: testEpoch()}
	limiter := NewLimiter(clk, 0)
	policy := Policy{Name: "uneven", Quota: 7, Window: time.Minute}

	for i := range policy.Quota {
		decision := limiter.Take(policy, "k")

		require.True(t, decision.Allowed, "request %d of %d was refused", i+1, policy.Quota)
		require.Equal(t, policy.Quota-i-1, decision.Remaining)
	}

	require.False(t, limiter.Take(policy, "k").Allowed)
}
