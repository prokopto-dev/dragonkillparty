package middleware

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/prokopto-dev/dragonkillparty/internal/clock"
)

// HeaderRequestID is the request-id header, spelled exactly as it appears on the wire.
//
// `X-Request-Id`, not `X-Request-ID`: docs/design/02-api-design.md:647 and the problem-body examples
// in .claude/rules/api-endpoints.md both use this casing, and it is what the SDKs and the SPA read
// back. Go canonicalises header keys on both get and set, so the casing here is documentation
// rather than mechanism — but it is the casing a reader will grep for.
const HeaderRequestID = "X-Request-Id"

// maxInboundIDLen bounds an echoed client-supplied id.
//
// A ULID is 26 characters. The allowance above that exists because a caller behind a proxy chain
// may already have a correlation id in another format and echoing it is more useful than replacing
// it; the cap exists because this value is written into a response header and into every slog line
// for the request, and an unbounded attacker-controlled string in both places is a log-flooding
// primitive.
const maxInboundIDLen = 128

// requestIDKey types the context key. An unexported struct{} type cannot collide with a key set by
// any other package, which a string constant can.
type requestIDKey struct{}

// RequestID assigns every request an id, echoes it, and puts it on the context.
//
// Three effects, and acceptance criterion 6 of first-ten-prs.md PR 4 names all three: the id is
// echoed when the client supplied a usable one and generated as a ULID when it did not; it goes out
// on the response header; and it is attached to a slog logger in the context so every line logged
// while handling the request carries it without any handler having to thread it through.
// internal/api/errors.go reads it back out of the context to populate `request_id` in every problem
// body — which is the whole support workflow: a user pastes the id from a screenshot and it is a
// grep key across the logs (docs/design/06-cicd-and-release.md §"Structured logs").
//
// The header is set BEFORE next.ServeHTTP rather than after. A handler that writes its status and
// body first would otherwise flush the headers and silently drop a later Set — which is exactly the
// case that matters, because the failing request is the one whose id someone needs.
func RequestID(clk clock.Clock, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitiseInboundID(r.Header.Get(HeaderRequestID))
		if id == "" {
			id = newULID(clk)
		}

		w.Header().Set(HeaderRequestID, id)

		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = withLogger(ctx, slog.With("request_id", id))

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// IDFromContext returns the request id RequestID stored, or "" outside a request.
//
// Returning "" rather than panicking or generating one is deliberate: the callers are error
// renderers and log helpers, and a missing id must never be the reason a request fails. An empty
// `request_id` in a problem body is a self-describing symptom — it says the middleware was not
// mounted — whereas a freshly minted id there would be a plausible-looking value that correlates
// with nothing in the logs.
func IDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)

	return id
}

// sanitiseInboundID decides whether a client-supplied id may be echoed, returning "" if not.
//
// This is a security boundary, not tidiness. The value goes into a response header and into log
// lines, so it must not carry CR, LF or NUL (header and log injection), must not be empty, and must
// be bounded. Everything printable-ASCII and in range is accepted rather than requiring a ULID:
// rejecting a proxy's existing correlation id would break the one case echoing exists to serve.
//
// If this function is ever relaxed to "trim and pass through", the injection it prevents comes
// straight back — an id of "abc\r\nSet-Cookie: …" would be written verbatim into the response.
func sanitiseInboundID(raw string) string {
	id := strings.TrimSpace(raw)
	if id == "" || len(id) > maxInboundIDLen {
		return ""
	}

	for i := range len(id) {
		// Printable ASCII only. Below 0x20 covers CR, LF, tab and NUL; 0x7f is DEL. Anything
		// multi-byte is rejected with it, which is fine: correlation ids are machine-generated
		// tokens, not text.
		if id[i] < 0x20 || id[i] > 0x7e {
			return ""
		}
	}

	return id
}

// newULID mints a request id.
//
// crypto/rand rather than the package's default entropy source: request ids appear in problem
// bodies that users paste into issues, so a guessable sequence would leak how much traffic an
// instance is taking and let one user's id be inferred from another's. Ten bytes per request is a
// syscall a guild-sized workload will never notice.
//
// The clock is injected because time.Now is grep-banned outside internal/clock (gate CLOCK001), and
// because a test that wants deterministic ids needs to control the timestamp half.
//
// ulid.MustNew cannot fail here in practice — it errors only on a timestamp past 10889 AD or a
// short read from the entropy source — and a request id is not worth failing a request over.
func newULID(clk clock.Clock) string {
	return ulid.MustNew(ulid.Timestamp(clk.Now()), rand.Reader).String()
}
