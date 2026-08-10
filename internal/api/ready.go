package api

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
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

	// Command survives: the migrations-pending body is the documented public exception, and the SPA
	// renders it for an operator who may have no shell access at that moment. Detail does not.
	if !localCaller(r.RemoteAddr) {
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
// It reads r.RemoteAddr and NOT X-Forwarded-For, deliberately: that header is client-supplied, so
// trusting it would let anyone unredact this endpoint with one curl flag. The cost is the real one —
// behind a reverse proxy on the same host, every caller looks like loopback and the detail is
// disclosed to whoever the proxy is forwarding for. Closing that needs a configured trusted-proxy
// list, which is a setting this binary does not have yet and is filed rather than guessed at here.
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
