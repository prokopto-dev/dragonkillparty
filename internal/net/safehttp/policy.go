package safehttp

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The environment variables that configure the policy, documented in
// docs/operations/configuration.md §"Outbound network policy". They are READ IN cmd/dkp, not here:
// this package parses the values it is handed, so that a policy can be built in a test without
// touching the process environment.
const (
	AllowPrivateEnv = "DKP_OUTBOUND_ALLOW_PRIVATE"
	AllowCIDRsEnv   = "DKP_OUTBOUND_ALLOW_CIDRS"
)

// The bounds every client carries unless a caller narrows them further (03-security.md §8.1).
const (
	// DefaultTimeout bounds the whole request — connect, TLS, headers and body together.
	DefaultTimeout = 10 * time.Second

	// DefaultConnectTimeout bounds one dial. Shorter than the total, because a dial that has not
	// completed in three seconds is a host that is not going to answer, and an outbound surface
	// that ties up a request goroutine per unreachable webhook is an amplifier.
	DefaultConnectTimeout = 3 * time.Second

	// DefaultMaxBodyBytes caps the response body. JWKS documents are kilobytes; a hostile endpoint
	// that streams forever is otherwise an out-of-memory kill on a Raspberry Pi.
	DefaultMaxBodyBytes int64 = 1 << 20
)

// The failure classes. They are the ONLY thing an error carries into a message: a raw dial error
// distinguishes "connection refused" from "no route to host" from "blocked", and that difference is
// a port scanner (03-security.md §8.1, "a raw dial error is an SSRF oracle"). The specific address
// stays on [BlockedError] as a field, for a caller that wants to log it deliberately.
const (
	classScheme      = "scheme"
	classUserinfo    = "userinfo"
	classHost        = "host"
	classPort        = "port"
	classNetwork     = "network"
	classAddress     = "address"
	classLoopback    = "loopback"
	classPrivate     = "private"
	classLinkLocal   = "link-local"
	classMulticast   = "multicast"
	classUnspecified = "unspecified"
	classMappedIPv4  = "ipv4-mapped"
	classReserved    = "reserved"
	classNotLoopback = "not-loopback"
)

var (
	// ErrBlocked is what every policy refusal unwraps to, so a caller can tell "we would not make
	// this request" apart from "the request failed" with one errors.Is.
	ErrBlocked = errors.New("blocked by the outbound policy")

	// ErrRedirect is a refused redirect. Every client this package builds follows zero of them.
	ErrRedirect = errors.New("outbound redirect refused")

	// ErrBodyTooLarge is a response body that ran past the cap. It surfaces from Read, because that
	// is where the byte over the line arrives.
	ErrBodyTooLarge = errors.New("outbound response body exceeds the cap")
)

// BlockedError is one refusal: a stable class, and the address that caused it.
//
// Error() prints the CLASS ONLY. The address is a field rather than part of the message so that
// disclosing it is a decision somebody makes — slog at the call site, where the operator needs it —
// rather than a default that rides along into an HTTP response body.
type BlockedError struct {
	// Class is a short stable label: "private", "loopback", "link-local", "port", "scheme"…
	Class string

	// Addr is the address the dialer was about to connect to, or the zero Addr for a refusal
	// decided from the URL alone.
	Addr netip.Addr

	// Port is the port that was refused, or 0 when the port is not what refused it.
	Port uint16
}

func (e *BlockedError) Error() string { return ErrBlocked.Error() + ": " + e.Class }

// Unwrap makes errors.Is(err, ErrBlocked) the one question a caller has to ask.
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// blocked builds a refusal decided before there was an address to name.
func blocked(class string) error { return &BlockedError{Class: class} }

// Policy is the outbound network policy: everything a client is allowed to reach.
//
// The zero value is the shipped default — HTTPS only, ports 80 and 443, no private address, no
// redirect, a 1 MiB body and a 10 s deadline — so a caller that has no opinion gets the strict one.
// That is deliberate: a policy struct whose zero value permitted anything would make every future
// `safehttp.New(Policy{})` a hole.
type Policy struct {
	// AllowPrivate relaxes the LAN classes — loopback, RFC 1918, unique-local and CGNAT — and is
	// DKP_OUTBOUND_ALLOW_PRIVATE. It does NOT relax link-local: 169.254.169.254 is the cloud
	// metadata endpoint, and no deployment reaches it on purpose through this package.
	AllowPrivate bool

	// AllowCIDRs is the narrow form the design prefers: an explicit list of prefixes that are
	// permitted whatever class they fall in. DKP_OUTBOUND_ALLOW_CIDRS.
	AllowCIDRs []netip.Prefix

	// AllowHTTPHosts are the hosts plain http:// may be used for. Everything else is https-only.
	// Compared case-insensitively, with a trailing root dot ignored.
	AllowHTTPHosts []string

	// AllowPorts are ports permitted in addition to 80 and 443.
	AllowPorts []uint16

	// MaxBodyBytes caps the response body; 0 means DefaultMaxBodyBytes.
	MaxBodyBytes int64

	// Timeout bounds the whole request; 0 means DefaultTimeout.
	Timeout time.Duration

	// ConnectTimeout bounds one dial; 0 means DefaultConnectTimeout.
	ConnectTimeout time.Duration

	// loopbackOnly inverts the policy: ONLY loopback, any port, http permitted. It is unexported
	// and set by [NewLoopback] alone, because it is not a relaxation of the outbound policy but a
	// different question — "may this process talk to itself" — and the two must not be spelled
	// alike. See NewLoopback for why the container healthcheck needs it.
	loopbackOnly bool
}

// ParsePolicy resolves the two configured strings — DKP_OUTBOUND_ALLOW_PRIVATE and
// DKP_OUTBOUND_ALLOW_CIDRS, read in cmd/dkp — into a policy.
//
// Empty is not an error for either: unset is the default and the default is the strict one. Anything
// unrecognised returns the STRICT policy with an error, so a caller that logs and continues fails
// closed and a caller that ignores the error entirely still does. That is the shape
// api.ParseReadyDetailPolicy already uses for the same reason.
func ParsePolicy(allowPrivate, allowCIDRs string) (Policy, error) {
	var p Policy

	switch strings.ToLower(strings.TrimSpace(allowPrivate)) {
	case "", "false":
	case "true":
		p.AllowPrivate = true
	default:
		return Policy{}, fmt.Errorf("parse %s: %q is not a boolean", AllowPrivateEnv, allowPrivate)
	}

	for _, field := range strings.Split(allowCIDRs, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			return Policy{}, fmt.Errorf("parse %s entry %q: %w", AllowCIDRsEnv, field, err)
		}

		// A zero-bit prefix permits the entire address space, which is not an allowlist — it is the
		// gate turned off, spelled in a way that looks like configuration. The design asks for "a
		// narrow CIDR allowlist"; refusing the widest possible entry is the one place that phrase
		// can be enforced rather than hoped for.
		if prefix.Bits() == 0 {
			return Policy{}, fmt.Errorf("parse %s entry %q: a /0 prefix allows every address, which is not an allowlist",
				AllowCIDRsEnv, field)
		}

		p.AllowCIDRs = append(p.AllowCIDRs, prefix.Masked())
	}

	return p, nil
}

// normalised returns the policy with its zero-valued bounds filled in.
func (p Policy) normalised() Policy {
	if p.Timeout <= 0 {
		p.Timeout = DefaultTimeout
	}

	if p.ConnectTimeout <= 0 {
		p.ConnectTimeout = DefaultConnectTimeout
	}

	if p.MaxBodyBytes <= 0 {
		p.MaxBodyBytes = DefaultMaxBodyBytes
	}

	return p
}

// checkURL is the cheap half: scheme, credentials and port, decided before any DNS happens.
//
// It is NOT the SSRF control — [Policy.control] is, because a hostname says nothing about where a
// socket ends up. This exists so that a plainly wrong URL fails without a lookup, and so that
// `http://` and `ftp://` are refused by the same policy value that refuses the address.
func (p Policy) checkURL(u *url.URL) error {
	if u == nil || u.Host == "" {
		return blocked(classHost)
	}

	// A URL may not carry credentials. `https://api.example.com@10.0.0.5/` is the oldest filter
	// bypass there is, and no outbound surface here has a use for HTTP basic credentials in a URL.
	if u.User != nil {
		return blocked(classUserinfo)
	}

	switch scheme := strings.ToLower(u.Scheme); scheme {
	case "https":
	case "http":
		if !p.httpAllowed(u.Hostname()) {
			return blocked(classScheme)
		}
	default:
		return blocked(classScheme)
	}

	port := u.Port()
	if port == "" {
		return nil // The scheme's default port, which is 80 or 443, both of which are allowed.
	}

	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return blocked(classPort)
	}

	return p.checkPort(uint16(n))
}

// httpAllowed reports whether plain http may be used for this host.
func (p Policy) httpAllowed(host string) bool {
	if p.loopbackOnly {
		// Probing this process is http by definition — the server inside the container terminates
		// no TLS. The dialer still refuses everything that is not loopback, which is the control
		// that matters here.
		return true
	}

	host = canonicalHost(host)

	return slices.ContainsFunc(p.AllowHTTPHosts, func(allowed string) bool {
		return canonicalHost(allowed) == host
	})
}

// canonicalHost lowercases a host and drops the root dot, so that `Example.COM`, `example.com` and
// `example.com.` are one host rather than three.
func canonicalHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// checkPort applies the port allowlist: 80 and 443, plus whatever the caller declared.
func (p Policy) checkPort(port uint16) error {
	if port == 0 {
		return &BlockedError{Class: classPort, Port: port}
	}

	// A loopback-only client talks to this process, which binds whatever the operator configured —
	// :8080 by default. The port allowlist protects against reaching a service that was never meant
	// to be addressed; on loopback the address itself is already the whole answer.
	if p.loopbackOnly || port == 80 || port == 443 || slices.Contains(p.AllowPorts, port) {
		return nil
	}

	return &BlockedError{Class: classPort, Port: port}
}

// control is the net.Dialer.Control hook, and it is the SSRF control.
//
// It runs after the name has been resolved and after the socket exists, with the address the kernel
// is about to connect to — so a name that resolved to a public address a moment ago and resolves to
// 127.0.0.1 now is refused on the second resolution. Any design that resolves, checks, then hands
// the hostname back to http.Transport re-resolves between the check and the connect and is
// rebindable by construction (03-security.md §8.1).
func (p Policy) control(network, address string, _ syscall.RawConn) error {
	// The transport dials tcp4 or tcp6. Anything else — a unix socket, UDP — is not a shape this
	// package builds, so it is a bug or a bypass and either way not something to permit.
	if !strings.HasPrefix(network, "tcp") {
		return blocked(classNetwork)
	}

	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		// Fail closed. An address the dialer produced and this cannot parse is a shape nobody has
		// reasoned about, and permitting it would make the whole check advisory.
		return blocked(classAddress)
	}

	if err := p.checkPort(ap.Port()); err != nil {
		return err
	}

	return p.checkAddr(ap.Addr())
}

// checkAddr decides one address.
//
// The order is the whole rule, so read it as one: structural refusals first (nothing relaxes them),
// then the explicit CIDR allowlist, then link-local (which AllowPrivate deliberately does not
// reach), then the LAN classes, then the reserved space.
func (p Policy) checkAddr(addr netip.Addr) error {
	// A zone is only meaningful for a link-local address, which is denied below — and
	// netip.Prefix.Contains returns false for ANY zoned address, so an allowlist comparison against
	// one would silently answer "not in the list" for the wrong reason.
	addr = addr.WithZone("")

	if !addr.IsValid() {
		return &BlockedError{Class: classAddress, Addr: addr}
	}

	// An IPv4-mapped IPv6 literal is never how a resolver hands back an A record — net.IP.String
	// prints those in dotted quad — so one arriving here was typed by somebody, in the shape that
	// exists to make an address look unlike itself.
	if addr.Is4In6() {
		return &BlockedError{Class: classMappedIPv4, Addr: addr}
	}

	// Structural: not a unicast peer at all. No allowlist reaches these, because there is no
	// deployment in which an HTTP client connecting to 0.0.0.0 or 224.0.0.1 is the intent.
	switch {
	case addr.IsUnspecified():
		return &BlockedError{Class: classUnspecified, Addr: addr}
	case addr.IsMulticast(), addr.IsInterfaceLocalMulticast():
		return &BlockedError{Class: classMulticast, Addr: addr}
	}

	// The explicit allowlist. It is checked BEFORE the class rules because it is the narrow,
	// audited, step-up-gated form the design asks an operator to use for a genuinely internal
	// endpoint — including, if they type it themselves, one this package would otherwise refuse.
	if slices.ContainsFunc(p.AllowCIDRs, func(prefix netip.Prefix) bool { return prefix.Contains(addr) }) {
		return nil
	}

	if p.loopbackOnly {
		if addr.IsLoopback() {
			return nil
		}

		return &BlockedError{Class: classNotLoopback, Addr: addr}
	}

	// Link-local, and NOT relaxed by AllowPrivate. 169.254.169.254 is the cloud metadata service —
	// the single highest-value SSRF target there is — and "allow my LAN" is never a request to
	// reach it. An operator who genuinely means it has DKP_OUTBOUND_ALLOW_CIDRS and has to type the
	// address.
	if addr.IsLinkLocalUnicast() {
		return &BlockedError{Class: classLinkLocal, Addr: addr}
	}

	if addr.IsLoopback() {
		if p.AllowPrivate {
			return nil
		}

		return &BlockedError{Class: classLoopback, Addr: addr}
	}

	// RFC 1918 and RFC 4193 unique-local, plus CGNAT — 100.64/10 is where a Tailscale peer lives,
	// which on a self-hosted box is as much "the LAN" as 192.168/16 is.
	if addr.IsPrivate() || cgnat.Contains(addr) {
		if p.AllowPrivate {
			return nil
		}

		return &BlockedError{Class: classPrivate, Addr: addr}
	}

	if slices.ContainsFunc(reserved, func(prefix netip.Prefix) bool { return prefix.Contains(addr) }) {
		return &BlockedError{Class: classReserved, Addr: addr}
	}

	return nil
}

// cgnat is RFC 6598 shared address space. netip.Addr has no classifier for it.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// reserved is the address space that is neither a LAN nor the public internet: ranges that are
// unroutable, or that carry an IPv4 destination inside an IPv6 address.
//
// The last four are the reason this list is longer than the table in 03-security.md §8.1. Teredo,
// 6to4 and both NAT64 forms embed an IPv4 address in an IPv6 one, so on a host with any of those
// configured they are a route to 127.0.0.1 or to the LAN that never looks like one. Refusing them
// is strictly stricter than the design's table, not a departure from it; ADR-0029 records the
// addition so that a future reader does not "tidy" the list back down to the table.
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network" (RFC 1122) — never a routable peer
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments: DS-Lite, NAT64 discovery
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, and 255.255.255.255 with it
	netip.MustParsePrefix("::/96"),          // deprecated IPv4-compatible IPv6
	netip.MustParsePrefix("100::/64"),       // discard-only (RFC 6666)
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 (RFC 6052)
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64 (RFC 8215)
	netip.MustParsePrefix("2001::/32"),      // Teredo — an IPv4 address and port inside an IPv6 one
	netip.MustParsePrefix("2002::/16"),      // 6to4 — likewise
}
