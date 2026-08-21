package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/store"
)

// TestSessionCookieName_IsTheHostPrefixedName pins canonical §7's exact string.
//
// THE NAME IS THE CONTROL (§3.6). A browser refuses a `__Host-` cookie that carries a Domain, or a
// Path other than `/`, or arrives without Secure — so the prefix pins the cookie to the exact origin
// and blocks subdomain injection, which matters because self-hosters park several apps under one
// domain. Renaming it silently removes a control that no other line of code implements, and the same
// string is published in the OpenAPI `securitySchemes` block, so it is wire contract as well.
func TestSessionCookieName_IsTheHostPrefixedName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "__Host-dkp_session", auth.SessionCookieName)
}

// TestSessionLifetimes_MatchTheSpecifiedWindows. Idle 14 days, absolute 30 (§3.6). The absolute
// ceiling is what bounds a stolen cookie, so a change to either is a security decision.
func TestSessionLifetimes_MatchTheSpecifiedWindows(t *testing.T) {
	t.Parallel()

	require.Equal(t, 14*24*time.Hour, auth.SessionIdleWindow)
	require.Equal(t, 30*24*time.Hour, auth.SessionAbsoluteWindow)
	require.Greater(t, auth.SessionAbsoluteWindow, auth.SessionIdleWindow,
		"an absolute ceiling below the idle window would make the idle slide meaningless")
}

// TestResolve_TouchIsThrottled is the statement-count assertion behind internal/auth's touchInterval,
// and it is a performance control with a correctness consequence.
//
// SQLite has ONE writer here by construction, and it is the connection raid-night awards queue on. A
// resolver that stamped last_seen_at on every request would put a bot's poll loop — 3600 writes an
// hour, per credential — in front of them, for a column whose question is "was this used, and roughly
// when". So the write happens at most once a minute, and this test walks all three cases in order:
// current (no write), stale (one write), current again (no write).
//
// The counter is the right instrument rather than reading the row back: what is being asserted is
// that no STATEMENT was issued, and a row-read assertion would pass just as happily against a write
// that had been issued and lost a race.
func TestResolve_TouchIsThrottled(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	counter := store.Counted(t)

	counter.Reset()
	_, err := svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.NoError(t, err)
	require.Equal(t, 1, counter.Count(),
		"a freshly stamped session must cost one statement: the lookup, and no write")

	env.clock.Advance(2 * time.Minute)

	counter.Reset()
	_, err = svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.NoError(t, err)
	require.Equal(t, 2, counter.Count(),
		"once the stamp is older than the throttle, the lookup is followed by one write")

	counter.Reset()
	_, err = svc.ResolveRequest(t.Context(), request(env.cookie, ""))
	require.NoError(t, err)
	require.Equal(t, 1, counter.Count(),
		"the write just refreshed the stamp, so the next request must not write again")
}

// TestResolve_TokenTouchIsThrottled is the same control on the bearer path, which is the one a bot's
// poll loop actually hits.
func TestResolve_TokenTouchIsThrottled(t *testing.T) {
	t.Parallel()

	svc, env := newResolver(t)

	counter := store.Counted(t)

	counter.Reset()
	_, err := svc.ResolveRequest(t.Context(), request(nil, env.token))
	require.NoError(t, err)
	require.Equal(t, 2, counter.Count(),
		"a token that has never been used has a NULL last_used_at, so the first request stamps it")

	counter.Reset()
	_, err = svc.ResolveRequest(t.Context(), request(nil, env.token))
	require.NoError(t, err)
	require.Equal(t, 1, counter.Count(), "the second request inside the throttle window must not write")

	env.clock.Advance(2 * time.Minute)

	counter.Reset()
	_, err = svc.ResolveRequest(t.Context(), request(nil, env.token))
	require.NoError(t, err)
	require.Equal(t, 2, counter.Count(), "past the throttle, it stamps again")
}
