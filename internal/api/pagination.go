package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// The page-size contract. Canonical §7 and .claude/rules/api-endpoints.md: `limit` defaults to 50
// and caps at 200. The cap is not advice — it is what keeps a `?limit=100000` from turning one bot's
// retry loop into the statement budget of every other request on a volunteer's Raspberry Pi.
//
// THESE NUMBERS APPEAR TWICE IN THIS FILE and Go cannot help: a struct tag is an uninterpreted string
// literal, so ListParams below cannot write `default:"DefaultPageLimit"`. What closes the gap is
// TestArch_Collections_UseTheSharedEnvelope, which reads the `default`, `minimum` and `maximum` out
// of the EMITTED OpenAPI schema and compares them to these constants — so a tag edited without the
// constant (or the reverse) is a red gate, not a silent second contract.
const (
	// DefaultPageLimit is the page size a collection returns when the caller names none.
	DefaultPageLimit = 50

	// MaxPageLimit is the largest page any collection will return, whatever `?limit` asks for.
	MaxPageLimit = 200

	// MinPageLimit is the smallest. Zero is not a page, it is a `has_more` probe with no items, and
	// offering it would be a second, undocumented way to ask "is there anything here".
	MinPageLimit = 1
)

// ListParams is the pagination half of a collection's input struct, embedded rather than retyped.
//
// EMBED IT; DO NOT COPY IT. The `default` and `maximum` tags below reach the OpenAPI document and
// therefore both generated SDKs, so a resource that declares its own `limit` with a different cap
// publishes a second, contradictory pagination contract — which is the bespoke shape
// TestArch_Collections_UseTheSharedEnvelope exists to reject.
//
//	type ListRaidsInput struct {
//	    api.ListParams
//	    StateIn string `query:"state__in"`
//	}
//
// Filters are additional explicit typed parameters alongside it (see .claude/skills/add-endpoint/
// shapes.md), never a query DSL.
type ListParams struct {
	// Cursor is the opaque token from the previous page's `next_cursor`. Empty means "the first
	// page". It is never parsed by a client and never constructed by one.
	Cursor string `query:"cursor" doc:"Opaque cursor from a previous response's next_cursor. Omit for the first page."`

	// Limit is the requested page size. The tags publish the contract; Paginator.Request clamps as
	// well, because a handler reached through a path Huma did not validate (a test, a future internal
	// caller) must not be able to ask for an unbounded page.
	Limit int `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size. Defaults to 50, capped at 200."`
}

// Page is THE list envelope. Every collection in this API returns this shape and no other.
//
// `has_more` is AUTHORITATIVE: a client loops on it, not on whether `items` came back full, because a
// page can be short of `limit` and still have more behind it once row-level authorization filters it.
// `next_cursor` is null at the end.
//
// There is no `page`, no `offset` and no `Link` header, here or anywhere in the API. Offset
// pagination shifts under concurrent inserts, and the ledger is append-heavy: a bot walking page 3
// while three batches land sees some rows twice and some never, and the symptom is a member's
// balance being slightly wrong weeks later rather than an error anyone sees
// (docs/api/pagination-and-sync.md).
//
// Four keys MAY join these three, and only these four: `total` and `total_estimated` for
// `?include_total=true`, and `head_seq` and `server_time` for the `?since_seq=` catch-up loop. They
// are per-resource additions to the response type rather than fields of this struct, because most
// collections carry none of them — and the arch gate holds the list closed either way.
type Page[T any] struct {
	// Items is the page. It is NEVER null on the wire — an empty page is `[]` — because a bot that
	// ranges over the result should not have to distinguish the two.
	// `nullable:"false"` because Huma makes every Go slice a nullable array by default
	// (huma.DefaultArrayNullable), and the published schema is what both SDKs are generated from: a
	// nullable `items` becomes `T[] | null` in TypeScript and forces every consumer to null-check a
	// case this server never emits. The tag makes the document say what the code already does.
	Items []T `json:"items" nullable:"false" doc:"This page of results. Empty, never null, when there is nothing to return."`

	// NextCursor is the token for the next page, or null at the end.
	NextCursor *string `json:"next_cursor" doc:"Cursor for the next page, or null when this is the last page."`

	// HasMore is authoritative. Loop on this.
	HasMore bool `json:"has_more" doc:"Whether another page exists. Authoritative; loop on this, not on the item count."`
}

// ListOutput is the Huma output struct a collection operation returns: the envelope and nothing else.
//
//	func(ctx context.Context, in *ListRaidsInput) (*api.ListOutput[RaidDTO], error)
//
// Huma names the generated schema after the instantiated type (`ListOutput[…RaidDTO]` becomes
// `PageRaidDTO` in the document via DefaultSchemaNamer), so each resource still gets a distinct,
// readable schema name while sharing one Go type.
type ListOutput[T any] struct {
	Body Page[T]
}

// PageRequest is a collection request's pagination parameters after validation: the clamped limit and
// the decoded position, if the caller sent one.
//
// It carries the principal class and filter fingerprint it was DECODED against, unexported, so that
// NewPage mints the next cursor under exactly the binding Paginator.Request verified. The alternative
// — passing both to each call — is the transposable, forgettable arrangement core.Cursor's own
// Decode signature was designed to remove; reproducing it one layer up would give it back.
type PageRequest struct {
	// Limit is the page size to serve: the caller's, clamped to [MinPageLimit, MaxPageLimit].
	Limit int

	// After is the decoded position. Meaningful only when HasCursor is true.
	After core.Cursor

	// HasCursor reports whether the caller sent a cursor. False means "start at the beginning".
	HasCursor bool

	class  core.PrincipalClass
	filter string
}

// Fetch is how many rows the query should ask the database for: one more than the page.
//
// OVER-FETCHING BY ONE IS HOW `has_more` IS ANSWERED WITHOUT A SECOND QUERY. A `COUNT(*)` over the
// remainder is the obvious alternative and is wrong twice: it is a second scan of an append-heavy
// table, and it races — rows land between the two statements, so the count can disagree with the page
// it describes. Ask for limit+1, serve limit, and the extra row IS the answer.
func (r PageRequest) Fetch() int { return r.Limit + 1 }

// Paginator turns a collection request into a PageRequest and a page of rows into the envelope.
//
// It holds the instance's cursor codec, and it is safe for concurrent use because the codec is. A
// ZERO Paginator HOLDS NO CODEC AND REFUSES EVERY CALL with a 503 rather than falling back to
// unsigned cursors: an unsigned cursor is a client-editable WHERE clause, so "the key was not wired"
// has to look like an outage, not like a working endpoint. That is the same rule every other input in
// api.Config follows — a missing authorization input refuses, it never defaults to permissive.
type Paginator struct {
	codec *core.CursorCodec
}

// NewPaginator builds a paginator over the instance's cursor codec.
//
// A nil codec is accepted and produces the refusing zero value described above, because api.New is
// called with an incomplete Config in the spec-only and degraded-boot paths and neither should panic.
func NewPaginator(codec *core.CursorCodec) Paginator { return Paginator{codec: codec} }

// Request validates the pagination parameters of a collection request.
//
// class is the authorization class of the principal making this request and filter is the fingerprint
// of the filters it applied (core.FilterFingerprint over the canonical filter string the handler
// assembles). Both are checked against the presented cursor here, and both are remembered on the
// returned PageRequest so NewPage mints the next one under the same binding.
//
// The returned error is always a *ProblemDetail, ready to return from a handler:
//
//   - a tampered, truncated, foreign-keyed or cross-class token is 400 `cursor_invalid`;
//   - a genuine token aimed at a different query is 400 `cursor_filter_mismatch`, which is a
//     DIFFERENT code because it is recoverable by restarting the scan and a bot should be told so;
//   - a handler that forgot to bind its listing to a principal class, or an instance with no cursor
//     key, is 503 — a server fault, reported as one rather than leaked to the caller as a bad
//     request they cannot fix.
func (p Paginator) Request(in ListParams, class core.PrincipalClass, filter string) (PageRequest, error) {
	// Checked before anything else, and on the FIRST page as much as on a later one. A listing that
	// binds nothing is one whose cursors cross every boundary, and refusing only when a cursor is
	// presented would let such an endpoint look perfectly healthy right up to page two.
	if class == "" {
		return PageRequest{}, unboundListingProblem()
	}

	out := PageRequest{Limit: clampLimit(in.Limit), class: class, filter: filter}

	if in.Cursor == "" {
		return out, nil
	}

	if p.codec == nil {
		return PageRequest{}, noCursorCodecProblem()
	}

	cur, err := p.codec.Decode(in.Cursor, class, filter)
	if err != nil {
		return PageRequest{}, cursorProblem(err)
	}

	out.After = cur
	out.HasCursor = true

	return out, nil
}

// NewPage builds the response envelope from an over-fetched slice of rows.
//
// rows is what the query returned for req.Fetch() — up to limit+1 items in the collection's sort
// order. keyOf reports the sort key and the tie-breaker id of a row, which become the cursor's `k`
// and `id`; for a collection sorted by its own ULID primary key the two are the same value.
//
// The trim, the has_more and the mint are one function on purpose. Done by hand at each call site
// they are three independent chances to publish a `next_cursor` for a row that was trimmed off the
// page — which hands the client a position it has not seen and silently skips a row.
func NewPage[T any](p Paginator, req PageRequest, rows []T, keyOf func(T) (key, id core.ULID)) (Page[T], error) {
	// Never nil: `"items": null` is a shape every bot author has to special-case, and `[]` is the
	// same information without the branch.
	page := Page[T]{Items: []T{}}

	// Clamped again rather than read raw. Request always clamps, so this only matters for a
	// PageRequest built by literal — but a zero Limit there would trim every row away while
	// reporting has_more, which is a page with nothing in it and no way onward.
	limit := clampLimit(req.Limit)

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	page.Items = append(page.Items, rows...)
	page.HasMore = hasMore

	if !hasMore {
		return page, nil
	}

	// hasMore means len(rows) was greater than limit, and limit is at least MinPageLimit, so after
	// the trim there is always a last row to mint from. `has_more: true` with a null next_cursor is
	// a dead end for a client, and this is why it cannot happen.

	if p.codec == nil {
		return Page[T]{}, noCursorCodecProblem()
	}

	key, id := keyOf(rows[len(rows)-1])

	token, err := p.codec.Encode(core.Cursor{
		Key:            key,
		ID:             id,
		Filter:         req.filter,
		PrincipalClass: req.class,
	})
	if err != nil {
		if errors.Is(err, core.ErrEmptyPrincipalClass) {
			return Page[T]{}, unboundListingProblem()
		}

		return Page[T]{}, fmt.Errorf("mint next cursor: %w", err)
	}

	page.NextCursor = &token

	return page, nil
}

// clampLimit resolves the caller's `limit` to a page size this server will serve.
//
// Zero and negative mean "unset" and take the default: Huma applies the `default:"50"` tag for a
// request that omits the parameter, but a handler called from a test or a future internal caller
// arrives with the Go zero value, and defaulting there is better than serving an empty page.
// Above the cap it clamps rather than erroring — `?limit=1000` is a bot asking to go faster, not a
// malformed request, and answering 200 rows is what the documented contract says happens.
func clampLimit(limit int) int {
	switch {
	case limit < MinPageLimit:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	default:
		return limit
	}
}

// cursorProblem translates internal/core's cursor sentinels into the closed error enum.
//
// internal/core does not import internal/api — the dependency runs the other way — so the codec
// returns sentinels and this is the one place that names their wire codes.
func cursorProblem(err error) *ProblemDetail {
	switch {
	case errors.Is(err, core.ErrCursorFilterMismatch):
		return NewProblem(http.StatusBadRequest, CodeCursorFilterMismatch,
			"This cursor was issued for a different set of filters or a different sort. Restart the "+
				"walk without a cursor, using the filters you actually want.")

	case errors.Is(err, core.ErrEmptyPrincipalClass):
		// A cursor the server could not check the binding of is a server bug, not a client one.
		return unboundListingProblem()

	default:
		return NewProblem(http.StatusBadRequest, CodeCursorInvalid,
			"This cursor is not one this server issued to you, or its format has changed. Start the "+
				"walk over from the beginning.")
	}
}

// noCursorCodecProblem is the refusal an instance with no cursor signing key gives.
//
// It is 503 and not 500: the process is up and every unpaginated operation still works, so an
// operator's /readyz and /healthz stay meaningful, and a bot's retry has somewhere to land.
func noCursorCodecProblem() *ProblemDetail {
	return NewProblem(http.StatusServiceUnavailable, CodeServiceUnavailable,
		"This instance has no cursor signing key, so paginated collections cannot be served. "+
			"This is a server configuration fault; retry shortly.")
}

// unboundListingProblem is the refusal for a listing that never named a principal class.
//
// Also 503, and deliberately opaque: it means a HANDLER omitted the binding that keeps one
// principal class's cursors out of another's hands, and the caller can do nothing about it. Telling
// them which half of the check failed would only describe the server's internals.
func unboundListingProblem() *ProblemDetail {
	return NewProblem(http.StatusServiceUnavailable, CodeServiceUnavailable,
		"This collection is not bound to a principal class, so it cannot be paginated safely. "+
			"This is a server bug; please report it with the request id.")
}
