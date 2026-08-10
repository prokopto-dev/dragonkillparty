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

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.RemoteAddr = remoteAddr

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
		{name: "rfc4193 unique local v6", addr: "[fd00::1]:9000", local: true},
		{name: "link-local v4", addr: "169.254.10.1:9000", local: true},
		{name: "link-local v6", addr: "[fe80::1]:9000", local: true},
		{name: "v4-mapped rfc1918, an rfc1918 caller in an IPv6 hat", addr: "[::ffff:10.0.0.9]:9000", local: true},
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
