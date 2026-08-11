package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"reflect"
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

// readyUnwired is the response when the endpoint has no usable checker. It is not the same thing as
// "ready": a mux built that way is a programming error in cmd/, and reporting ready would hide it
// behind a green load-balancer probe.
//
// Two shapes reach it, and #75 is that only one of them used to. A nil ReadyChecker at the handler is
// the obvious one. The one that matters is a non-nil interface holding a NIL IMPLEMENTATION —
// Config{Readiness: (*someChecker)(nil)} — which is not `== nil`, survives a compile, passes New's
// registration guard, and used to call Ready() on a nil receiver: a panic into the Problem
// middleware's recovery, i.e. a 500 at 3am where a 503 naming the wiring bug was available.
// checkerUnwired is what makes this value reachable by the shape this paragraph describes.
//
// Config.Readiness == nil still does not reach here — New omits the route entirely in that case,
// preserving the split PR 3 introduced. That asymmetry is deliberate: "this binary does not do
// readiness" is a configuration, and 404 says so; "somebody wired a checker and it was nil" is a bug,
// and 503 with a reason is how a bug should sound.
var readyUnwired = ReadyReport{
	Check:  "migrations",
	State:  ReadyStateFailed,
	Detail: "no readiness checker configured",
}

// checkerUnwired reports whether checker is no checker at all: a nil interface, or a non-nil
// interface holding a nil implementation.
//
// The second half is why this exists rather than a plain `checker != nil` (#75). An interface value
// carries a type and a value, so an interface holding a typed nil is itself non-nil — the single most
// reliably surprising thing in the language, and the shape a pointer-receiver checker wired from cmd/
// produces the moment its constructor returns a nil pointer with a nil error.
//
// It costs one reflect.ValueOf per readiness probe, which is affordable at probe frequency and buys a
// 503 that names the fault instead of a panic recovered into a 500. Kinds that cannot be nil — a
// struct value like cmd/dkp's `readiness`, which is the wiring in production — take the default
// branch and are treated as wired, because they are: a value-type checker with a nil field inside is
// that checker's business, and cmd/dkp's Ready answers "no database configured" for exactly that.
func checkerUnwired(checker ReadyChecker) bool {
	if checker == nil {
		return true
	}

	value := reflect.ValueOf(checker)

	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return value.IsNil()
	default:
		return false
	}
}

// ReadyDetailPolicy decides who, if anyone, is shown a /readyz `detail`.
//
// It exists because the peer address is not a trust signal (#74). The redaction that shipped with #59
// released a detail to a loopback or RFC-1918 `RemoteAddr`, which is correct only for a directly
// exposed binary — and the deployment this project recommends is the binary behind a reverse proxy,
// where every caller in the world arrives from 127.0.0.1 or a bridge address. For the majority of real
// installs that made the detail effectively public, including the one naming which append-only
// guarantee a guild's ledger is currently missing.
//
// So disclosure now requires an EXPLICIT signal from the operator and is off until it gets one.
// `RemoteAddr` never grants disclosure by itself; under ReadyDetailLocal it can only narrow what the
// operator already opted into. The other explicit signal an endpoint could use — an authenticated,
// authorized caller — is not available yet: there is no auth package before ROADMAP Phase 2, and
// /readyz is a probe that a load balancer calls unauthenticated by design. When it exists, it belongs
// here as a fourth answer and not as a loosening of these three.
type ReadyDetailPolicy string

const (
	// ReadyDetailNever withholds every detail from every caller, and is the default — including the
	// zero value, so a Config that never mentions this field discloses nothing.
	//
	// The operator's route to the detail is the process log (the boot path logs the same fault at
	// error level), or turning one of the two policies below on deliberately. `check` and `state` are
	// public in every policy, so monitoring still sees THAT something is wrong; what an unasked-for
	// caller does not get is WHICH thing and its shape.
	ReadyDetailNever ReadyDetailPolicy = "never"

	// ReadyDetailLocal is the pre-#74 rule, now opt-in: the peer is loopback or private space AND
	// nothing in the request says the peer is relaying somebody else.
	//
	// Setting it is the operator asserting a fact about their deployment that this process cannot
	// check — that nothing sits in front of the listener, so the peer address means what it says. It
	// is the right answer for a directly exposed binary (`docker run -p 8080:8080`), which is the
	// shape the address test was always a real control for. It is the WRONG answer in front of a
	// layer-4 proxy or a layer-7 one that strips its own forwarded headers, because neither is
	// visible from in here; see viaProxy.
	ReadyDetailLocal ReadyDetailPolicy = "local"

	// ReadyDetailAlways discloses the detail to every caller, and is how an operator behind a reverse
	// proxy gets it back.
	//
	// The fail-closed default costs the legitimate case — monitoring that genuinely is on the local
	// network but reaches the instance through the proxy — and this is the deliberate, documented way
	// to pay for it. Set it only where /readyz is not reachable from the public internet: on a
	// published instance it puts the names of this guild's missing ledger protections in an
	// unauthenticated response.
	ReadyDetailAlways ReadyDetailPolicy = "always"
)

// ParseReadyDetailPolicy resolves a configured string — DKP_READYZ_DETAIL, read in cmd/dkp — to a
// policy.
//
// Empty is not an error: unset is the default and the default is ReadyDetailNever. Anything else
// unrecognised returns ReadyDetailNever WITH an error, so a caller that logs and continues fails
// closed and a caller that ignores the error entirely still does. Case and surrounding space are
// forgiven because `DKP_READYZ_DETAIL=Always ` in a compose file is a typo, not a policy.
func ParseReadyDetailPolicy(value string) (ReadyDetailPolicy, error) {
	switch policy := ReadyDetailPolicy(strings.ToLower(strings.TrimSpace(value))); policy {
	case "", ReadyDetailNever:
		return ReadyDetailNever, nil
	case ReadyDetailLocal, ReadyDetailAlways:
		return policy, nil
	default:
		return ReadyDetailNever, fmt.Errorf("unknown readiness detail policy %q: want %q, %q or %q",
			value, ReadyDetailNever, ReadyDetailLocal, ReadyDetailAlways)
	}
}

// disclosesTo reports whether this policy releases a detail to this request.
//
// Every branch that returns true names an explicit operator decision. `local` reaches for the peer
// address only INSIDE that decision, where it subtracts from what was opted into; there is no path
// from an address alone to a disclosure, which is the whole of #74.
func (p ReadyDetailPolicy) disclosesTo(r *http.Request) bool {
	switch p {
	case ReadyDetailAlways:
		return true
	case ReadyDetailLocal:
		return !viaProxy(r.Header) && localCaller(r.RemoteAddr)
	case ReadyDetailNever:
		return false
	default:
		// The zero value, and any policy a future edit adds without deciding about it here. Fail
		// closed: a disclosure control whose default branch discloses is not a control.
		return false
	}
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
// docs/design/06-cicd-and-release.md §"Health endpoints" requires /readyz to disclose a detail only
// where the operator has said so; this body is its one stated exception, and that document is updated
// in the same change rather than left contradicting the code. It tells an unauthenticated caller only
// that the instance is mid-upgrade, which the 503 already tells them, and the command it names is
// published documentation. The SPA renders it as a banner for an operator who may not have shell
// access at that moment.
//
// Every OTHER check's detail is withheld unless the operator has explicitly asked for it to be
// disclosed — ReadyDetailPolicy, from DKP_READYZ_DETAIL — and the default is that nobody sees it. That
// is the obligation the paragraph above handed to whoever added the first such check, corrected in
// #74 for the deployment this project recommends: behind a reverse proxy the peer address is
// 127.0.0.1 for every caller alive, so a rule that read it was disclosing to the public internet on
// the majority of real installs. The append-only check (#59) is the sharpest example of why that
// matters — its detail names which of the guarantees on this guild's ledger is currently missing,
// which is reconnaissance handed to precisely the person who would use it.
//
// `state` and `check` stay public in every case. An operator's monitoring has to see that something is
// wrong, and the 503 says that much anyway; what an unasked-for caller does not get is WHICH thing and
// its shape.
func handleReadyz(w http.ResponseWriter, r *http.Request, checker ReadyChecker, detail ReadyDetailPolicy) {
	report := readyUnwired
	if !checkerUnwired(checker) {
		report = checker.Ready()
	}

	// Command survives the redaction: the migrations-pending body is the documented public exception,
	// and the SPA renders it for an operator who may have no shell access at that moment.
	if !detail.disclosesTo(r) {
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
// Since #74 it is reached only from ReadyDetailLocal, where the operator has asserted that nothing
// sits in front of this listener. That makes it a check on the assertion rather than the whole
// control — if a proxy announces itself, the assertion was wrong and the detail stays withheld — and
// it is why the residual gap below is now a documented cost of one opt-in policy rather than the
// default behaviour of the endpoint.
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
// configured to add nothing, is invisible here. An operator in that shape who sets ReadyDetailLocal
// has asserted something untrue and will disclose the detail through their proxy — which is why the
// default is ReadyDetailNever and why the documentation for `local` names the deployment it is for.
// Recovering the real client instead (a DKP_TRUSTED_PROXIES CIDR set, a right-to-left X-Forwarded-For
// walk, PROXY-protocol support on the listener) is the shared resolved-client-IP helper that rate
// limiting, the audit log's actor IP and session binding each need too; it is deliberately not
// invented here for one call site, and is filed as #98.
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
// docs/design/06-cicd-and-release.md §"Health endpoints" permits to see a /readyz detail UNDER
// ReadyDetailLocal: loopback, or private space — RFC 1918 and its IPv6 counterpart RFC 4193, which
// that document names explicitly because an IPv6-only deployment has no other private range to be
// reached on.
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
// together, inside ReadyDetailLocal and nowhere else; neither is sufficient, and since #74 neither is
// consulted at all until an operator has opted in. Do not add a forwarded-address lookup here:
// reading a client-supplied header to GRANT disclosure is the failure viaProxy's comment describes.
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
