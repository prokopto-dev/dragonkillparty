package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
)

// The resolved client IP: ONE answer to "who is the client, really", computed once per request and
// read by everything that needs it (issue #98, docs/design/03-security.md §8.3).
//
// It exists because RemoteAddr answers a different question — "who opened this socket" — and behind
// the reverse proxy this project recommends the two are never the same. A rate limit keyed on the
// proxy's address limits the guild rather than the attacker; an audit row carrying it records the
// proxy rather than the officer. The forwarded headers carry the real answer, and they are
// client-supplied strings, which is why believing one is a configuration decision and not a default.
//
// THE DEFAULT IS ZERO TRUST. With DKP_TRUSTED_PROXIES empty every forwarded header is ignored
// entirely and the client IP is the socket's peer address. §8.3 is explicit about why: a spoofed
// X-Forwarded-For gives an attacker unlimited distinct "IPs" against a per-IP rate limit, and lets
// them attribute their actions to an officer's home address in the audit log. Both are silent, and a
// permissive default here is the kind of thing that ships in v1 and is never revisited.

const (
	// HeaderForwardedFor is the de-facto forwarded chain, spelled as it appears on the wire. It is
	// the ONLY header this package reads for the client address — see TrustedProxies.Resolve for why
	// X-Real-IP and RFC 7239 Forwarded are not.
	HeaderForwardedFor = "X-Forwarded-For"

	// maxForwardedHops bounds the right-to-left walk.
	//
	// The chain is attacker-influenced: whatever the client sent arrives at the left of whatever the
	// proxies appended, so its LENGTH is chosen by the caller. Real chains are one to three hops —
	// nginx, a tunnel, a CDN — and a bound two orders of magnitude above that costs nothing to a
	// legitimate deployment while turning "send a megabyte of trusted-looking addresses and make the
	// server evaluate every one of them against every configured CIDR" into a fixed cost.
	//
	// Reaching the bound behaves exactly like reaching an unparseable entry: the walk stops and the
	// innermost address it did verify is the answer. It never falls back to the leftmost entry, which
	// is the one an attacker writes.
	maxForwardedHops = 64
)

// forwardedHeaders is the family that means "something in front of this listener rewrote the
// request". §8.3 names these four; they are the ones documented to operators as ignored while the
// trust list is empty, and any of them arriving is the same misconfiguration.
//
// Local rather than package-level: a package-level slice is mutable state any caller can append to,
// and -shuffle=on plus t.Parallel() turn that into an intermittent failure somewhere else.
func forwardedHeaders() []string {
	return []string{HeaderForwardedFor, "X-Forwarded-Proto", "X-Real-IP", "Forwarded"}
}

// TrustedProxies is the parsed DKP_TRUSTED_PROXIES list: the CIDRs whose forwarded headers this
// process believes.
//
// THE ZERO VALUE TRUSTS NOTHING, and that is the whole safety argument. A Config that never mentions
// this field, a test that constructs a server without one, an embedder that forgets — all of them get
// the socket's peer address and no header parsing at all. There is no spelling of "unset" that means
// "trust everything".
type TrustedProxies struct {
	// prefixes is never mutated after ParseTrustedProxies returns, so a TrustedProxies value is safe
	// to copy and to share across goroutines.
	prefixes []netip.Prefix
}

// ParseTrustedProxies parses DKP_TRUSTED_PROXIES: a comma-separated list of CIDRs.
//
// EMPTY IS NOT AN ERROR — it is the documented default, and it means trust nothing. A malformed
// entry IS an error, and the caller is expected to make it fatal: issue #98 asks for the parse to
// "fail loudly rather than silently trusting nothing" because the two failures look identical from
// the outside and only one of them is the operator's intent. An instance that silently trusted
// nothing after a typo would put every client behind the proxy into one shared rate-limit bucket and
// stamp the proxy's address on every session, with nothing in the logs to say why.
//
// A BARE IP IS ACCEPTED as its own single-host prefix. Operators write `127.0.0.1` far more often
// than `127.0.0.1/32`, the meaning is unambiguous, and refusing it would be a boot failure over
// punctuation. A prefix that is not already masked (`10.1.2.3/8`) is masked rather than refused, for
// the same reason.
func ParseTrustedProxies(spec string) (TrustedProxies, error) {
	var prefixes []netip.Prefix

	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			// A trailing comma, or a variable set to whitespace. Neither is a statement about trust.
			continue
		}

		prefix, err := parseTrustedEntry(entry)
		if err != nil {
			return TrustedProxies{}, err
		}

		prefixes = append(prefixes, prefix)
	}

	return TrustedProxies{prefixes: prefixes}, nil
}

// parseTrustedEntry turns one list entry into a prefix.
func parseTrustedEntry(entry string) (netip.Prefix, error) {
	if prefix, err := netip.ParsePrefix(entry); err == nil {
		// An IPv4-mapped IPv6 prefix (::ffff:10.0.0.0/104) is refused rather than normalised. Every
		// address this package compares is unmapped first, so such a prefix would match nothing at
		// all — a trust list that is syntactically valid, silently inert, and indistinguishable from
		// a working one until somebody spoofs past it.
		if prefix.Addr().Is4In6() {
			return netip.Prefix{}, fmt.Errorf(
				"trusted proxy %q is an IPv4-mapped IPv6 prefix; write it as an IPv4 CIDR", entry)
		}

		return prefix.Masked(), nil
	}

	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf(
			"trusted proxy %q is neither a CIDR (10.0.0.0/8) nor an IP address (127.0.0.1)", entry)
	}

	addr = normaliseAddr(addr)

	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// Empty reports whether nothing is trusted — the default, in which no forwarded header is read.
func (t TrustedProxies) Empty() bool { return len(t.prefixes) == 0 }

// String renders the list for a log line. It carries no secret: these are the operator's own CIDRs.
func (t TrustedProxies) String() string {
	if t.Empty() {
		return "(none)"
	}

	out := make([]string, 0, len(t.prefixes))
	for _, prefix := range t.prefixes {
		out = append(out, prefix.String())
	}

	return strings.Join(out, ",")
}

// Trusts reports whether addr is one of the configured proxies.
func (t TrustedProxies) Trusts(addr netip.Addr) bool {
	addr = normaliseAddr(addr)
	if !addr.IsValid() {
		return false
	}

	for _, prefix := range t.prefixes {
		// Prefix.Contains is false across address families, so an IPv4 CIDR never matches an IPv6
		// peer by accident. Both sides are unmapped, which is what makes ::ffff:10.0.0.1 match
		// 10.0.0.0/8 rather than slipping past it.
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

// Resolve answers "who is the client, really" for one request.
//
// THE ALGORITHM IS §8.3's, and every clause of it is load-bearing:
//
//  1. With the trust list EMPTY, the answer is the peer address and no header is read. Not "read it
//     if it looks reasonable" — read at all.
//  2. If the PEER is not itself trusted, the answer is the peer address and no header is read,
//     whatever the list says. A forwarded header from a direct caller is a claim by the caller.
//  3. Otherwise walk X-Forwarded-For RIGHT TO LEFT, popping entries that are themselves trusted
//     proxies. THE FIRST UNTRUSTED ENTRY IS THE CLIENT.
//
// NEVER THE LEFTMOST ENTRY. That is the trap this function exists to avoid: the leftmost entry is
// whatever the original caller put in the header before any proxy touched it, so a limiter or an
// audit log keyed on it is keyed on a string the attacker chose. Right-to-left is the only direction
// in which each entry was written by something we have decided to believe.
//
// AN UNREADABLE ENTRY STOPS THE WALK and the innermost address already verified is the answer. That
// case is only reachable when the forged entry is itself inside a trusted CIDR — otherwise the walk
// has already stopped at the real client to its right — so stopping yields the closest verified hop
// rather than the caller's string. Running out of entries has the same shape: every hop was trusted,
// so the leftmost is a trusted address rather than an attacker's, and it is the answer.
//
// X-REAL-IP AND RFC 7239 Forwarded ARE NOT READ, in any configuration, and that is a deliberate
// narrowing of what the operator-facing documentation implied. Neither carries a chain, so neither
// can be walked: honouring X-Real-IP means believing one header completely, which re-opens the
// leftmost-entry trap by another name whenever the trust list is wider than the actual proxies. The
// two are still IGNORED rather than rejected — a proxy that sets X-Real-IP alongside
// X-Forwarded-For, which is the nginx default, keeps working. See docs/operations/configuration.md.
//
// The returned Addr is INVALID when RemoteAddr cannot be parsed at all — a Unix-socket listener, a
// hand-built request in a test. Callers must handle that; it is never treated as a match for a
// trusted CIDR.
func (t TrustedProxies) Resolve(r *http.Request) netip.Addr {
	peer := PeerIP(r)

	if t.Empty() || !t.Trusts(peer) {
		return peer
	}

	client := peer
	entries := forwardedForEntries(r.Header)

	for i, hops := len(entries)-1, 0; i >= 0 && hops < maxForwardedHops; i, hops = i-1, hops+1 {
		addr, ok := parseForwardedAddr(entries[i])
		if !ok {
			break
		}

		client = addr

		if !t.Trusts(addr) {
			break
		}
	}

	return client
}

// PeerIP is the socket's remote address: the one thing about a request that no header can change.
//
// It is the resolver's floor — what Resolve returns whenever it declines to read a header — and it
// is exported because a consumer that runs where the ClientIP middleware was not mounted needs a
// safe fallback rather than a zero value it will silently key a limiter on.
func PeerIP(r *http.Request) netip.Addr {
	host := r.RemoteAddr

	// net/http always presents host:port for a TCP listener; SplitHostPort failing means something
	// else is on the other end (a Unix socket presents "@"), and the whole string is the best guess.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}

	return normaliseAddr(addr)
}

// forwardedForEntries flattens every X-Forwarded-For field line into one ordered list.
//
// Multiple field lines are equivalent to one comma-joined line in field order (RFC 9110 §5.3), and a
// proxy chain routinely produces both spellings, so reading only Header.Get — the first line — would
// silently drop every hop a second proxy appended.
func forwardedForEntries(h http.Header) []string {
	var out []string

	for _, line := range h.Values(HeaderForwardedFor) {
		out = append(out, strings.Split(line, ",")...)
	}

	return out
}

// parseForwardedAddr reads one X-Forwarded-For entry, reporting false for anything that is not an
// address.
//
// It accepts the four spellings that appear in the wild — a bare address, a bracketed IPv6 literal,
// and either of those with the source port appended, which some appliances do — and rejects
// everything else, including RFC 7239's obfuscated identifiers ("_hidden", "unknown"). Rejection is
// not an error: it stops the walk, which is the safe direction.
func parseForwardedAddr(entry string) (netip.Addr, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return netip.Addr{}, false
	}

	if addr, err := netip.ParseAddr(strings.Trim(entry, "[]")); err == nil {
		return normaliseAddr(addr), true
	}

	if hostPort, err := netip.ParseAddrPort(entry); err == nil {
		return normaliseAddr(hostPort.Addr()), true
	}

	return netip.Addr{}, false
}

// normaliseAddr puts an address into the one form everything here compares and keys on.
//
// TWO NORMALISATIONS, EACH CLOSING A REAL HOLE:
//
//   - Unmap. ::ffff:192.0.2.1 and 192.0.2.1 are the same host, and a dual-stack listener presents the
//     first spelling for an IPv4 client. Without this, that client matches no IPv4 CIDR in the trust
//     list and gets a rate-limit bucket of its own for each spelling.
//   - Zone. fe80::1%eth0 names an interface on the machine that wrote it, which is meaningless here
//     and is attacker-controlled when it arrives in a header — an unbounded string that would
//     otherwise become part of a map key.
func normaliseAddr(addr netip.Addr) netip.Addr { return addr.Unmap().WithZone("") }

// clientIPKey types the context key. An unexported struct{} cannot collide with a key set by another
// package, which a string constant can.
type clientIPKey struct{}

// ClientIP resolves the request's client address once and puts it on the context.
//
// ONE RESOLUTION PER REQUEST, read by everything downstream, is the point (issue #98). Four call
// sites need this answer — rate limiting, the audit log's actor IP, session binding and the /readyz
// disclosure policy — and each one that computed it itself would get it wrong differently.
//
// It is an http.Handler wrapper rather than a Huma middleware because the question has nothing to do
// with which operation was matched, and because /healthz, /readyz and the SPA are raw handlers Huma
// never sees.
//
// THE WARNING IS ONE-TIME, per §8.3, and that is a DoS control rather than tidiness: the condition is
// "a request carrying a forwarded header", which is a thing any caller can produce on demand, so a
// per-request log line would be a log-flooding primitive handed to the internet.
func ClientIP(trusted TrustedProxies, next http.Handler) http.Handler {
	// Scoped to the mount, not to the package: package-level state would make the warning fire once
	// per process across every test in the package, which is both a shuffled-order flake and a
	// silence exactly where a test wants to observe it.
	var warnOnce sync.Once

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if trusted.Empty() && hasForwardedHeader(r.Header) {
			warnOnce.Do(func() {
				Logger(r.Context()).WarnContext(r.Context(),
					"a forwarded header arrived while DKP_TRUSTED_PROXIES is empty; ignoring it and "+
						"using the socket's peer address as the client IP",
					"env", "DKP_TRUSTED_PROXIES",
					"fix", "set it to your proxy's CIDR (DKP_TRUSTED_PROXIES=127.0.0.1/32) if this "+
						"instance is behind one; leave it empty if it is not",
					"cost_if_ignored", "every client shares one rate-limit bucket and one recorded address",
					"logged", "once per process")
			})
		}

		ctx := context.WithValue(r.Context(), clientIPKey{}, trusted.Resolve(r))

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClientIPFromContext returns the address ClientIP resolved.
//
// The bool is false outside a request, or when the middleware was not mounted. Callers must not
// treat that as an address: keying a rate limiter on a zero Addr would put every caller into one
// bucket, which is a denial of service delivered by a wiring bug. Fall back to PeerIP instead.
func ClientIPFromContext(ctx context.Context) (netip.Addr, bool) {
	addr, ok := ctx.Value(clientIPKey{}).(netip.Addr)

	return addr, ok && addr.IsValid()
}

// hasForwardedHeader reports whether the request carries any of the headers §8.3 names.
//
// PRESENCE, not contents — Header.Get returns "" for a header that is present and empty, so a bare
// `X-Forwarded-For:` would read as "no proxy here" through Get. Values canonicalises the lookup key,
// which is what a request off the wire carries.
func hasForwardedHeader(h http.Header) bool {
	for _, name := range forwardedHeaders() {
		if len(h.Values(name)) > 0 {
			return true
		}
	}

	return false
}
