package safehttp

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// loopbackServer starts an httptest server — which binds 127.0.0.1 — and returns it with a counter
// of the requests its handler actually served.
//
// The counter is the point. "The request failed" is satisfied by a broken transport, a typo in a
// URL or a policy that refuses everything; "the handler was never reached" is the assertion that
// distinguishes a control from a coincidence.
func loopbackServer(t *testing.T, handler http.HandlerFunc) (srv *httptest.Server, served *atomic.Int64) {
	t.Helper()

	served = &atomic.Int64{}

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		handler(w, r)
	}))

	t.Cleanup(srv.Close)

	return srv, served
}

// serverPort returns the port an httptest server was assigned.
func serverPort(t *testing.T, rawURL string) uint16 {
	t.Helper()

	u, err := url.Parse(rawURL)
	require.NoError(t, err, "parse the test server URL")

	port, err := strconv.ParseUint(u.Port(), 10, 16)
	require.NoError(t, err, "the test server URL must carry a port")

	return uint16(port)
}

// get issues one GET through a client and returns the response and error.
func get(t *testing.T, client *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	require.NoError(t, err, "build the request")

	resp, err := client.Do(req) //nolint:bodyclose // the caller closes it; a refused request has none

	return resp, err
}

// TestNew_LoopbackServer_IsRefusedAtDial is the end-to-end half of the private-range suite: a real
// server, a real request, and a refusal that happens before the connection is made.
//
// The policy deliberately allows this server's SCHEME and PORT, so that the only control left that
// can refuse the request is the dialer's address check. A test that let the port allowlist do the
// refusing would pass just as happily with the Control hook removed.
func TestNew_LoopbackServer_IsRefusedAtDial(t *testing.T) {
	t.Parallel()

	srv, served := loopbackServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret"))
	})

	policy := Policy{
		AllowHTTPHosts: []string{"127.0.0.1"},
		AllowPorts:     []uint16{serverPort(t, srv.URL)},
	}

	resp, err := get(t, New(policy), srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.ErrorIs(t, err, ErrBlocked, "a loopback destination must be refused")
	require.Equal(t, classLoopback, blockClass(t, err))
	require.Zero(t, served.Load(), "the handler ran, so the refusal happened after the connection")

	// Not vacuous: the same server answers a client whose policy permits loopback. Without this the
	// assertions above would hold for a transport that could not reach anything at all.
	reachable, err := get(t, NewLoopback(2*time.Second), srv.URL)
	require.NoError(t, err, "the server itself is reachable")

	defer func() { _ = reachable.Body.Close() }()

	body, err := io.ReadAll(reachable.Body)
	require.NoError(t, err)
	require.Equal(t, "secret", string(body))
	require.Equal(t, int64(1), served.Load())
}

// TestNewLoopback_NonLoopbackAddress_IsRefused is the other half of the healthcheck exception: the
// loopback client is not a general-purpose client with the checks turned off.
//
// It cannot reach the LAN or the internet AT ALL — not a private address, not a public one — so a
// later change that reached for it because "it is the one that works" gets a refusal rather than an
// unpoliced egress.
func TestNewLoopback_NonLoopbackAddress_IsRefused(t *testing.T) {
	t.Parallel()

	policy := Policy{loopbackOnly: true}

	for _, addr := range []string{"10.0.0.5:8080", "203.0.113.9:443", "169.254.169.254:80"} {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()

			err := policy.control("tcp4", addr, nil)
			require.Equal(t, classNotLoopback, blockClass(t, err), "%s", addr)
		})
	}
}

// TestNew_Redirect_IsRefused pins the zero-redirect rule, and pins it by where the request does not
// arrive rather than by the error alone.
//
// A redirect is how an allowlisted URL becomes a different URL after it was approved. Every hop
// would be re-validated by the same dialer, so following one is not unsafe by construction — but
// nothing this package serves needs it, and the hop that is never made cannot be got wrong.
func TestNew_Redirect_IsRefused(t *testing.T) {
	t.Parallel()

	var followed atomic.Int64

	srv, served := loopbackServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/moved") {
			followed.Add(1)
			_, _ = w.Write([]byte("followed"))

			return
		}

		http.Redirect(w, r, "/moved", http.StatusFound)
	})

	resp, err := get(t, NewLoopback(2*time.Second), srv.URL+"/start")
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.ErrorIs(t, err, ErrRedirect, "a redirect must not be followed")
	require.Zero(t, followed.Load(), "the redirect target was fetched")
	require.Equal(t, int64(1), served.Load(), "exactly the first request should have been served")
}

// TestNew_ResponseBody_StopsAtTheCap covers the response-size bound, at the boundary in both
// directions: a body of exactly the cap is delivered whole, and the first byte past it fails.
func TestNew_ResponseBody_StopsAtTheCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "under the cap", size: 8},
		{name: "exactly the cap", size: 16},
		{name: "one byte over", size: 17, wantErr: true},
		{name: "far over", size: 4096, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, _ := loopbackServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("x", tc.size)))
			})

			client := newClient(Policy{loopbackOnly: true, MaxBodyBytes: 16, Timeout: 2 * time.Second}, nil)

			resp, err := get(t, client, srv.URL)
			require.NoError(t, err, "the request itself succeeds; the cap is enforced on the read")

			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)

			if tc.wantErr {
				require.ErrorIs(t, err, ErrBodyTooLarge)

				return
			}

			require.NoError(t, err)
			require.Len(t, body, tc.size)
		})
	}
}

// TestNew_Transport_IsWiredAsDeclared reads the client back and asserts the bounds that have no
// behaviour to observe from outside.
//
// Proxy is the one that most needs saying. An environment-driven proxy would move every dial to the
// proxy's own address, so the Control hook would validate the proxy and wave the real destination
// through — the whole defence, defeated by an inherited HTTP_PROXY. It cannot be tested through a
// request either: net/http's ProxyFromEnvironment ignores a proxy for loopback destinations, so a
// behavioural test against an httptest server would pass with the proxy wired up. Reading the field
// is the honest assertion.
func TestNew_Transport_IsWiredAsDeclared(t *testing.T) {
	t.Parallel()

	client := New(Policy{})

	require.Equal(t, DefaultTimeout, client.Timeout, "the whole-request deadline")
	require.NotNil(t, client.CheckRedirect, "a client with no CheckRedirect follows ten redirects")

	guarded, ok := client.Transport.(*guard)
	require.True(t, ok, "the transport must be the policy guard, not a bare http.Transport")
	require.Equal(t, DefaultMaxBodyBytes, guarded.policy.MaxBodyBytes, "the body cap defaults in")
	require.Equal(t, DefaultConnectTimeout, guarded.policy.ConnectTimeout, "so does the connect timeout")

	transport, ok := guarded.next.(*http.Transport)
	require.True(t, ok, "the guard must wrap an http.Transport")

	require.Nil(t, transport.Proxy, "HTTP_PROXY must not be inherited: a proxy moves the dial")
	require.NotNil(t, transport.DialContext, "the dialer carrying the address check must be installed")
	require.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
}

// TestNew_UnexpectedPort_IsRefusedBeforeAnyLookup asserts the cheap half runs first: a URL the
// policy will not accept never becomes a DNS query, let alone a connection.
//
// The host is .invalid (RFC 6761), so if the guard did not refuse it the test would fail slowly
// against a resolver rather than quickly against the policy — which is itself the signal.
func TestNew_UnexpectedPort_IsRefusedBeforeAnyLookup(t *testing.T) {
	t.Parallel()

	resp, err := get(t, New(Policy{}), "https://idp.invalid:8443/.well-known/jwks.json")
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.ErrorIs(t, err, ErrBlocked)
	require.Equal(t, classPort, blockClass(t, err))
}
