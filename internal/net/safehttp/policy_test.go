package safehttp

import (
	"net/netip"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// allowed is the sentinel a table row uses for "this address may be dialled". A class is a refusal;
// the empty string is not, and spelling it out keeps a row from reading as an oversight.
const allowed = ""

// blockClass returns the refusal class of an error, or the empty string when there was none.
func blockClass(t *testing.T, err error) string {
	t.Helper()

	if err == nil {
		return allowed
	}

	require.ErrorIs(t, err, ErrBlocked, "every refusal must unwrap to ErrBlocked")

	var blocked *BlockedError

	require.ErrorAs(t, err, &blocked, "every refusal must carry its class")

	return blocked.Class
}

// TestPolicyCheckAddr_DefaultPolicy_RefusesEverythingButThePublicInternet is the private-range
// suite: one row per class of address a self-hosted box can reach, decided by the function the
// dialer calls.
//
// The two allowed rows are as load-bearing as the refusals. A control that denies everything is
// indistinguishable in a test suite from a control that works, right up until JWKS fetching does
// not work either; 203.0.113.9 is TEST-NET-3 and 2606:4700:4700::1111 is a real public resolver,
// and both must pass.
func TestPolicyCheckAddr_DefaultPolicy_RefusesEverythingButThePublicInternet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr  string
		class string
	}{
		{"203.0.113.9", allowed},
		{"2606:4700:4700::1111", allowed},

		{"127.0.0.1", classLoopback},
		{"127.1.2.3", classLoopback},
		{"::1", classLoopback},

		{"10.0.0.5", classPrivate},
		{"172.16.0.1", classPrivate},
		{"172.31.255.255", classPrivate},
		{"192.168.1.1", classPrivate},
		{"fd00::1", classPrivate},
		{"100.64.0.1", classPrivate},
		{"100.127.255.254", classPrivate},

		// The metadata endpoint, and the range it lives in. Refused in its own class because
		// AllowPrivate must not reach it.
		{"169.254.169.254", classLinkLocal},
		{"169.254.0.1", classLinkLocal},
		{"fe80::1", classLinkLocal},

		{"224.0.0.1", classMulticast},
		{"239.255.255.250", classMulticast},
		{"ff02::1", classMulticast},
		{"ff01::1", classMulticast},

		{"0.0.0.0", classUnspecified},
		{"::", classUnspecified},

		{"::ffff:127.0.0.1", classMappedIPv4},
		{"::ffff:203.0.113.9", classMappedIPv4},

		{"0.1.2.3", classReserved},
		{"192.0.0.170", classReserved},
		{"240.0.0.1", classReserved},
		{"255.255.255.255", classReserved},
		{"::2", classReserved},
		{"100::1", classReserved},
		{"64:ff9b::7f00:1", classReserved},
		{"64:ff9b:1::1", classReserved},
		{"2001::1", classReserved},
		{"2002:7f00:1::1", classReserved},
	}

	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()

			err := Policy{}.checkAddr(netip.MustParseAddr(tc.addr))
			require.Equal(t, tc.class, blockClass(t, err), "%s", tc.addr)
		})
	}
}

// TestPolicyCheckAddr_AllowPrivate_RelaxesTheLANButNotLinkLocal pins the one asymmetry in the
// policy, because it is the one a later reader is most likely to "fix".
//
// DKP_OUTBOUND_ALLOW_PRIVATE is how a deployment reaches an endpoint on its own LAN. It is not how
// anybody reaches 169.254.169.254, which is the cloud metadata service and the highest-value SSRF
// target there is — an operator who genuinely means that address types it into
// DKP_OUTBOUND_ALLOW_CIDRS.
func TestPolicyCheckAddr_AllowPrivate_RelaxesTheLANButNotLinkLocal(t *testing.T) {
	t.Parallel()

	policy := Policy{AllowPrivate: true}

	tests := []struct {
		addr  string
		class string
	}{
		{"127.0.0.1", allowed},
		{"::1", allowed},
		{"10.0.0.5", allowed},
		{"192.168.1.1", allowed},
		{"fd00::1", allowed},
		{"100.64.0.1", allowed},

		{"169.254.169.254", classLinkLocal},
		{"fe80::1", classLinkLocal},
		{"224.0.0.1", classMulticast},
		{"0.0.0.0", classUnspecified},
		{"240.0.0.1", classReserved},
		{"64:ff9b::7f00:1", classReserved},
		{"::ffff:10.0.0.5", classMappedIPv4},
	}

	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()

			err := policy.checkAddr(netip.MustParseAddr(tc.addr))
			require.Equal(t, tc.class, blockClass(t, err), "%s", tc.addr)
		})
	}
}

// TestPolicyCheckAddr_AllowCIDRs_PermitsTheListedPrefixOnly is the narrow form the design prefers
// over AllowPrivate: one prefix, typed by an operator, audited and step-up gated.
//
// It reaches further than AllowPrivate deliberately — an explicit /32 for a metadata address is
// permitted, because somebody wrote that address down — and stops at the structural classes, which
// no configuration makes into a sensible HTTP peer.
func TestPolicyCheckAddr_AllowCIDRs_PermitsTheListedPrefixOnly(t *testing.T) {
	t.Parallel()

	policy := Policy{AllowCIDRs: []netip.Prefix{
		netip.MustParsePrefix("10.0.5.0/24"),
		netip.MustParsePrefix("169.254.169.254/32"),
	}}

	tests := []struct {
		addr  string
		class string
	}{
		{"10.0.5.7", allowed},
		{"10.0.5.255", allowed},
		{"169.254.169.254", allowed},

		{"10.0.6.7", classPrivate},
		{"169.254.169.253", classLinkLocal},
		{"127.0.0.1", classLoopback},
		{"224.0.0.1", classMulticast},
		{"0.0.0.0", classUnspecified},
	}

	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()

			err := policy.checkAddr(netip.MustParseAddr(tc.addr))
			require.Equal(t, tc.class, blockClass(t, err), "%s", tc.addr)
		})
	}
}

// TestPolicyControl_NonTCPOrUnparseableAddress_FailsClosed covers the two inputs the dialer should
// never hand this function.
//
// Both are refused rather than permitted, and that direction is the decision: an address shape
// nobody has reasoned about is exactly the one an attacker would like the check to wave through.
func TestPolicyControl_NonTCPOrUnparseableAddress_FailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		network string
		address string
		class   string
	}{
		{"a unix socket is not a shape this package dials", "unix", "/var/run/x.sock", classNetwork},
		{"udp is not either", "udp4", "203.0.113.9:443", classNetwork},
		{"an address that does not parse", "tcp4", "not-an-address", classAddress},
		{"a hostname is not an address — the dialer resolves first", "tcp4", "example.com:443", classAddress},
		{"a zoned link-local address", "tcp6", "[fe80::1%eth0]:443", classLinkLocal},
		{"the public case still passes", "tcp4", "203.0.113.9:443", allowed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := Policy{}.control(tc.network, tc.address, nil)
			require.Equal(t, tc.class, blockClass(t, err), "%s", tc.address)
		})
	}
}

// TestPolicyControl_PortAllowlist_IsTwoPortsPlusWhatWasDeclared covers the port half of the dialer
// check. It is decided on the port the socket connects to, so a URL that hides the port behind a
// redirect or a scheme default cannot get a different answer than one that spells it out.
func TestPolicyControl_PortAllowlist_IsTwoPortsPlusWhatWasDeclared(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		policy  Policy
		address string
		class   string
	}{
		{"80 is allowed", Policy{}, "203.0.113.9:80", allowed},
		{"443 is allowed", Policy{}, "203.0.113.9:443", allowed},
		{"8080 is not", Policy{}, "203.0.113.9:8080", classPort},
		{"22 is not", Policy{}, "203.0.113.9:22", classPort},
		{"6379 is not — a redis on the LAN is the classic pivot", Policy{}, "203.0.113.9:6379", classPort},
		{"a declared port is", Policy{AllowPorts: []uint16{8443}}, "203.0.113.9:8443", allowed},
		{"declaring one does not declare its neighbour", Policy{AllowPorts: []uint16{8443}}, "203.0.113.9:8444", classPort},
		{"a loopback-only client may use any port", Policy{loopbackOnly: true}, "127.0.0.1:8080", allowed},
		{"…but still only loopback", Policy{loopbackOnly: true}, "10.0.0.5:8080", classNotLoopback},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.policy.control("tcp4", tc.address, nil)
			require.Equal(t, tc.class, blockClass(t, err), "%s", tc.address)
		})
	}
}

// TestPolicyCheckURL_SchemePortAndCredentials_AreRefusedBeforeAnyLookup covers the cheap half: the
// refusals that can be decided from the URL alone, before a name is resolved.
func TestPolicyCheckURL_SchemePortAndCredentials_AreRefusedBeforeAnyLookup(t *testing.T) {
	t.Parallel()

	httpAllowed := Policy{AllowHTTPHosts: []string{"icons.example.com"}}

	tests := []struct {
		name   string
		policy Policy
		url    string
		class  string
	}{
		{"https is the default", Policy{}, "https://idp.example.com/.well-known/jwks.json", allowed},
		{"an explicit 443 is the same thing", Policy{}, "https://idp.example.com:443/jwks", allowed},
		{"plain http is not", Policy{}, "http://idp.example.com/jwks", classScheme},
		{"…unless the host was allowlisted", httpAllowed, "http://icons.example.com/i.png", allowed},
		{"the allowlist is case- and root-dot-insensitive", httpAllowed, "http://Icons.Example.com./i.png", allowed},
		{"another host does not inherit it", httpAllowed, "http://other.example.com/i.png", classScheme},
		{"ftp is not a scheme this package speaks", Policy{}, "ftp://files.example.com/x", classScheme},
		{"nor is file", Policy{}, "file:///etc/passwd", classHost},
		{"credentials in a URL are the oldest bypass there is", Policy{}, "https://idp.example.com@10.0.0.5/x", classUserinfo},
		{"an unexpected port is refused here too", Policy{}, "https://idp.example.com:8443/x", classPort},
		{"a declared one is not", Policy{AllowPorts: []uint16{8443}}, "https://idp.example.com:8443/x", allowed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			u, err := url.Parse(tc.url)
			require.NoError(t, err, "the fixture URL must parse")

			require.Equal(t, tc.class, blockClass(t, tc.policy.checkURL(u)), "%s", tc.url)
		})
	}
}

// TestParsePolicy_UnparseableConfiguration_FailsClosed is the configuration half.
//
// Every error path must ALSO return the strict policy, because the caller that logs a
// configuration error and carries on is the one this protects: a parse failure that returned a
// half-built permissive policy would turn a typo in a compose file into an open egress.
func TestParsePolicy_UnparseableConfiguration_FailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		allowPrivate string
		allowCIDRs   string
		wantErr      string
		wantPrivate  bool
		wantCIDRs    []netip.Prefix
	}{
		{name: "unset is the strict default"},
		{name: "false is too", allowPrivate: "false"},
		{name: "true opts in", allowPrivate: "true", wantPrivate: true},
		{name: "case and space are forgiven in a compose file", allowPrivate: " TRUE ", wantPrivate: true},
		{name: "anything else is a configuration error", allowPrivate: "yes", wantErr: AllowPrivateEnv},
		{
			name:       "a CIDR list is comma-separated",
			allowCIDRs: "10.0.5.0/24, 192.168.4.0/22",
			wantCIDRs: []netip.Prefix{
				netip.MustParsePrefix("10.0.5.0/24"),
				netip.MustParsePrefix("192.168.4.0/22"),
			},
		},
		{
			name:       "a host inside a prefix is read as the prefix",
			allowCIDRs: "10.0.5.7/24",
			wantCIDRs:  []netip.Prefix{netip.MustParsePrefix("10.0.5.0/24")},
		},
		{name: "an empty field is skipped, not an error", allowCIDRs: ",,"},
		{name: "a bare address is not a prefix", allowCIDRs: "10.0.5.7", wantErr: AllowCIDRsEnv},
		{name: "nor is prose", allowCIDRs: "the office", wantErr: AllowCIDRsEnv},
		{name: "/0 permits everything, which is not an allowlist", allowCIDRs: "0.0.0.0/0", wantErr: "not an allowlist"},
		{name: "the IPv6 spelling of the same thing", allowCIDRs: "::/0", wantErr: "not an allowlist"},
		{
			name:         "a bad boolean discards a good CIDR list too",
			allowPrivate: "sure",
			allowCIDRs:   "10.0.5.0/24",
			wantErr:      AllowPrivateEnv,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			policy, err := ParsePolicy(tc.allowPrivate, tc.allowCIDRs)

			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Equal(t, Policy{}, policy, "a failed parse must return the strict policy")

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantPrivate, policy.AllowPrivate)
			require.Equal(t, tc.wantCIDRs, policy.AllowCIDRs)
		})
	}
}
