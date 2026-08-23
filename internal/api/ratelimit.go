package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
)

// The rate-limit gate: the FIRST thing that happens to a request that reached an operation, and the
// only middleware in this tree that can refuse one without reading the database
// (docs/design/03-security.md §10, docs/design/02-api-design.md §5).
//
// WHY IT IS A HUMA MIDDLEWARE AND WHY IT IS MOUNTED FIRST. It is a Huma middleware because the limit
// is a property of the API surface: /healthz and /readyz must answer whatever else is happening —
// canonical §13 makes a 429 there a container Docker kills mid-migration — and the SPA's static
// assets are served from memory and cost nothing to shed. Mounting it FIRST is the part that matters
// for security: it runs before the authorization gate and before the credential resolver, so a flood
// is shed before it costs a session lookup, a token hash or (when the login endpoint lands) an argon2
// verification. A limiter mounted after authentication protects everything except the expensive part.
//
// WHAT IT KEYS ON, AND WHAT IT DOES NOT. The key is the RESOLVED CLIENT IP (issue #98): the socket's
// peer address, unless a trusted proxy told us otherwise. Nothing else is available here by
// construction — the Principal is resolved by the middleware that runs after this one — so the
// per-principal, per-token and per-operation buckets of §10's table are NOT implemented and are
// deliberately not faked from a header. They land with the endpoints that need them; see the issue
// filed alongside this change.
//
// THE HAZARD AN OPERATOR HAS TO KNOW ABOUT: behind a reverse proxy with DKP_TRUSTED_PROXIES unset,
// every request resolves to the proxy's address, so the whole guild shares one bucket. That is why
// the client-IP middleware warns once when a forwarded header arrives with the trust list empty, and
// why docs/operations/troubleshooting.md names the symptom.

// rateLimitPolicies are the two buckets this API ships, taken from §10's table:
// "PAT reads 600/min" and "PAT writes 120/min | separate bucket". They are also what
// api_token.rate_limit_rpm already defaults to in db/schema.hcl.
//
// Functions rather than package-level vars because a var is package-level mutable state, which
// -shuffle=on and t.Parallel() turn into an intermittent failure in somebody else's package
// (.claude/rules/go-idioms.md).
//
// THE NAMES ARE WIRE SURFACE. They appear in the RateLimit and RateLimit-Policy fields, a bot keys
// its backoff on them, and renaming one is a breaking change for the same reason renaming an
// OperationID is.
func readPolicy() middleware.Policy {
	return middleware.Policy{Name: "read", Quota: 600, Window: time.Minute}
}

func writePolicy() middleware.Policy {
	return middleware.Policy{Name: "write", Quota: 120, Window: time.Minute}
}

// policyFor picks the bucket by method.
//
// SAFE METHODS READ, EVERYTHING ELSE WRITES (RFC 9110 §9.2.1). Splitting on the method rather than on
// the operation is what keeps this honest at Wave 1: a per-operation limit needs a declared limit per
// operation, and inventing one per route as routes appear is how a limit table drifts from the
// document that specifies it. The method is a fact about the request that no route table can
// disagree with.
func policyFor(method string) middleware.Policy {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return readPolicy()
	default:
		return writePolicy()
	}
}

// unresolvedClientKey is the bucket a request lands in when no client address could be determined at
// all — a listener that is not TCP, or a hand-built request in a test.
//
// It is a SHARED bucket, which is the fail-closed choice: the alternative is exempting a request
// whose origin cannot be established, and "I could not tell who you are" must not be the cheapest way
// past a limit. It is reachable only through a wiring fault, and the cost of that fault is a single
// throttled bucket rather than an open door.
const unresolvedClientKey = "unresolved"

// rateLimitMiddleware charges every operation against its bucket and emits both header families.
//
// BOTH FAMILIES ON EVERY RESPONSE, not only on a 429 (02-api-design.md §5). A client that first sees
// its budget in the response that already refused it cannot avoid the refusal; the headers are the
// mechanism by which a well-written bot never reaches one.
//
// NOTHING IS LOGGED PER REFUSAL, and that is deliberate rather than an omission. The condition is
// "an unauthenticated caller sent too many requests", which is a thing anyone can produce on demand,
// so a line per refusal is a log-flooding primitive handed to whoever is already flooding. The
// counter belongs in /metrics when that exists; the limiter's own degradation (Overflowed) is a
// process fact and is reported once by the code that reads it.
func rateLimitMiddleware(humaAPI huma.API, limiter *middleware.Limiter) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		policy := policyFor(ctx.Method())
		decision := limiter.Take(policy, policy.Name+"|"+clientKey(ctx))

		// Set BEFORE next: a handler that has written its status and body has flushed the headers, and
		// a Set afterwards is silently dropped — on exactly the responses whose budget a client most
		// wants to read.
		decision.SetHeaders(policy, ctx.SetHeader)

		if decision.Allowed {
			next(ctx)

			return
		}

		// RFC 9110 §10.2.3. Delta-seconds, never a date: a date requires the client's clock to agree
		// with ours, and half the bots in this ecosystem run on a Raspberry Pi with no RTC.
		ctx.SetHeader(middleware.HeaderRetryAfter, strconv.FormatInt(decision.RetryAfterSeconds(), 10))

		problem := NewProblem(http.StatusTooManyRequests, CodeRateLimited,
			"This client has used its request budget for now. Retry after the number of seconds in "+
				"Retry-After; the RateLimit headers on every response say how much budget is left.")

		// The write error is waived on the same terms as internal/api/auth.go: by the time huma
		// reports one the response is committed, huma has already printed it, and there is nothing
		// left to say to this caller.
		_ = huma.WriteErr(humaAPI, ctx, problem.Status, problem.Detail, problem)
	}
}

// clientKey is the resolved client address for this request, as a bucket key.
//
// The middleware.ClientIP wrapper resolved it once, at the transport level, for every consumer that
// needs it (issue #98). The fallback to the raw peer address covers a handler tree assembled without
// that wrapper: it is the same answer the resolver gives when it declines to read a header, so a
// missing wrapper degrades to zero-trust rather than to one shared bucket for the whole internet.
func clientKey(ctx huma.Context) string {
	addr, ok := middleware.ClientIPFromContext(ctx.Context())
	if !ok {
		if req, _ := humago.Unwrap(ctx); req != nil {
			addr = middleware.PeerIP(req)
		}
	}

	if !addr.IsValid() {
		return unresolvedClientKey
	}

	return addr.String()
}
