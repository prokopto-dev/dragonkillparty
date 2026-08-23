package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mustTrust parses a trust list that the test author asserts is well-formed.
func mustTrust(t *testing.T, spec string) TrustedProxies {
	t.Helper()

	trusted, err := ParseTrustedProxies(spec)
	require.NoError(t, err, "the test's own trust list does not parse")

	return trusted
}

// resolveRequest runs one synthetic request through a trust list and reports the resolved address.
//
// The headers are applied with Add rather than Set so a case can send two X-Forwarded-For field
// lines, which is what a two-proxy chain actually produces and which Header.Get would silently
// truncate to the first.
func resolveRequest(t *testing.T, trusted TrustedProxies, remoteAddr string, forwarded []string) netip.Addr {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	req.RemoteAddr = remoteAddr

	for _, line := range forwarded {
		req.Header.Add(HeaderForwardedFor, line)
	}

	return trusted.Resolve(req)
}

// TestParseTrustedProxies_Empty_TrustsNothing is the default the whole design rests on.
//
// docs/design/03-security.md §8.3: "DKP_TRUSTED_PROXIES is empty by default. With it empty,
// X-Forwarded-For … are ignored entirely." An empty value is NOT an error — it is the answer — and a
// list that parsed to something non-empty from an unset variable would be a trust boundary nobody
// asked for.
func TestParseTrustedProxies_Empty_TrustsNothing(t *testing.T) {
	t.Parallel()

	for _, spec := range []string{"", "   ", ",", " , ,"} {
		trusted, err := ParseTrustedProxies(spec)

		require.NoError(t, err, "an empty or separator-only list is the documented default, not an error")
		require.True(t, trusted.Empty(), "%q parsed to a non-empty trust list", spec)
		require.Equal(t, "(none)", trusted.String())
	}
}

// TestParseTrustedProxies_Malformed_FailsLoudly is issue #98's "fail the parse loudly rather than
// silently trusting nothing".
//
// The two outcomes are indistinguishable from outside the process — both ignore every forwarded
// header — and only one of them is what the operator meant. cmd/dkp makes this error fatal for that
// reason.
func TestParseTrustedProxies_Malformed_FailsLoudly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec string
	}{
		{name: "not an address at all", spec: "localhost"},
		{name: "hostname among valid entries", spec: "10.0.0.0/8, proxy.example.com"},
		{name: "prefix bits out of range", spec: "10.0.0.0/33"},
		{name: "empty prefix length", spec: "10.0.0.0/"},
		{name: "address with a port", spec: "127.0.0.1:8080"},
		{name: "v4-mapped v6 prefix matches nothing", spec: "::ffff:10.0.0.0/104"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			trusted, err := ParseTrustedProxies(tc.spec)

			require.Error(t, err, "a malformed trust list must not parse to a silently empty one")
			require.True(t, trusted.Empty(),
				"a failed parse returned a partially populated trust list, which would trust a "+
					"prefix the operator never successfully configured")
		})
	}
}

// TestParseTrustedProxies_Accepted_NormalisesEveryDocumentedSpelling.
//
// A bare IP is accepted as its own single-host prefix because operators write `127.0.0.1` far more
// often than `127.0.0.1/32`, and an unmasked prefix is masked rather than refused, because refusing
// either would be a boot failure over punctuation.
func TestParseTrustedProxies_Accepted_NormalisesEveryDocumentedSpelling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec string
		want string
	}{
		{name: "single cidr", spec: "10.0.0.0/8", want: "10.0.0.0/8"},
		{name: "bare ipv4", spec: "127.0.0.1", want: "127.0.0.1/32"},
		{name: "bare ipv6", spec: "::1", want: "::1/128"},
		{name: "host bits are masked off", spec: "10.1.2.3/8", want: "10.0.0.0/8"},
		{name: "whitespace and a trailing comma", spec: " 127.0.0.1/32 , 10.0.0.0/8 ,", want: "127.0.0.1/32,10.0.0.0/8"},
		{name: "v4-mapped bare address is unmapped", spec: "::ffff:127.0.0.1", want: "127.0.0.1/32"},
		{name: "ipv6 prefix", spec: "fd00::/8", want: "fd00::/8"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			trusted, err := ParseTrustedProxies(tc.spec)

			require.NoError(t, err)
			require.Equal(t, tc.want, trusted.String())
			require.False(t, trusted.Empty())
		})
	}
}

// TestTrustedProxies_Resolve_WalksRightToLeftAndNeverTakesTheLeftmost is the whole of §8.3's
// algorithm, case by case.
//
// Read the table as the specification: with the list empty nothing is read; with the PEER untrusted
// nothing is read; otherwise the chain is walked right to left popping trusted entries and the first
// untrusted one is the client. The leftmost entry is the one an attacker writes, and no row here
// returns it unless every hop in the chain was itself a trusted address.
func TestTrustedProxies_Resolve_WalksRightToLeftAndNeverTakesTheLeftmost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		trust     string
		peer      string
		forwarded []string
		want      string
	}{
		{
			name:      "empty trust list ignores the header entirely",
			trust:     "",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"203.0.113.9"},
			want:      "127.0.0.1",
		},
		{
			name:      "untrusted peer ignores the header entirely",
			trust:     "10.0.0.0/8",
			peer:      "198.51.100.7:5000",
			forwarded: []string{"203.0.113.9"},
			want:      "198.51.100.7",
		},
		{
			name:      "trusted peer, no header at all, is its own client",
			trust:     "127.0.0.1/32",
			peer:      "127.0.0.1:5000",
			forwarded: nil,
			want:      "127.0.0.1",
		},
		{
			name:      "one proxy: the single entry is the client",
			trust:     "127.0.0.1/32",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"203.0.113.9"},
			want:      "203.0.113.9",
		},
		{
			name:      "the leftmost entry is a forgery and is never reached",
			trust:     "127.0.0.1/32",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"1.2.3.4, 203.0.113.9"},
			want:      "203.0.113.9",
		},
		{
			name:      "a forged entry that looks trusted is still not reached",
			trust:     "127.0.0.1/32, 10.0.0.0/8",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"10.9.9.9, 203.0.113.9"},
			want:      "203.0.113.9",
		},
		{
			name:      "two real proxies: pop both, take the third from the right",
			trust:     "127.0.0.1/32, 10.0.0.0/8",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"203.0.113.9, 10.0.0.5, 10.0.0.6"},
			want:      "203.0.113.9",
		},
		{
			name:      "field lines are joined in order, not truncated to the first",
			trust:     "127.0.0.1/32, 10.0.0.0/8",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"203.0.113.9", "10.0.0.5, 10.0.0.6"},
			want:      "203.0.113.9",
		},
		{
			name:      "every hop trusted: the leftmost is a trusted address and is the answer",
			trust:     "10.0.0.0/8",
			peer:      "10.0.0.1:5000",
			forwarded: []string{"10.1.1.1, 10.0.0.5"},
			want:      "10.1.1.1",
		},
		{
			name:      "an unreadable entry stops the walk at the innermost verified hop",
			trust:     "127.0.0.1/32, 10.0.0.0/8",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"_hidden, 10.0.0.5"},
			want:      "10.0.0.5",
		},
		{
			name:      "an empty entry stops the walk the same way",
			trust:     "127.0.0.1/32, 10.0.0.0/8",
			peer:      "127.0.0.1:5000",
			forwarded: []string{", 10.0.0.5"},
			want:      "10.0.0.5",
		},
		{
			name:      "a v4-mapped peer matches an IPv4 CIDR",
			trust:     "127.0.0.1/32",
			peer:      "[::ffff:127.0.0.1]:5000",
			forwarded: []string{"203.0.113.9"},
			want:      "203.0.113.9",
		},
		{
			name:      "a v4-mapped chain entry resolves to its IPv4 form",
			trust:     "127.0.0.1/32",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"::ffff:203.0.113.9"},
			want:      "203.0.113.9",
		},
		{
			name:      "an IPv6 client behind an IPv6 proxy",
			trust:     "fd00::/8",
			peer:      "[fd00::1]:5000",
			forwarded: []string{"2001:db8::9, fd00::2"},
			want:      "2001:db8::9",
		},
		{
			name:      "a bracketed IPv6 entry",
			trust:     "127.0.0.1/32",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"[2001:db8::9]"},
			want:      "2001:db8::9",
		},
		{
			name:      "an entry carrying the source port",
			trust:     "127.0.0.1/32",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"203.0.113.9:41288"},
			want:      "203.0.113.9",
		},
		{
			name:      "a bracketed IPv6 entry carrying the source port",
			trust:     "127.0.0.1/32",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"[2001:db8::9]:41288"},
			want:      "2001:db8::9",
		},
		{
			name:      "a zone identifier is stripped rather than becoming part of the answer",
			trust:     "127.0.0.1/32",
			peer:      "127.0.0.1:5000",
			forwarded: []string{"fe80::1%eth0"},
			want:      "fe80::1",
		},
		{
			name:      "an IPv4 CIDR never matches an IPv6 peer by accident",
			trust:     "10.0.0.0/8",
			peer:      "[2001:db8::1]:5000",
			forwarded: []string{"203.0.113.9"},
			want:      "2001:db8::1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := resolveRequest(t, mustTrust(t, tc.trust), tc.peer, tc.forwarded)

			require.True(t, got.IsValid(), "no address resolved")
			require.Equal(t, tc.want, got.String())
		})
	}
}

// TestTrustedProxies_Resolve_LongChainStopsAtTheHopBound.
//
// The chain length is chosen by the caller, so the walk has to be bounded or "send a megabyte of
// trusted-looking addresses" becomes a way to make the server do arbitrary work per request. Hitting
// the bound behaves like hitting an unreadable entry — the walk stops and the innermost address it
// verified is the answer — rather than falling back to the leftmost entry, which is the forgery.
func TestTrustedProxies_Resolve_LongChainStopsAtTheHopBound(t *testing.T) {
	t.Parallel()

	// Every hop is inside the trusted CIDR, so nothing but the bound can stop the walk. The leftmost
	// entry is a public address: if the bound were absent, it would be the answer.
	hops := make([]string, 0, maxForwardedHops+2)
	hops = append(hops, "203.0.113.9")

	for range maxForwardedHops + 1 {
		hops = append(hops, "10.0.0.5")
	}

	got := resolveRequest(t, mustTrust(t, "10.0.0.0/8"), "10.0.0.1:5000",
		[]string{strings.Join(hops, ", ")})

	require.Equal(t, "10.0.0.5", got.String(),
		"the walk ran past its hop bound and reached the caller-supplied leftmost entry")
}

// TestPeerIP_Unparseable_ReportsAnInvalidAddress.
//
// A listener that is not TCP has no address to report, and the resolver's contract is that callers
// handle that rather than being handed a zero value that looks like a real key. internal/api's
// limiter turns it into one shared, throttled bucket; what it must never become is an exemption.
func TestPeerIP_Unparseable_ReportsAnInvalidAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		addr  string
		valid bool
		want  string
	}{
		{name: "tcp v4", addr: "192.0.2.4:1234", valid: true, want: "192.0.2.4"},
		{name: "tcp v6", addr: "[2001:db8::4]:1234", valid: true, want: "2001:db8::4"},
		{name: "no port", addr: "192.0.2.4", valid: true, want: "192.0.2.4"},
		{name: "v4-mapped", addr: "[::ffff:192.0.2.4]:1234", valid: true, want: "192.0.2.4"},
		{name: "unix socket", addr: "@", valid: false},
		{name: "empty", addr: "", valid: false},
		{name: "not an address", addr: "who-knows:1234", valid: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
			req.RemoteAddr = tc.addr

			got := PeerIP(req)

			require.Equal(t, tc.valid, got.IsValid())

			if tc.valid {
				require.Equal(t, tc.want, got.String())
			}
		})
	}
}

// TestClientIP_Middleware_PutsTheResolvedAddressOnTheContext is the one-resolution-per-request
// property issue #98 asks for: four call sites, one answer, computed here.
func TestClientIP_Middleware_PutsTheResolvedAddressOnTheContext(t *testing.T) {
	t.Parallel()

	var (
		seen  netip.Addr
		found bool
	)

	handler := ClientIP(mustTrust(t, "127.0.0.1/32"),
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, found = ClientIPFromContext(r.Context())
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set(HeaderForwardedFor, "1.2.3.4, 203.0.113.9")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	require.True(t, found, "the middleware did not put a client address on the context")
	require.Equal(t, "203.0.113.9", seen.String())
}

// TestClientIPFromContext_NotMounted_ReportsFalse.
//
// False must be distinguishable from "the zero address", because a consumer that keyed a rate
// limiter on a zero Addr would put every caller on earth into one bucket — a denial of service
// delivered by a wiring bug rather than by an attacker.
func TestClientIPFromContext_NotMounted_ReportsFalse(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)

	addr, ok := ClientIPFromContext(req.Context())

	require.False(t, ok)
	require.False(t, addr.IsValid())
}

// TestClientIP_ForwardedHeaderWithEmptyTrustList_WarnsExactlyOnce is §8.3's one-time warning, and the
// "once" is the security-relevant half.
//
// The trigger is "a request carrying a forwarded header", which is a condition any caller can produce
// on demand. A line per request would therefore be a log-flooding primitive handed to the internet —
// the same class of mistake as logging every rate-limit refusal.
func TestClientIP_ForwardedHeaderWithEmptyTrustList_WarnsExactlyOnce(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := ClientIP(TrustedProxies{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set(HeaderForwardedFor, "203.0.113.9")

		handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(withLogger(req.Context(), logger)))
	}

	// Count RECORDS, not mentions: one record names the variable several times — in the message, in
	// `env` and in `fix` — and counting the string would pass a five-line log as long as each line
	// happened to name it once.
	records := strings.Count(logged.String(), "level=WARN")

	require.Equal(t, 1, records,
		"the misconfiguration warning is not one-time; five requests produced %d records", records)
	require.Contains(t, logged.String(), "ignoring it",
		"the warning must say that the header was ignored, which is the behaviour being explained")
}

// TestClientIP_NoForwardedHeader_DoesNotWarn keeps the warning meaningful: an instance with no proxy
// in front of it and no trust list configured is correctly configured, and must not be told otherwise
// on every boot.
func TestClientIP_NoForwardedHeader_DoesNotWarn(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer

	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))

	handler := ClientIP(TrustedProxies{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	req.RemoteAddr = "127.0.0.1:5000"

	handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(withLogger(req.Context(), logger)))

	require.Empty(t, logged.String())
}

// TestClientIP_WholeForwardedFamily_TriggersTheWarning.
//
// §8.3 names four headers as ignored while the trust list is empty, and an operator who set only
// X-Real-IP on their proxy has exactly the misconfiguration the warning exists to name. Detecting
// only X-Forwarded-For would leave them silently sharing one bucket.
//
// PRESENCE, NOT CONTENTS: a bare `X-Forwarded-For:` with no value is still evidence of a relay, and
// Header.Get would report it as absent.
func TestClientIP_WholeForwardedFamily_TriggersTheWarning(t *testing.T) {
	t.Parallel()

	for _, header := range append(forwardedHeaders(), "") {
		t.Run("header "+header, func(t *testing.T) {
			t.Parallel()

			var logged bytes.Buffer

			logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))
			handler := ClientIP(TrustedProxies{},
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
			req.RemoteAddr = "127.0.0.1:5000"

			if header != "" {
				req.Header.Set(header, "")
			}

			handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(withLogger(req.Context(), logger)))

			if header == "" {
				require.Empty(t, logged.String(), "a request with no forwarded header warned")

				return
			}

			require.Contains(t, logged.String(), "DKP_TRUSTED_PROXIES",
				"a present-but-empty %s did not trigger the warning", header)
		})
	}
}

// TestTrustedProxies_Resolve_IgnoresXRealIPAndForwarded pins the deliberate narrowing this change
// makes to what the operator documentation implied.
//
// Neither header carries a chain, so neither can be walked: honouring X-Real-IP means believing one
// header completely, which is the leftmost-entry trap by another name as soon as the trust list is
// wider than the real proxy set. They are IGNORED rather than rejected, so nginx's default of setting
// both keeps working — X-Forwarded-For is read and X-Real-IP is not.
func TestTrustedProxies_Resolve_IgnoresXRealIPAndForwarded(t *testing.T) {
	t.Parallel()

	trusted := mustTrust(t, "127.0.0.1/32")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("X-Real-IP", "203.0.113.9")
	req.Header.Set("Forwarded", "for=203.0.113.9")

	require.Equal(t, "127.0.0.1", trusted.Resolve(req).String(),
		"a header with no chain to walk was believed")

	req.Header.Set(HeaderForwardedFor, "198.51.100.4")

	require.Equal(t, "198.51.100.4", trusted.Resolve(req).String(),
		"X-Forwarded-For stopped being read because its siblings were present")
}
