package api

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// ReadyState is what /readyz reports about one check.
type ReadyState string

const (
	// ReadyStateReady — the check passed.
	ReadyStateReady ReadyState = "ready"
	// ReadyStatePending — migrations are waiting and DKP_AUTO_MIGRATE is off.
	ReadyStatePending ReadyState = "pending"
	// ReadyStateDegraded — the check found real damage that only a human can repair.
	//
	// Distinct from failed, which means the check could not be evaluated. This state is an evaluated
	// check with a bad answer, and one that will keep having that answer for as long as nobody acts:
	// the append-only protection on the ledger is the first of them (#59). It answers 503 like every
	// other non-ready state, because a probe that returns 200 with the bad news in the body is a probe
	// whose bad news nobody's monitoring reads — which is the entire failure this state exists to fix.
	ReadyStateDegraded ReadyState = "degraded"
	// ReadyStateFailed — the check could not be evaluated at all.
	ReadyStateFailed ReadyState = "failed"
)

// ReadyReport is the body of GET /readyz.
//
// The field names and the migrations-pending values are a contract, not an implementation detail:
// docs/development/first-ten-prs.md:167 and docs/design/06-cicd-and-release.md:523 both specify the
// exact body {"check":"migrations","state":"pending","command":"dkp migrate"}, and the SPA renders
// a banner containing that command verbatim. Changing a key here changes what an operator is told
// to type.
//
// It reports ONE check — the first one that is not ready, or the migrations check when everything
// passed. That is the shape the contract above fixes, so a second check does not widen the envelope
// into a `checks[]` array; it joins an ordered ladder in the adapter (cmd/dkp/serve.go), with
// migrations first precisely so the body above is what a pending instance still answers.
type ReadyReport struct {
	Check   string     `json:"check"`
	State   ReadyState `json:"state"`
	Command string     `json:"command,omitempty"`
	Detail  string     `json:"detail,omitempty"`
}

// ReadyChecker reports whether the instance is ready to serve.
//
// Declared here, by the consumer, and satisfied by internal/migrate through the adapter in cmd/dkp —
// so internal/api does not import the migrator and the migrator does not import the API. It stays one
// method as the checks multiply: the endpoint reports the first check that is not ready, so what the
// API needs is the answer and not the list. The worker heartbeat, free disk and outbox lag that
// canonical §13 also names arrive with the code that can fail them.
type ReadyChecker interface {
	Ready() ReadyReport
}

// readyUnwired is the response when a checker was registered but is nil. It is not the same thing
// as "ready": a mux built that way is a programming error in cmd/, and reporting ready would hide it
// behind a green load-balancer probe.
//
// Note that Config.Readiness == nil does not reach here — New omits the route entirely in that case,
// preserving the split PR 3 introduced. This value covers a non-nil interface holding a nil
// implementation, which is the shape that survives a compile and fails at 3am.
var readyUnwired = ReadyReport{
	Check:  "migrations",
	State:  ReadyStateFailed,
	Detail: "no readiness checker configured",
}

// handleReadyz answers the readiness probe.
//
// 503 whenever a check is not ready — migrations pending, the ledger's append-only protection gone, a
// check that could not be evaluated — 200 when every check passed, and, this is the part that matters,
// /healthz continues to answer 200 throughout. Canonical §13 splits them precisely so that a
// database problem never lets Docker's HEALTHCHECK kill the container, which during a migration is
// how a guild loses its ledger. A load balancer stops sending traffic; the supervisor does not stop
// the process.
//
// The migrations-pending body is PUBLIC, and that is a decision rather than an oversight.
// docs/design/06-cicd-and-release.md §"Health endpoints" requires /readyz to disclose detail only to
// loopback and RFC-1918 callers; this body is its one stated exception, and that document was
// updated in the same change rather than left contradicting the code. It tells an unauthenticated
// caller only that the instance is mid-upgrade, which the 503 already tells them, and the command it
// names is published documentation. The SPA renders it as a banner for an operator who may not have
// shell access at that moment.
//
// Every OTHER check's detail is redacted for a caller that is not on the local network, which is the
// obligation the paragraph above handed to whoever added the first such check. That is now the
// ledger's append-only protection (#59), and it is the sharpest possible example of why the rule
// exists: its detail names which of the guarantees on this guild's ledger is currently missing, which
// is reconnaissance handed to precisely the person who would use it. `state` and `check` stay public
// in every case — an operator's monitoring has to see that something is wrong, and the 503 says that
// much anyway; what a stranger does not get is WHICH thing and its shape.
func handleReadyz(w http.ResponseWriter, r *http.Request, checker ReadyChecker) {
	report := readyUnwired
	if checker != nil {
		report = checker.Ready()
	}

	// Two conditions, and BOTH have to hold for a detail to be released: the peer is on the local
	// network, AND nothing says the peer is relaying somebody else. A same-host reverse proxy — the
	// recommended deployment — makes every caller in the world present as 127.0.0.1, so the address
	// test alone is not a control there at all; it would pass the whole internet.
	//
	// Command survives either way: the migrations-pending body is the documented public exception, and
	// the SPA renders it for an operator who may have no shell access at that moment.
	if viaProxy(r.Header) || !localCaller(r.RemoteAddr) {
		report.Detail = ""
	}

	status := http.StatusOK
	if report.State != ReadyStateReady {
		status = http.StatusServiceUnavailable
	}

	body, err := json.Marshal(report)
	if err != nil {
		// Unreachable with a struct of strings, and handled anyway: a readiness probe that panics
		// takes the instance out of rotation in the least diagnosable way available.
		slog.ErrorContext(r.Context(), "marshal readiness report", "error", err)
		http.Error(w, `{"check":"migrations","state":"failed"}`, http.StatusServiceUnavailable)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// viaProxy reports whether anything in the request says it was relayed by a proxy.
//
// It exists because RemoteAddr answers "who connected", and the disclosure rule needs "who asked". A
// reverse proxy on the same host presents 127.0.0.1 for every caller alive, and one in a container or
// in front of a cluster presents an RFC-1918 address — so a public client routed through either would
// satisfy localCaller and be handed the names of the ledger protections this instance is missing. The
// address test is not a control in the deployment this project actually recommends unless something
// notices the relay.
//
// The test is the PRESENCE of a header key — whatever its value, and across the whole X-Forwarded-*
// family rather than three chosen members. Contents are never read, and that asymmetry is the entire
// safety argument: these headers are client-supplied, so believing one that says "the real client is
// 10.0.0.5" would let anybody unredact this endpoint with one curl flag, while using presence to
// REFUSE is the direction a forged header cannot exploit — the worst it achieves is a shorter
// response. An operator behind a proxy still gets the detail by asking the process directly rather
// than through the proxy, which is what somebody on the box is doing anyway.
//
// Both halves of that sentence are load-bearing, because the narrower readings each had a hole:
// Header.Get returns "" for a header that is present and empty, so `X-Forwarded-For:` with no value
// read as "no proxy here"; and a proxy setting only X-Forwarded-Port, or Traefik's X-Forwarded-Prefix,
// was invisible to a fixed list while still relaying the public internet from a local peer.
//
// The list is evidence, not authority, and not exhaustive: a layer-4 proxy, or a layer-7 one
// configured to add nothing, is invisible here. Closing that needs the trusted-proxy list
// (DKP_TRUSTED_PROXIES, already specified in docs/getting-started/install-docker.md) plus
// PROXY-protocol support, filed as #74 — and note that implementing it LOOSENS this function for a
// validated peer rather than tightening it, since recovering the real client is what lets a
// genuinely-local caller behind a proxy see the detail again. Until then this is what keeps the
// default safe.
func viaProxy(h http.Header) bool {
	// Local rather than package-level: a package-level slice is mutable state that any caller can
	// append to, and this one decides who sees a security-relevant string.
	//
	// Canonicalised on both sides. net/http canonicalises "CF-Connecting-IP" to "Cf-Connecting-Ip"
	// when it parses a request, so indexing the map with the vendor's own spelling would silently
	// never match.
	named := []string{
		http.CanonicalHeaderKey("Forwarded"),        // RFC 7239, the standard one
		http.CanonicalHeaderKey("X-Real-IP"),        // nginx's usual proxy_set_header
		http.CanonicalHeaderKey("CF-Connecting-IP"), // Cloudflare
		http.CanonicalHeaderKey("True-Client-IP"),   // Akamai, Cloudflare Enterprise
	}

	// One pass over what the request actually carries, canonicalising the REQUEST's key rather than
	// looking the canonical form up in the map. The difference is not academic: an http.Header is a
	// plain map, so a caller that populated it by hand can hold "cf-connecting-ip", and a lookup of
	// the canonical spelling would miss it while the same header off the wire matched. A disclosure
	// control must not depend on which door the header came through.
	for name := range h {
		canonical := http.CanonicalHeaderKey(name)

		// The whole X-Forwarded-* family by prefix, because the policy is the family and not a list of
		// the members somebody thought of: X-Forwarded-Port on its own is as good evidence of a relay
		// as X-Forwarded-For, and Traefik's X-Forwarded-Prefix or a proxy's X-Forwarded-Server are the
		// same fact again.
		if strings.HasPrefix(canonical, "X-Forwarded-") || slices.Contains(named, canonical) {
			return true
		}
	}

	return false
}

// localCaller reports whether remoteAddr is one of the callers
// docs/design/06-cicd-and-release.md §"Health endpoints" permits to see a /readyz detail: loopback, or
// private space — RFC 1918 and its IPv6 counterpart RFC 4193, which that document names explicitly
// because an IPv6-only deployment has no other private range to be reached on.
//
// Link-local is NOT on the list, and the omission is deliberate rather than an oversight in the
// standard library's vocabulary. 169.254/16 and fe80::/10 are reachable by anything sharing a layer-2
// segment — another tenant's VM on a cloud VLAN, a device on the guild's office wifi — which is not the
// "has shell access to the box, or is on the network the instance is managed from" audience this
// exception exists for. An earlier revision of this function admitted them and disclosed which of a
// guild's ledger protections was missing that little bit more widely than the contract allows.
//
// Anything unparseable is treated as PUBLIC. A Unix-socket peer, a proxy protocol string or an empty
// RemoteAddr all land there, and defaulting an unrecognised caller to "trusted" is the one direction
// that turns a parsing surprise into a disclosure.
//
// It answers only "is the PEER local", which on its own is not the question — a proxy peer is local
// while the person behind it is not. viaProxy is the other half of the test and the two are used
// together; neither is sufficient. Do not add a forwarded-address lookup here: reading a
// client-supplied header to GRANT disclosure is the failure viaProxy's comment describes.
func localCaller(remoteAddr string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}

	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}

	// ::ffff:10.0.0.1 is an RFC-1918 caller wearing an IPv6 hat; IsPrivate would otherwise say no.
	ip = ip.Unmap()

	// netip.Addr.IsPrivate is exactly RFC 1918 for IPv4 and RFC 4193 (fc00::/7) for IPv6 — the two
	// ranges the contract names — and nothing else. It does not include link-local, CGNAT (100.64/10)
	// or the documentation ranges, and each of those absences is wanted here.
	return ip.IsLoopback() || ip.IsPrivate()
}
