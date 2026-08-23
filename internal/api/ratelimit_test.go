package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/api"
	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
	"github.com/prokopto-dev/dragonkillparty/internal/clock"
)

// frozenClock stops time for the limiter under test.
//
// IT IS LOAD-BEARING, not tidiness. The meter refills continuously: the write bucket emits one token
// every 500 ms, so a test that drains 120 requests over a second of real wall clock gets two of them
// back and the request that must be refused is admitted instead. That is a failure that depends on
// how loaded the machine is — green on a laptop, red on a busy CI runner, and blamed on the limiter
// rather than on the test. Freezing the clock makes every assertion below a statement about the
// arithmetic.
type frozenClock struct{ at time.Time }

func (c frozenClock) Now() time.Time { return c.at }

var _ clock.Clock = frozenClock{}

// stopped is the instant every server in this file is built at.
func stopped() clock.Clock { return frozenClock{at: time.Unix(1_754_524_800, 0).UTC()} }

// The write bucket is 120/min and the read bucket is 600/min (docs/design/03-security.md §10), so
// every exhaustion test below drives the write bucket: it proves the same arithmetic in a fifth of
// the requests, and internal/api is one of the packages whose tests always re-run.
const writeQuota = 120

// trustLocalProxy is the trust list a test uses when it wants to control the client address, which is
// the only way to do so through a real listener: httptest binds loopback, so every request arrives
// from 127.0.0.1 and only a trusted-proxy configuration makes X-Forwarded-For readable.
func trustLocalProxy(t *testing.T) middleware.TrustedProxies {
	t.Helper()

	trusted, err := middleware.ParseTrustedProxies("127.0.0.1/32,::1/128")
	require.NoError(t, err)

	return trusted
}

// send performs one request against srv, optionally claiming a forwarded client address.
func send(t *testing.T, srv *httptest.Server, method, path, forwardedFor string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
	require.NoError(t, err)

	if forwardedFor != "" {
		req.Header.Set(middleware.HeaderForwardedFor, forwardedFor)
	}

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	// Drained and closed immediately: 120 undrained bodies is 120 connections the client cannot
	// reuse, which turns an in-process test into a file-descriptor test.
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	return res
}

// drainWriteBucket sends exactly the write quota and asserts none of it was shed.
func drainWriteBucket(t *testing.T, srv *httptest.Server, forwardedFor string) {
	t.Helper()

	for i := range writeQuota {
		res := send(t, srv, http.MethodPatch, "/api/v1/guild", forwardedFor)

		require.NotEqual(t, http.StatusTooManyRequests, res.StatusCode,
			"request %d of a quota of %d was shed", i+1, writeQuota)
	}
}

// TestRateLimit_EveryResponse_CarriesBothHeaderFamilies.
//
// docs/design/02-api-design.md §5 is explicit that this is not a 429-only courtesy: "Both header
// families are emitted on every response, not just 429s … because every existing bot library already
// parses the latter". A client that first learns its budget from the response that already refused it
// cannot use the headers for what they exist for.
func TestRateLimit_EveryResponse_CarriesBothHeaderFamilies(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(api.New(api.Config{Clock: stopped()}))
	t.Cleanup(srv.Close)

	res := send(t, srv, http.MethodGet, "/api/v1/meta", "")

	require.Equal(t, http.StatusOK, res.StatusCode)

	require.Equal(t, `"read";q=600;w=60`, res.Header.Get(middleware.HeaderRateLimitPolicy))
	require.Equal(t, `"read";r=599;t=1`, res.Header.Get(middleware.HeaderRateLimit))

	require.Equal(t, "600", res.Header.Get(middleware.HeaderXRateLimitLimit))
	require.Equal(t, "599", res.Header.Get(middleware.HeaderXRateLimitRemaining))
	// Pinned exactly, because the clock is stopped: the de-facto triplet's Reset is a UNIX TIMESTAMP
	// rather than a delta — GitHub's and Twitter's are, and the libraries that read this family
	// expect it — while the IETF field beside it carries the delta. A bot that read one as the other
	// would sleep for fifty-five years.
	require.Equal(t, strconv.FormatInt(stopped().Now().Add(time.Second).Unix(), 10),
		res.Header.Get(middleware.HeaderXRateLimitReset))

	require.Empty(t, res.Header.Get(middleware.HeaderRetryAfter),
		"Retry-After belongs on a refusal; on a served response it tells a client to back off for no reason")
}

// TestRateLimit_QuotaExhausted_Answers429FromTheClosedEnum.
//
// The 429 is the published `rate_limited` row of docs/api/errors.md: an RFC 9457 body carrying the
// machine code a bot discriminates on, a Retry-After a bot obeys, and the request id the support
// workflow runs on. Never a 200 with an error body, and never a bare text/plain 429 — a bot's error
// parser can read neither.
func TestRateLimit_QuotaExhausted_Answers429FromTheClosedEnum(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(api.New(api.Config{Clock: stopped()}))
	t.Cleanup(srv.Close)

	drainWriteBucket(t, srv, "")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, srv.URL+"/api/v1/guild", nil)
	require.NoError(t, err)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	require.Equal(t, http.StatusTooManyRequests, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "application/problem+json")

	retryAfter, err := strconv.Atoi(res.Header.Get(middleware.HeaderRetryAfter))
	require.NoError(t, err, "Retry-After must be delta-seconds, never a date: half the bots in this "+
		"ecosystem run on a box with no real-time clock")
	require.GreaterOrEqual(t, retryAfter, 1, "Retry-After: 0 invites an immediate retry that is refused again")

	require.Equal(t, `"write";r=0;t=60`, res.Header.Get(middleware.HeaderRateLimit))
	require.Equal(t, "0", res.Header.Get(middleware.HeaderXRateLimitRemaining))

	var problem struct {
		Type      string `json:"type"`
		Status    int    `json:"status"`
		Code      string `json:"code"`
		Detail    string `json:"detail"`
		RequestID string `json:"request_id"`
	}

	require.NoError(t, json.Unmarshal(body, &problem))

	require.Equal(t, string(api.CodeRateLimited), problem.Code)
	require.Equal(t, http.StatusTooManyRequests, problem.Status)
	require.NotEmpty(t, problem.Detail)
	require.NotEmpty(t, problem.RequestID,
		"a problem body with no request_id is a support request nobody can answer")
	require.Contains(t, problem.Type, "rate_limited")
}

// TestRateLimit_ReadsAndWrites_AreSeparateBuckets.
//
// §10's table gives PAT reads and PAT writes their own rows and says "separate bucket" in as many
// words. The reason is operational: a bot that hammers a write endpoint during an anti-snipe must not
// take the standings page down for everybody reading it.
func TestRateLimit_ReadsAndWrites_AreSeparateBuckets(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(api.New(api.Config{Clock: stopped()}))
	t.Cleanup(srv.Close)

	drainWriteBucket(t, srv, "")

	require.Equal(t, http.StatusTooManyRequests,
		send(t, srv, http.MethodPatch, "/api/v1/guild", "").StatusCode)

	read := send(t, srv, http.MethodGet, "/api/v1/meta", "")

	require.Equal(t, http.StatusOK, read.StatusCode,
		"an exhausted write bucket shed a read; the two buckets are not separate")
	require.Equal(t, "599", read.Header.Get(middleware.HeaderXRateLimitRemaining))
}

// TestRateLimit_TrustedProxy_GivesEachForwardedClientItsOwnBucket.
//
// This is the whole point of folding issue #98 into the limiter: behind a proxy, the socket's peer
// address is the proxy's, so a limiter keyed on it limits the guild rather than the caller. With
// DKP_TRUSTED_PROXIES naming the proxy, the resolved client address is the key and one noisy member
// cannot shed anybody else's raid night.
func TestRateLimit_TrustedProxy_GivesEachForwardedClientItsOwnBucket(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(api.New(api.Config{Clock: stopped(), TrustedProxies: trustLocalProxy(t)}))
	t.Cleanup(srv.Close)

	drainWriteBucket(t, srv, "203.0.113.1")

	require.Equal(t, http.StatusTooManyRequests,
		send(t, srv, http.MethodPatch, "/api/v1/guild", "203.0.113.1").StatusCode)

	require.NotEqual(t, http.StatusTooManyRequests,
		send(t, srv, http.MethodPatch, "/api/v1/guild", "203.0.113.2").StatusCode,
		"a second client behind the same trusted proxy was shed by the first client's budget")
}

// TestRateLimit_UntrustedPeer_KeysOnTheSocketAddressNotTheHeader is the mechanism
// docs/design/03-security.md §8.3 names for itself: "an integration test sends
// X-Forwarded-For: 1.2.3.4 from an untrusted peer and asserts … the rate-limit key uses the real
// RemoteAddr".
//
// It is the single most important test in this file. If it fails, the per-IP limit is not a limit at
// all: an attacker gets a fresh budget per forged header, which is unlimited budget, and every other
// test here passes while it happens.
func TestRateLimit_UntrustedPeer_KeysOnTheSocketAddressNotTheHeader(t *testing.T) {
	t.Parallel()

	// No TrustedProxies: the default, and the configuration every instance has until an operator
	// deliberately names their proxy.
	srv := httptest.NewServer(api.New(api.Config{Clock: stopped()}))
	t.Cleanup(srv.Close)

	// Every request claims a DIFFERENT client address. All of them are ignored, so all of them charge
	// the same bucket — the loopback peer that actually opened the socket.
	for i := range writeQuota {
		res := send(t, srv, http.MethodPatch, "/api/v1/guild", "203.0.113."+strconv.Itoa(i%250+1))

		require.NotEqual(t, http.StatusTooManyRequests, res.StatusCode,
			"request %d was shed before the quota", i+1)
	}

	res := send(t, srv, http.MethodPatch, "/api/v1/guild", "198.51.100.77")

	require.Equal(t, http.StatusTooManyRequests, res.StatusCode,
		"a forged X-Forwarded-For bought a fresh rate-limit budget; the per-IP limit is not a limit")
}

// TestRateLimit_Healthz_IsNeverShed.
//
// Canonical conventions §13: /healthz must answer 200 whatever else is wrong, because Docker's
// HEALTHCHECK calls it and a container killed here is a container killed mid-migration. A 429 on the
// liveness endpoint would let a flood — or a busy raid night — restart the instance.
//
// It carries no rate-limit headers either, which is the same fact stated on the wire: the endpoint is
// outside the API surface the limiter meters.
func TestRateLimit_Healthz_IsNeverShed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(api.New(api.Config{Clock: stopped()}))
	t.Cleanup(srv.Close)

	drainWriteBucket(t, srv, "")

	require.Equal(t, http.StatusTooManyRequests,
		send(t, srv, http.MethodPatch, "/api/v1/guild", "").StatusCode)

	health := send(t, srv, http.MethodGet, "/healthz", "")

	require.Equal(t, http.StatusOK, health.StatusCode,
		"/healthz was shed by the rate limiter; Docker would kill this container")
	require.Empty(t, health.Header.Get(middleware.HeaderRateLimit),
		"/healthz is not part of the metered API surface and must not advertise a budget")
}

// TestRateLimit_RunsBeforeTheAuthorizationGate.
//
// The order is the security argument (internal/api/server.go): a flood has to be shed before it costs
// a session lookup, a token hash or an argon2 verification. This asserts it observably rather than by
// reading the mount order — the operation below answers 503 because this instance never reconciled
// its permission catalogue, and once the bucket is empty the SAME request answers 429 instead, which
// can only happen if the limiter ran first.
func TestRateLimit_RunsBeforeTheAuthorizationGate(t *testing.T) {
	t.Parallel()

	// The zero AuthorizationState: an instance that never reconciled, so every operation declaring a
	// permission answers 503 (issue #272).
	srv := httptest.NewServer(api.New(api.Config{Clock: stopped()}))
	t.Cleanup(srv.Close)

	first := send(t, srv, http.MethodPatch, "/api/v1/guild", "")

	require.Equal(t, http.StatusServiceUnavailable, first.StatusCode,
		"the premise of this test is wrong: this operation is expected to be refused by the "+
			"authorization gate, so that the limiter overtaking it is visible")
	require.NotEmpty(t, first.Header.Get(middleware.HeaderRateLimit),
		"a response refused by a later gate still carries the budget it spent")

	for range writeQuota {
		send(t, srv, http.MethodPatch, "/api/v1/guild", "")
	}

	require.Equal(t, http.StatusTooManyRequests,
		send(t, srv, http.MethodPatch, "/api/v1/guild", "").StatusCode,
		"the limiter did not overtake the authorization gate; it is mounted after it")
}
