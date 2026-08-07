package middleware

import (
	"net/http"
)

// Renderer writes an RFC 9457 problem+json response.
//
// Declared here, by the consumer, and satisfied by internal/api — the same shape api.ReadyChecker
// uses. The alternative, importing internal/api for its ProblemDetail type, would close an import
// cycle, because internal/api mounts this middleware.
//
// It takes a status and a code string rather than an error because the callers below have no error
// to pass: an unrouted path, a method mismatch and a recovered panic are all "the request never
// reached a handler".
type Renderer interface {
	// RenderProblem writes status and a problem+json body carrying code. It must not panic, and it
	// must be safe to call when nothing else has been written to w.
	RenderProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string)
}

// Matcher reports which pattern, if any, a router would use for a request.
//
// *http.ServeMux satisfies it. Declared as an interface so this package does not depend on the
// concrete router type, and so a test can drive the not-matched branch without building a mux.
type Matcher interface {
	Handler(r *http.Request) (h http.Handler, pattern string)
}

// Problem guarantees that every response this server produces is problem+json when it is an error.
//
// Huma renders its own errors — validation failures, a bad path parameter — through the hook
// internal/api installs, so those are covered. What is NOT covered is everything net/http answers
// before Huma is reached, and at route #1 that is almost every request this server will ever see:
//
//   - An unrouted path. http.ServeMux answers `404 page not found` as text/plain.
//   - A method mismatch on a routed path. ServeMux answers `Method Not Allowed` as text/plain.
//   - A panic in a handler. Without a recover the connection is torn down mid-response and the
//     caller gets a transport error rather than a 500 carrying the request id.
//
// A bot whose error parser expects problem+json cannot read any of the first two, and "never HTTP
// 200 with an error body" (canonical §7) is only half the promise — the other half is that an error
// is always shaped the same way.
//
// The empty pattern is how a non-match is detected, and it was verified against net/http rather
// than assumed: for both the 404 and the 405 case ServeMux.Handler returns pattern == "". The
// obvious alternative — registering a `/` catch-all — is WRONG and was measured to be wrong: a `/`
// pattern also matches a method mismatch, so it swallows ServeMux's 405 and turns
// `POST /healthz` into a 404. internal/api/health_test.go requires 405 there.
func Problem(mux Matcher, renderer Renderer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer recoverToProblem(w, r, renderer)

		if _, pattern := mux.Handler(r); pattern != "" {
			// Routed. Hand over untouched — no wrapping of the ResponseWriter at all, so streaming
			// responses and http.ResponseController keep working. Canonical §7 puts a long-lived
			// /events stream on this server in a later phase, and a wrapper here would surface as a
			// stream that never flushes.
			next.ServeHTTP(w, r)

			return
		}

		// Not routed. Ask the router what status it would have used rather than reimplementing its
		// precedence rules, then say the same thing in our media type. Running it against a
		// discarding writer is safe precisely because no handler matched: the only thing that will
		// run is net/http's built-in NotFound or method-not-allowed reply, neither of which has
		// side effects.
		probe := &headerProbe{headers: make(http.Header)}
		next.ServeHTTP(probe, r)

		status, code, detail := http.StatusNotFound, "not_found",
			"No operation is registered for this path."

		if probe.status == http.StatusMethodNotAllowed {
			status, code, detail = http.StatusMethodNotAllowed, "method_not_allowed",
				"That method is not allowed on this path."

			// RFC 9110 §15.5.6 makes Allow mandatory on a 405, and ServeMux has already worked out
			// the correct value. Dropping it would leave a client unable to discover the right verb.
			for _, allow := range probe.headers.Values("Allow") {
				w.Header().Add("Allow", allow)
			}
		}

		renderer.RenderProblem(w, r, status, code, detail)
	})
}

// recoverToProblem turns a panic into a 500 problem+json response and a log line.
func recoverToProblem(w http.ResponseWriter, r *http.Request, renderer Renderer) {
	rec := recover()
	if rec == nil {
		return
	}

	// http.ErrAbortHandler is the documented way for a handler to abort without being reported as an
	// error — the SSE and streaming code in later phases uses it. Re-panicking preserves net/http's
	// own handling instead of logging a false 500 on every closed stream.
	if rec == http.ErrAbortHandler {
		panic(rec)
	}

	Logger(r.Context()).ErrorContext(r.Context(), "panic serving request",
		"panic", rec, "method", r.Method, "path", r.URL.Path)

	// If the handler already wrote, the headers are on the wire and nothing can be sent; the log
	// line above is the whole remedy. RenderProblem's own write then fails harmlessly, and net/http
	// suppresses the duplicate-WriteHeader warning to the error log rather than to the client.
	renderer.RenderProblem(w, r, http.StatusInternalServerError, "internal_error",
		"The server encountered an unexpected condition.")
}

// headerProbe captures the status and headers of net/http's built-in not-found and
// method-not-allowed replies while discarding their plain-text bodies.
//
// It is used ONLY for requests that matched no pattern, so it never sits in front of a real
// handler and cannot affect streaming.
type headerProbe struct {
	headers http.Header
	status  int
}

func (p *headerProbe) Header() http.Header { return p.headers }

func (p *headerProbe) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}

// Write discards. The body being thrown away is `404 page not found\n` or `Method Not Allowed\n`,
// which is exactly the text this middleware exists to replace.
func (p *headerProbe) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = http.StatusOK
	}

	return len(b), nil
}
