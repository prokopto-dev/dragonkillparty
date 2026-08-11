package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubChecker is a ReadyChecker that answers with a fixed report. The interface is declared by this
// package precisely so a handler test does not need a database, a migrator or a temp directory.
type stubChecker struct{ report ReadyReport }

func (s stubChecker) Ready() ReadyReport { return s.report }

// readyz issues GET /readyz from remoteAddr and returns the status and body.
//
// remoteAddr is a parameter because it is the subject of half the tests below: httptest.NewRequest
// defaults to 192.0.2.1 (TEST-NET-1), which is a PUBLIC address, so a test that wants to be treated as
// an operator on the box has to say so.
func readyz(t *testing.T, checker ReadyChecker, remoteAddr string) (int, string) {
	t.Helper()

	return readyzWithHeaders(t, checker, remoteAddr, nil)
}

// readyzWithHeaders is readyz with request headers, for the proxy-evidence cases.
func readyzWithHeaders(t *testing.T, checker ReadyChecker, remoteAddr string, headers map[string]string) (int, string) {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.RemoteAddr = remoteAddr

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	New(Config{Readiness: checker}).ServeHTTP(rec, req)

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	return res.StatusCode, rec.Body.String()
}

// TestReadyz_LedgerAppendOnlyDegraded_Returns503 is the acceptance criterion of issue #59 at the
// handler boundary: a check whose answer is "this database's ledger can be rewritten" must make the
// readiness probe say no, on every poll, for as long as it stays true.
//
// The state is `degraded` rather than `failed` because the two are different events — one is an
// evaluated check with a bad answer, the other is a check that could not be evaluated — and both are
// 503 because a probe that answers 200 with the bad news in the body is a probe whose bad news no
// blackbox monitor ever reads. That was the whole gap: the boot path already logs this once.
func TestReadyz_LedgerAppendOnlyDegraded_Returns503(t *testing.T) {
	t.Parallel()

	checker := stubChecker{report: ReadyReport{
		Check:  "ledger_append_only",
		State:  ReadyStateDegraded,
		Detail: "missing append-only triggers: trg_ledger_entry_no_update",
	}}

	status, body := readyz(t, checker, "127.0.0.1:54321")

	require.Equal(t, http.StatusServiceUnavailable, status,
		"an instance whose ledger has lost its append-only protection reported itself ready")
	require.JSONEq(t, `{"check":"ledger_append_only","state":"degraded",`+
		`"detail":"missing append-only triggers: trg_ledger_entry_no_update"}`, body)
}

// TestReadyz_EveryCheckPasses_Returns200WithTheReadyBody is the positive control, and it also pins the
// body a healthy instance answers.
//
// Without it, a /readyz that reported degraded unconditionally would satisfy the test above and take
// every instance out of rotation forever. The exact body matters too: adding a second check must not
// add a field to the healthy response, because cmd/dkp/ready_test.go and any operator's monitoring
// match on it.
func TestReadyz_EveryCheckPasses_Returns200WithTheReadyBody(t *testing.T) {
	t.Parallel()

	checker := stubChecker{report: ReadyReport{Check: "migrations", State: ReadyStateReady}}

	status, body := readyz(t, checker, "127.0.0.1:54321")

	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `{"check":"migrations","state":"ready"}`, body)
}

// TestReadyz_PublicCaller_DetailIsRedactedAndTheVerdictIsNot is the disclosure rule from
// docs/design/06-cicd-and-release.md §"Health endpoints", which /readyz owed from the moment it grew a
// check with a detail.
//
// The two halves are one decision. `check` and `state` stay public because monitoring has to see that
// something is wrong and the 503 already says that much; `detail` is withheld because the ledger
// check's detail names which guarantee on this guild's ledger is currently absent, which is
// reconnaissance for exactly the person who would use it.
func TestReadyz_PublicCaller_DetailIsRedactedAndTheVerdictIsNot(t *testing.T) {
	t.Parallel()

	checker := stubChecker{report: ReadyReport{
		Check:  "ledger_append_only",
		State:  ReadyStateDegraded,
		Detail: "missing append-only triggers: trg_ledger_entry_no_update",
	}}

	tests := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{
			name:       "loopback sees the detail",
			remoteAddr: "127.0.0.1:41000",
			want: `{"check":"ledger_append_only","state":"degraded",` +
				`"detail":"missing append-only triggers: trg_ledger_entry_no_update"}`,
		},
		{
			name:       "a docker bridge address sees the detail",
			remoteAddr: "172.17.0.1:41000",
			want: `{"check":"ledger_append_only","state":"degraded",` +
				`"detail":"missing append-only triggers: trg_ledger_entry_no_update"}`,
		},
		{
			name:       "the public internet gets the verdict and nothing else",
			remoteAddr: "203.0.113.7:41000",
			want:       `{"check":"ledger_append_only","state":"degraded"}`,
		},
		{
			name:       "a public IPv6 caller likewise",
			remoteAddr: "[2001:db8::1]:41000",
			want:       `{"check":"ledger_append_only","state":"degraded"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status, body := readyz(t, checker, tc.remoteAddr)

			require.Equal(t, http.StatusServiceUnavailable, status)
			require.JSONEq(t, tc.want, body)
		})
	}
}

// TestReadyz_ProxiedCaller_DetailIsRedactedEvenFromLoopback is the hole the address test alone leaves,
// and it is the one that matters in the deployment this project recommends.
//
// A reverse proxy on the same host presents 127.0.0.1 for every caller alive; one in a container or in
// front of a cluster presents an RFC-1918 address. Either way `localCaller` says yes and the whole
// internet would be handed the names of the ledger protections this instance is missing. So evidence
// that the request was relayed redacts the detail regardless of how local the peer looks.
//
// The header CONTENTS are never read, which is why a forged one cannot be used to unredact anything:
// the only thing an attacker achieves by adding a header here is a smaller response. Note the
// X-Forwarded-For value in the third case is a loopback address — the shape that would work if this
// code believed what it was told.
func TestReadyz_ProxiedCaller_DetailIsRedactedEvenFromLoopback(t *testing.T) {
	t.Parallel()

	checker := stubChecker{report: ReadyReport{
		Check:  "ledger_append_only",
		State:  ReadyStateDegraded,
		Detail: "missing append-only triggers: trg_ledger_entry_no_update",
	}}

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "x-forwarded-for, the ubiquitous one", headers: map[string]string{"X-Forwarded-For": "203.0.113.9"}},
		{name: "forwarded, RFC 7239", headers: map[string]string{"Forwarded": "for=203.0.113.9;proto=https"}},
		{name: "a forged local x-forwarded-for buys nothing", headers: map[string]string{"X-Forwarded-For": "127.0.0.1"}},
		{name: "x-forwarded-proto alone still proves a relay", headers: map[string]string{"X-Forwarded-Proto": "https"}},
		{name: "x-forwarded-host alone", headers: map[string]string{"X-Forwarded-Host": "dkp.example.org"}},
		{name: "x-forwarded-port alone, which a fixed three-header list missed", headers: map[string]string{"X-Forwarded-Port": "443"}},
		{name: "traefik x-forwarded-prefix, a family member nobody enumerated", headers: map[string]string{"X-Forwarded-Prefix": "/dkp"}},
		{
			name:    "present but EMPTY is still a relay: Header.Get cannot tell it from absent",
			headers: map[string]string{"X-Forwarded-For": ""},
		},
		{name: "nginx x-real-ip", headers: map[string]string{"X-Real-Ip": "203.0.113.9"}},
		{name: "cloudflare", headers: map[string]string{"CF-Connecting-IP": "203.0.113.9"}},
		{name: "akamai / cloudflare enterprise", headers: map[string]string{"True-Client-IP": "203.0.113.9"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// 127.0.0.1: a same-host reverse proxy, which is the exact deployment the docs recommend.
			status, body := readyzWithHeaders(t, checker, "127.0.0.1:41000", tc.headers)

			require.Equal(t, http.StatusServiceUnavailable, status)
			require.JSONEq(t, `{"check":"ledger_append_only","state":"degraded"}`, body,
				"a request relayed by a proxy was handed the detail because the PEER looked local")
		})
	}
}

// TestReadyz_UnproxiedLoopback_StillSeesTheDetail is the control for the test above.
//
// Without it, redacting unconditionally would satisfy every proxy case and quietly remove the
// operator's only in-band way to see which trigger is missing. Somebody on the box asking the process
// directly — which is what `curl localhost:8080/readyz` is — must still get the actionable string.
func TestReadyz_UnproxiedLoopback_StillSeesTheDetail(t *testing.T) {
	t.Parallel()

	checker := stubChecker{report: ReadyReport{
		Check:  "ledger_append_only",
		State:  ReadyStateDegraded,
		Detail: "missing append-only triggers: trg_ledger_entry_no_update",
	}}

	// A header a proxy does not set must not trip the check, or every request through any middleware
	// would lose its detail.
	status, body := readyzWithHeaders(t, checker, "127.0.0.1:41000",
		map[string]string{"User-Agent": "curl/8.7.1", "Accept": "*/*"})

	require.Equal(t, http.StatusServiceUnavailable, status)
	require.JSONEq(t, `{"check":"ledger_append_only","state":"degraded",`+
		`"detail":"missing append-only triggers: trg_ledger_entry_no_update"}`, body)
}

// TestReadyz_ProxiedCaller_TheVerdictAndTheCommandSurvive keeps the redaction from eating the two
// things that must reach a proxied caller.
//
// Monitoring behind a proxy still has to see that the instance is not ready and which check failed —
// that is the whole point of #59 — and the migrations-pending `command` is the documented public
// exception the SPA renders as a banner. A redaction that took either would fix a disclosure by
// breaking the feature.
func TestReadyz_ProxiedCaller_TheVerdictAndTheCommandSurvive(t *testing.T) {
	t.Parallel()

	proxied := map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"}

	status, body := readyzWithHeaders(t, stubChecker{report: ReadyReport{
		Check: "migrations", State: ReadyStatePending, Command: "dkp migrate",
	}}, "127.0.0.1:41000", proxied)

	require.Equal(t, http.StatusServiceUnavailable, status)
	require.JSONEq(t, `{"check":"migrations","state":"pending","command":"dkp migrate"}`, body,
		"the migrations-pending body is public by decision and the SPA renders the command verbatim")

	status, body = readyzWithHeaders(t, stubChecker{report: ReadyReport{
		Check: "migrations", State: ReadyStateReady,
	}}, "127.0.0.1:41000", proxied)

	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `{"check":"migrations","state":"ready"}`, body)
}

// TestViaProxy_Classification pins the evidence rule, which is header-KEY presence over the whole
// X-Forwarded-* family plus four named headers — not a nonempty value, and not a fixed list of the
// three X-Forwarded members somebody happened to think of.
//
// Both distinctions are regressions waiting to happen, and both were live in this PR's first revision
// of the function:
//
//   - Header.Get returns "" for a header that is PRESENT and empty, so `X-Forwarded-For:` with no value
//     read as "not proxied" and released the detail to a proxy's public callers.
//   - a proxy that sets only X-Forwarded-Port (or Traefik's X-Forwarded-Prefix, or a future member of
//     the family) was invisible to a three-name list, with the same result.
//
// The negative rows are equally the point: an ordinary request must not be mistaken for a relayed one,
// or the detail becomes unreachable everywhere and the check quietly stops being useful.
func TestViaProxy_Classification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
		proxied bool
	}{
		{name: "no headers at all", proxied: false},
		{name: "ordinary client headers", headers: map[string]string{
			"User-Agent": "Prometheus/2.53", "Accept": "application/json",
		}, proxied: false},
		{name: "a header that merely mentions forwarding is not one", headers: map[string]string{
			"X-Forwarded": "not a real header", "Forwarded-By": "nobody",
		}, proxied: false},
		{name: "an empty x-forwarded-for IS evidence: the key is there", headers: map[string]string{
			"X-Forwarded-For": "",
		}, proxied: true},
		{name: "an empty forwarded likewise", headers: map[string]string{"Forwarded": ""}, proxied: true},
		{name: "x-forwarded-for", headers: map[string]string{"X-Forwarded-For": "203.0.113.9"}, proxied: true},
		{name: "forwarded", headers: map[string]string{"Forwarded": "for=203.0.113.9"}, proxied: true},
		{name: "x-forwarded-proto", headers: map[string]string{"X-Forwarded-Proto": "https"}, proxied: true},
		{name: "x-forwarded-host", headers: map[string]string{"X-Forwarded-Host": "dkp.example.org"}, proxied: true},
		{name: "x-forwarded-port, the one a fixed list missed", headers: map[string]string{"X-Forwarded-Port": "443"}, proxied: true},
		{name: "x-forwarded-prefix, traefik", headers: map[string]string{"X-Forwarded-Prefix": "/dkp"}, proxied: true},
		{name: "x-forwarded-server", headers: map[string]string{"X-Forwarded-Server": "edge-1"}, proxied: true},
		{name: "an x-forwarded member nobody has invented yet", headers: map[string]string{"X-Forwarded-Tenant": "g"}, proxied: true},
		{name: "x-real-ip", headers: map[string]string{"X-Real-Ip": "203.0.113.9"}, proxied: true},
		{name: "cf-connecting-ip, whose canonical form is Cf-Connecting-Ip", headers: map[string]string{"CF-Connecting-IP": "203.0.113.9"}, proxied: true},
		{name: "true-client-ip", headers: map[string]string{"True-Client-IP": "203.0.113.9"}, proxied: true},
		{
			name:    "header names are matched case-insensitively, as net/http canonicalises them",
			headers: map[string]string{"x-forwarded-for": "203.0.113.9"}, proxied: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			header := http.Header{}
			for name, value := range tc.headers {
				header.Set(name, value)
			}

			require.Equal(t, tc.proxied, viaProxy(header))
		})
	}
}

// TestViaProxy_NonCanonicalKey_IsStillEvidence covers a header map built by hand rather than through
// Header.Set.
//
// net/http canonicalises every key it parses off the wire, so this shape cannot arrive from a client —
// but viaProxy takes an http.Header, which is a plain map any in-process caller or middleware can
// populate directly, and a predicate that recognised only canonical keys would be one refactor away
// from a silent hole in a disclosure control.
func TestViaProxy_NonCanonicalKey_IsStillEvidence(t *testing.T) {
	t.Parallel()

	require.True(t, viaProxy(http.Header{"x-forwarded-port": []string{"443"}}))
	require.True(t, viaProxy(http.Header{"cf-connecting-ip": []string{"203.0.113.9"}}))
	require.True(t, viaProxy(http.Header{"X-FORWARDED-FOR": []string{""}}),
		"a present key with an empty value is evidence whatever its spelling")
}

// TestReadyz_MigrationsPending_BodyStaysPublicVerbatim guards the ONE stated exception to the
// redaction above, from both sides.
//
// The pending body is public by decision: it tells an unauthenticated caller only that the instance is
// mid-upgrade, which the 503 already tells them, and the SPA renders the command it names as a banner
// for an operator who may have no shell access at that moment. A redaction that ate `command` would
// blank that banner for exactly the person who needs it, and would do so silently.
func TestReadyz_MigrationsPending_BodyStaysPublicVerbatim(t *testing.T) {
	t.Parallel()

	checker := stubChecker{report: ReadyReport{
		Check: "migrations", State: ReadyStatePending, Command: "dkp migrate",
	}}

	for _, remoteAddr := range []string{"127.0.0.1:41000", "203.0.113.7:41000"} {
		t.Run(remoteAddr, func(t *testing.T) {
			t.Parallel()

			status, body := readyz(t, checker, remoteAddr)

			require.Equal(t, http.StatusServiceUnavailable, status)
			require.JSONEq(t, `{"check":"migrations","state":"pending","command":"dkp migrate"}`, body,
				"the migrations-pending body is a wire contract and is public on purpose")
		})
	}
}

// TestReadyz_NilChecker_Reports503Failed keeps the unwired case from reporting ready, and shows that
// the redaction applies to it too.
//
// The handler is called directly because that is the only way to reach this branch: api.New omits the
// route entirely when Config.Readiness is nil, which is the split PR 3 introduced and the test below
// pins. A checker that is nil at the handler must still answer 503 — reporting ready would hide a
// wiring bug behind a green load-balancer probe.
func TestReadyz_NilChecker_Reports503Failed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{
			name:       "an operator on the box is told what is wrong",
			remoteAddr: "127.0.0.1:41000",
			want:       `{"check":"migrations","state":"failed","detail":"no readiness checker configured"}`,
		},
		{
			name:       "a stranger learns only that it is not ready",
			remoteAddr: "203.0.113.7:41000",
			want:       `{"check":"migrations","state":"failed"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			req.RemoteAddr = tc.remoteAddr

			handleReadyz(rec, req, nil)

			res := rec.Result()
			t.Cleanup(func() { _ = res.Body.Close() })

			require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
			require.JSONEq(t, tc.want, rec.Body.String())
		})
	}
}

// TestReadyz_NoChecker_RouteIsAbsent pins the split PR 3 introduced: api.New with no Readiness
// registers no /readyz at all, so a binary that cannot reach a database does not answer a readiness
// probe it never evaluated.
func TestReadyz_NoChecker_RouteIsAbsent(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	New(Config{}).ServeHTTP(rec, req)

	res := rec.Result()
	t.Cleanup(func() { _ = res.Body.Close() })

	require.Equal(t, http.StatusNotFound, res.StatusCode)
}

// TestLocalCaller_Classification is the unit test for the address rule, including the cases that are
// easy to get wrong by writing the obvious prefix check.
//
// The allowlist is exactly what docs/design/06-cicd-and-release.md §"Health endpoints" authorises:
// loopback, RFC 1918, and RFC 4193 unique-local (the IPv6 range an IPv6-only Docker or Kubernetes
// network assigns, without which the rule could not be satisfied at all there). The rows asserting
// FALSE are the substance of the test — link-local and CGNAT are each one std-lib predicate away from
// being admitted by accident, and each would hand the names of a guild's missing ledger protections to
// anyone sharing a segment or a carrier NAT.
//
// Every unparseable form must classify as PUBLIC. That direction is the whole safety property: a
// RemoteAddr this function does not understand — a Unix socket peer, a proxy-protocol string, an empty
// value from a handler invoked directly — must not become a disclosure.
func TestLocalCaller_Classification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		addr  string
		local bool
	}{
		{name: "loopback v4", addr: "127.0.0.1:9000", local: true},
		{name: "loopback v4 without a port", addr: "127.0.0.1", local: true},
		{name: "loopback v6", addr: "[::1]:9000", local: true},
		{name: "rfc1918 ten", addr: "10.4.5.6:9000", local: true},
		{name: "rfc1918 seventeen-two, the docker bridge", addr: "172.17.0.1:9000", local: true},
		{name: "rfc1918 one-ninety-two", addr: "192.168.1.20:9000", local: true},
		{name: "rfc4193 unique local v6, what an IPv6-only docker network assigns", addr: "[fd00::1]:9000", local: true},
		{name: "v4-mapped rfc1918, an rfc1918 caller in an IPv6 hat", addr: "[::ffff:10.0.0.9]:9000", local: true},
		{name: "link-local v4 is NOT local: anything on the segment can reach it", addr: "169.254.10.1:9000", local: false},
		{name: "link-local v6 likewise", addr: "[fe80::1]:9000", local: false},
		{name: "test-net-1, which httptest defaults to", addr: "192.0.2.1:1234", local: false},
		{name: "public v4", addr: "203.0.113.7:9000", local: false},
		{name: "public v6", addr: "[2001:db8::1]:9000", local: false},
		{name: "carrier-grade nat is not rfc1918", addr: "100.64.0.1:9000", local: false},
		{name: "one-seventy-two outside the rfc1918 block", addr: "172.32.0.1:9000", local: false},
		{name: "empty", addr: "", local: false},
		{name: "not an address at all", addr: "@", local: false},
		{name: "a hostname is not an address", addr: "localhost:9000", local: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.local, localCaller(tc.addr))
		})
	}
}
