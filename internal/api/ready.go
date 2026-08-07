package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ReadyState is what /readyz reports about one check.
type ReadyState string

const (
	// ReadyStateReady — the check passed.
	ReadyStateReady ReadyState = "ready"
	// ReadyStatePending — migrations are waiting and DKP_AUTO_MIGRATE is off.
	ReadyStatePending ReadyState = "pending"
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
type ReadyReport struct {
	Check   string     `json:"check"`
	State   ReadyState `json:"state"`
	Command string     `json:"command,omitempty"`
	Detail  string     `json:"detail,omitempty"`
}

// ReadyChecker reports whether the instance is ready to serve.
//
// Declared here, by the consumer, and satisfied by internal/migrate — so internal/api does not
// import the migrator and the migrator does not import the API. It is one method because there is
// one check; the worker heartbeat and the DB-reachability checks canonical §13 also names arrive
// with the code that can fail them.
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
// 503 when migrations are pending, 200 when they are not, and — this is the part that matters —
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
// The redaction still has to exist for the checks that genuinely leak shape — worker heartbeat, free
// disk, outbox lag, raw DB error strings. Those land with the code that can fail them, and `Detail`
// below is where they will surface, so whoever adds the first one owns adding the caller check.
func handleReadyz(w http.ResponseWriter, r *http.Request, checker ReadyChecker) {
	report := readyUnwired
	if checker != nil {
		report = checker.Ready()
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
