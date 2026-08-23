package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/prokopto-dev/dragonkillparty/internal/api/middleware"
	"github.com/prokopto-dev/dragonkillparty/internal/auth"
	"github.com/prokopto-dev/dragonkillparty/internal/core"
	"github.com/prokopto-dev/dragonkillparty/internal/idempotency"
)

// The idempotency half of the write-safety pair: the middleware that turns N identical retries into
// one effect (issue #290, canonical §7, docs/api/idempotency-and-concurrency.md).
//
// WHY IT IS MIDDLEWARE AND NOT A HANDLER CONCERN, in the same words auth.go uses about the
// credential: "bots retry; duplicated ticks and double-charged bids are the top support burden"
// (AGENTS.md). A handler that has to remember to claim a key is a handler that will one day not, and
// the failure is silent — a raid night credited twice, found three weeks later, fixed with a
// reversal and an argument. Running before the handler makes forgetting structurally impossible.
//
// THE FENCE IS THE OPERATION'S OWN DECLARATION, never a path list kept here. An operation is fenced
// exactly when it declares a REQUIRED Idempotency-Key header parameter, which is the same property
// arch_test.go's TestArch_MutatingPost_RequiresIdempotencyKey asserts of every mutating POST under
// canonical §7's prefixes. One reader for both (headerParameter, precondition.go): a second list of
// fenced paths in here is a routing table that disagrees with the registry the first time somebody
// adds a route, and it would disagree in the direction that skips the check.
//
// ORDER: authorization, then precondition, then this, then the handler — 03-security.md's chain
// (`authz → idempotency → handler`) with the precondition slotted where a 428 cannot burn a key. By
// the time this runs the caller is authenticated and permitted, so no refusal it could write is
// recorded as an idempotent outcome; the 401s and 403s upstream never reach a record at all.

const (
	// headerIdempotencyKey is the caller's key. Spelled once here; the arch gate matches it
	// case-insensitively against what an operation declares.
	headerIdempotencyKey = "Idempotency-Key"

	// headerIdempotencyReplayed marks a replayed response, and it is the signal
	// docs/api/idempotency-and-concurrency.md promises: "the original status and body are returned,
	// plus Idempotency-Replayed: true". A bot uses it to tell "my retry worked" from "my retry did
	// the work", which are different lines in its own log.
	headerIdempotencyReplayed = "Idempotency-Replayed"

	// headerRetryAfter is RFC 9110 §10.2.3's, sent with the 409 for an in-flight duplicate.
	headerRetryAfter = "Retry-After"
)

// maxRecordedResponseBytes bounds what one record may hold.
//
// It matches the order of magnitude of Huma's default request body limit (1 MiB) deliberately: the
// buffer is a COPY of bytes already streaming to the client, so this is the extra memory a fenced
// request costs, and pairing it with the limit already accepted on the way in is the answer that
// needs no separate justification. Every fenced operation in canonical §7's list returns a created
// resource or a list of them — a tick, an adjustment, a settled session — and none is within two
// orders of magnitude of this.
const maxRecordedResponseBytes = 1 << 20

// errNoIdempotencyService is the fault behind a fenced operation reached with no service wired. A
// sentinel rather than an inline errors.New so the log line reads the same every time.
var errNoIdempotencyService = errors.New("no idempotency service is wired")

// replayableResponseHeaders is the ALLOWLIST of headers a replay reproduces.
//
// AN ALLOWLIST AND NOT THE WHOLE HEADER SET, and the reason is the one header that must never be
// replayed: Set-Cookie. A session cookie is minted for the request that asked for it, and re-issuing
// one into a different request — possibly days later, possibly to a different connection — is a
// credential this table has no business handing out. Three headers are worth reproducing and each
// earns its place:
//
//   - Content-Type, or a replayed problem+json arrives labelled application/json and the caller's
//     error parser does not recognise its own error.
//   - ETag, so a bot that retried a create still learns the validator it needs for the next write.
//   - Location, so a replayed 201 still names what was created.
//
// A header that is not here is simply absent from the replay, which is visible; a header that is
// here and should not be is a security bug, which is why the list is short and written down.
func replayableResponseHeaders() []string {
	return []string{"Content-Type", "ETag", "Location"}
}

// idempotencyMiddleware claims the caller's Idempotency-Key, runs the handler at most once per key,
// and records what it produced.
//
// A NIL SERVICE FAILS CLOSED. It means no fenced operation can be served — those answer 503 — and it
// is the same reading Config gives every other authorization input: a missing one refuses, it never
// defaults to allow. Serving a fenced POST with no record is precisely the double-write this exists
// to prevent, so "no service" cannot mean "no idempotency".
func idempotencyMiddleware(humaAPI huma.API, svc *idempotency.Service) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()

		// Declared AND required: the arch gate fences on `required`, so an operation that declares an
		// optional Idempotency-Key has not opted in and is passed through. That keeps the two readings
		// identical rather than merely similar.
		if declared, required := headerParameter(op, headerIdempotencyKey); !declared || !required {
			next(ctx)

			return
		}

		if svc == nil {
			refuseIdempotencyUndecidable(humaAPI, ctx, op, errNoIdempotencyService)

			return
		}

		request, problem, fault := idempotentRequest(ctx, op)

		switch {
		case fault != nil:
			// The request could not be KEYED, as distinct from being refused: no principal, a
			// principal kind this server cannot name, a body that could not be read. Each is a 503
			// and an ERROR line, never a 4xx — the caller has done nothing a different request would
			// fix.
			refuseIdempotencyUndecidable(humaAPI, ctx, op, fault)

			return

		case problem != nil:
			writeIdempotencyProblem(humaAPI, ctx, op, problem)

			return
		}

		decision, err := svc.Begin(ctx.Context(), request)
		if err != nil {
			refuseIdempotencyUndecidable(humaAPI, ctx, op, err)

			return
		}

		switch decision.Outcome {
		case idempotency.OutcomeExecute:
			execute(ctx, next, op, svc, decision.RecordID)

		case idempotency.OutcomeReplay:
			replayRecorded(ctx, op, decision.Replay)

		case idempotency.OutcomeReused:
			writeIdempotencyProblem(humaAPI, ctx, op, keyReused(decision.OriginalRequestHash))

		case idempotency.OutcomeInFlight:
			ctx.SetHeader(headerRetryAfter,
				strconv.Itoa(int(idempotency.RetryAfter.Seconds())))
			writeIdempotencyProblem(humaAPI, ctx, op, keyInFlight())

		case idempotency.OutcomeUndecided:
			// Unreachable: Begin returns an error alongside the zero outcome, and that is handled
			// above. Written as the closed answer rather than omitted, because the default branch of
			// a control standing between a retry and a second award must not be able to become
			// "execute" by omission.
			fallthrough

		default:
			refuseIdempotencyUndecidable(humaAPI, ctx, op,
				errors.New("idempotency service returned no decision"))
		}
	}
}

// idempotentRequest turns the live HTTP request into the Request a claim is made of, and hands the
// body back to the handler.
//
// IT READS THE BODY ONCE, for both readers. The hash needs the bytes and so does the handler, and a
// middleware that read them without putting them back would hand every fenced operation an empty
// body — which would validate as a missing field rather than as a bug in here. The limit is the
// OPERATION's MaxBodyBytes, not one this file invents, so a body this refuses is exactly a body the
// handler would have refused (idempotency.ReadBody).
// It returns THREE values because the two failure kinds are answered differently: a *ProblemDetail
// is what the caller gets, and a bare error is a fault this server has to report to its operator and
// answer 503 for. Collapsing them would either log a client's mistyped key as a server error or hide
// a wiring bug behind a 400.
func idempotentRequest(ctx huma.Context, op *huma.Operation) (idempotency.Request, *ProblemDetail, error) {
	key, err := idempotency.ValidateKey(ctx.Header(headerIdempotencyKey))
	if err != nil {
		return idempotency.Request{}, keyRequired(err), nil
	}

	principal, ok := auth.FromContext(ctx.Context())
	if !ok {
		// Unreachable through the product: a fenced operation declares Security, so the credential
		// middleware refused the request before this one ran. It is written CLOSED anyway — a fenced
		// request with no principal has no scope to key a record by, and executing it unkeyed is the
		// double-write this middleware exists to prevent. 503 rather than 401 because the caller has
		// done nothing wrong: a fenced operation reached anonymously is this server failing to run
		// its own middleware in order.
		return idempotency.Request{}, nil, errNoPrincipal
	}

	ref, err := idempotency.PrincipalRef(principal.Kind, principal.ID)
	if err != nil {
		return idempotency.Request{}, nil, err
	}

	req, _ := humago.Unwrap(ctx)

	body, tooLarge, err := idempotency.ReadBody(req.Body, op.MaxBodyBytes)
	if err != nil {
		// A body that could not be read is a connection that went away mid-request, or a read
		// deadline. Neither is the caller's fault and neither is a state to claim a key in.
		return idempotency.Request{}, nil, err
	}

	if tooLarge {
		problem := NewProblem(http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
			"The request body is larger than this operation accepts. Split the batch and retry each "+
				"part under its own Idempotency-Key.")
		problem.Meta = map[string]any{"max_bytes": op.MaxBodyBytes}

		return idempotency.Request{}, problem, nil
	}

	// The handler reads what this just consumed. NopCloser because the original body's Close has
	// already been the caller's responsibility since huma read it, and a second Close on a drained
	// body is not something to make the handler think about.
	req.Body = io.NopCloser(bytes.NewReader(body))

	url := ctx.URL()

	return idempotency.Request{
		PrincipalRef: ref,
		Key:          key,
		Method:       ctx.Method(),
		PathTemplate: op.Path,
		Path:         url.Path,
		RawQuery:     url.RawQuery,
		Body:         body,
	}, nil, nil
}

// execute runs the handler under a claimed key and records what it produced.
//
// THE COMPLETION WRITE OUTLIVES THE REQUEST CONTEXT, deliberately: context.WithoutCancel. A client
// that disconnects the moment its mutation commits would otherwise leave the claim in_flight, and
// every retry for the next 24 hours would answer 409 — a dropped connection turned into a day of
// refusals on the endpoint a raid night depends on. The mutation already happened; recording it is
// not optional work.
func execute(
	ctx huma.Context,
	next func(huma.Context),
	op *huma.Operation,
	svc *idempotency.Service,
	recordID core.ULID,
) {
	req, writer := humago.Unwrap(ctx)
	recorder := newResponseRecorder(writer)

	settled := false

	defer func() {
		if settled {
			return
		}

		// The handler panicked. The Problem middleware turns that into a 500 and the caller retries;
		// a claim left behind would answer that retry with 409 instead of executing it. Whatever the
		// recorder happens to hold at this point is a partial response and must not become a replay.
		abandonClaim(context.WithoutCancel(ctx.Context()), svc, op, recordID,
			"handler panicked under a claimed idempotency key")
	}()

	// A FRESH huma.Context over the SAME request, because humago exposes no way to substitute the
	// writer on an existing one. `req` already carries the principal the credential middleware put
	// into its context (huma.WithContext rebinds the request), so nothing upstream is lost — only the
	// status pointer is new, and the recorder is what this function reads the status from anyway.
	next(humago.NewContext(op, req, recorder))

	settled = true

	completeClaim(context.WithoutCancel(ctx.Context()), svc, op, recordID, recorder)
}

// abandonClaim deletes a claim whose request produced nothing replayable, and logs when it cannot.
//
// A FAILURE HERE IS AN ERROR-LEVEL EVENT rather than something to return: there is no caller left to
// return it to — the response is already written — and the consequence is concrete and needs an
// operator's eye, because the key stays claimed until its TTL and every retry meets a 409.
func abandonClaim(
	ctx context.Context,
	svc *idempotency.Service,
	op *huma.Operation,
	recordID core.ULID,
	why string,
) {
	if err := svc.Abandon(ctx, recordID); err != nil {
		middleware.Logger(ctx).ErrorContext(ctx, "cannot release an idempotency claim",
			"operation", operationID(op),
			"record_id", recordID.String(),
			"reason", why,
			slog.Any("error", err))
	}
}

// completeClaim records the response, or releases the claim when there is nothing worth recording.
//
// A 5xx RELEASES IT, which is the row docs/api/idempotency-and-concurrency.md publishes: "the record
// rolled back with the transaction; your retry executes normally". A server fault is not an outcome
// to make permanent for a day — the caller did nothing wrong, and the whole point of their retry is
// that the operation has not happened yet.
//
// EVERY OTHER STATUS IS RECORDED, 4xx included. A validation failure is deterministic for a given
// body, so replaying it is both cheap and true; and a client that fixes its body is sending a
// different request, which collides as 422 idempotency_key_reused and tells them to use a fresh key.
func completeClaim(
	ctx context.Context,
	svc *idempotency.Service,
	op *huma.Operation,
	recordID core.ULID,
	recorder *responseRecorder,
) {
	switch {
	case recorder.status == 0:
		abandonClaim(ctx, svc, op, recordID, "the handler wrote no response")

		return

	case recorder.status >= http.StatusInternalServerError:
		abandonClaim(ctx, svc, op, recordID, "the handler answered "+strconv.Itoa(recorder.status))

		return

	case recorder.truncated:
		// The response outgrew what one record may hold, so there is nothing to replay. Releasing the
		// claim lets the retry execute, where the real uniqueness constraint underneath every fenced
		// operation is what stops the duplicate — docs/api is explicit that the header is a
		// convenience layer over those and never a substitute. It is logged at ERROR because a fenced
		// operation whose response does not fit is a design problem in that operation.
		abandonClaim(ctx, svc, op, recordID,
			"the response exceeded "+strconv.Itoa(maxRecordedResponseBytes)+" bytes and cannot be replayed")

		return
	}

	if err := svc.Complete(ctx, recordID, idempotency.Recorded{
		Status:  recorder.status,
		Headers: recorder.replayableHeaders(),
		Body:    recorder.body.Bytes(),
	}); err != nil {
		// The mutation landed and the record did not. The next retry re-executes, which is the
		// failure mode this whole file exists to prevent — so it is an ERROR naming the record, and
		// the operation's own uniqueness constraint is what catches the duplicate in the meantime.
		middleware.Logger(ctx).ErrorContext(ctx, "cannot record an idempotent response",
			"operation", operationID(op),
			"record_id", recordID.String(),
			"status", recorder.status,
			slog.Any("error", err))
	}
}

// replayRecorded writes a completed record's response back, marked as a replay.
func replayRecorded(ctx huma.Context, op *huma.Operation, recorded idempotency.Recorded) {
	for name, value := range recorded.Headers {
		ctx.SetHeader(name, value)
	}

	ctx.SetHeader(headerIdempotencyReplayed, "true")
	ctx.SetStatus(recorded.Status)

	if len(recorded.Body) > 0 {
		// Waived for the reason .claude/rules/go-idioms.md permits: the status line is already
		// committed by the time a body write can fail, and there is nothing left to say to this
		// caller.
		_, _ = ctx.BodyWriter().Write(recorded.Body)
	}

	middleware.Logger(ctx.Context()).InfoContext(ctx.Context(), "replayed an idempotent response",
		"operation", operationID(op),
		"status", recorded.Status)
}

// keyRequired is the 400 for an absent or unusable key. The detail names what was wrong, because
// every one of ValidateKey's reasons has a different fix and "add one" only helps the first.
func keyRequired(err error) *ProblemDetail {
	return NewProblem(http.StatusBadRequest, CodeIdempotencyKeyRequired,
		"This operation creates domain state and requires an Idempotency-Key header: "+err.Error()+
			". Generate one per logical operation — a UUIDv4 or a ULID — and reuse it only when "+
			"retrying that same operation.")
}

// keyReused is the 422 for a key held by a different request. meta.original_request_hash is what
// docs/api/errors.md promises and is the fingerprint of the request that owns the key, so a bot
// author can see the two differ rather than guessing.
func keyReused(originalRequestHash string) *ProblemDetail {
	problem := NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyKeyReused,
		"This Idempotency-Key is already held by a different request. You have a key-generation "+
			"bug: use one key per logical operation, not one per bot or per session.")
	problem.Meta = map[string]any{"original_request_hash": originalRequestHash}

	return problem
}

// keyInFlight is the 409 for an identical request that has not finished. Its Retry-After is set by
// the caller, because a header is not part of a problem body.
func keyInFlight() *ProblemDetail {
	return NewProblem(http.StatusConflict, CodeIdempotencyKeyInFlight,
		"An identical request under this Idempotency-Key is still executing. Wait for Retry-After "+
			"and retry the same request; it will replay the original response rather than run again.")
}

// writeIdempotencyProblem renders a refusal and logs it, on the split refuse() and refuseCapability()
// use: the caller gets the code docs/api/errors.md publishes, and the log gets the operation and the
// reason.
func writeIdempotencyProblem(humaAPI huma.API, ctx huma.Context, op *huma.Operation, problem *ProblemDetail) {
	middleware.Logger(ctx.Context()).WarnContext(ctx.Context(), "idempotency refused",
		"operation", operationID(op),
		"status", problem.Status,
		"code", string(problem.Code))

	_ = huma.WriteErr(humaAPI, ctx, problem.Status, problem.Detail, problem)
}

// refuseIdempotencyUndecidable writes the 503 for a claim that could not be MADE, as distinct from
// one that was refused: no service wired, or a database that could not be read.
//
// ITS OWN SENTENCE, not capability.go's undecidableDetail, for the reason that file gives about
// keeping unavailableDetail separate: these are different events with different operator-facing
// advice, and reusing a sentence about permissions would send whoever is debugging a database outage
// to look at role assignments.
func refuseIdempotencyUndecidable(humaAPI huma.API, ctx huma.Context, op *huma.Operation, err error) {
	middleware.Logger(ctx.Context()).ErrorContext(ctx.Context(),
		"cannot claim an idempotency key; refusing the operation",
		"operation", operationID(op),
		slog.Any("error", err))

	problem := NewProblem(http.StatusServiceUnavailable, CodeServiceUnavailable,
		"This server could not record your Idempotency-Key, so it refused rather than risk "+
			"performing this operation twice. Your request is not the problem; retry shortly. The "+
			"process log names the fault.")

	_ = huma.WriteErr(humaAPI, ctx, problem.Status, problem.Detail, problem)
}

// errNoPrincipal is the fault behind a fenced operation reached with no resolved principal.
var errNoPrincipal = errors.New("fenced operation reached with no principal")

// responseRecorder copies what the handler writes, so a completed claim can replay it.
//
// It WRAPS rather than buffers: every byte still goes to the client as the handler writes it, and
// the copy is bounded by maxRecordedResponseBytes. A buffering recorder would hold a response back
// until the handler returned, which changes the timing of every fenced write to buy nothing.
type responseRecorder struct {
	http.ResponseWriter

	status    int
	body      bytes.Buffer
	truncated bool
}

func newResponseRecorder(w http.ResponseWriter) *responseRecorder {
	return &responseRecorder{ResponseWriter: w}
}

// WriteHeader records the FIRST status written. net/http ignores a second WriteHeader and so does
// this, so what the record holds is what the client received.
func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}

	r.ResponseWriter.WriteHeader(status)
}

// Write records the body up to the cap and always passes it through.
//
// A write with no preceding WriteHeader is a 200, which is net/http's rule; recording it here rather
// than inferring it later is what keeps `status == 0` meaning "the handler wrote nothing at all".
func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}

	if !r.truncated {
		if r.body.Len()+len(p) > maxRecordedResponseBytes {
			r.truncated = true
			r.body.Reset()
		} else {
			// bytes.Buffer.Write never returns an error (it panics on OOM), so there is nothing to
			// handle; the caller's return values are the wrapped writer's, which is what matters.
			_, _ = r.body.Write(p)
		}
	}

	return r.ResponseWriter.Write(p)
}

// replayableHeaders returns the allowlisted headers the handler set, and only those it actually set.
func (r *responseRecorder) replayableHeaders() map[string]string {
	var headers map[string]string

	for _, name := range replayableResponseHeaders() {
		value := r.Header().Get(name)
		if value == "" {
			continue
		}

		if headers == nil {
			headers = make(map[string]string, len(replayableResponseHeaders()))
		}

		headers[name] = value
	}

	return headers
}
