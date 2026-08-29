package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/core"
)

// Tests for the shared list envelope: the page-size contract, the two cursor refusals a bot has to
// tell apart, and the over-fetch trim that decides `has_more`.
//
// The codec's own properties — signature, tamper detection, order preservation, the principal-class
// binding — are internal/core/cursor_test.go's. What is tested here is the layer above it: that a
// handler using these helpers cannot mint a cursor for a row it trimmed off the page, cannot serve
// an unsigned one, and reports the two failure modes with the two distinct codes docs/api/errors.md
// publishes.

// testFilter and otherFilter are two different filter sets, fingerprinted the way a handler would.
var (
	testFilter  = core.FilterFingerprint("pool=01JZ0PNMAN0000000000000001&sort=-started_at")
	otherFilter = core.FilterFingerprint("pool=01JZ0PNMAN0000000000000002&sort=-started_at")
)

// testClass and otherClass are two principal classes. The codec treats the value as opaque, so any
// two distinct strings exercise the boundary.
const (
	testClass  = core.PrincipalClass("member")
	otherClass = core.PrincipalClass("officer")
)

// testPaginator builds a paginator over a real codec — never a stub. A fake codec would let these
// tests pass against an envelope helper that mints tokens nothing verifies.
func testPaginator(t *testing.T) Paginator {
	t.Helper()

	codec, err := core.NewCursorCodec([]byte("a-thirty-two-byte-instance-key!!"))
	require.NoError(t, err)

	return NewPaginator(codec)
}

// row is a minimal listing row: a sort key and a tie-breaker, which is everything a cursor needs.
type row struct {
	Key core.ULID
	ID  core.ULID
}

// rowKey is the keyOf function NewPage takes.
func rowKey(r row) (core.ULID, core.ULID) { return r.Key, r.ID }

// rows builds n rows with real, lexically ordered ULID keys.
//
// Genuine ULIDs and not "01, 02, 03": the codec treats a key as an opaque string, so a fixture of
// short decimal strings would pass every assertion here while proving nothing about the 26-character
// Crockford base32 values a real listing carries — including that the plaintext leading key keeps
// tokens lexically ordered (core.CursorCodec.Encode).
func rows(t *testing.T, n int) []row {
	t.Helper()

	out := make([]row, 0, n)

	for i := range n {
		id := core.ULID(fmt.Sprintf("01JZ0RAW%018d", i))
		require.True(t, id.Valid(), "fixture key %q is not a ULID", id)

		out = append(out, row{Key: id, ID: id})
	}

	return out
}

// requireProblem asserts an error is a ProblemDetail with the given status and code.
//
// Both, always: a 422 that carries the right code and a 400 that carries the wrong one are different
// bugs, and asserting one of the two lets the other through.
func requireProblem(t *testing.T, err error, status int, code Code) {
	t.Helper()

	var p *ProblemDetail

	require.ErrorAs(t, err, &p, "expected a problem+json error, got %v", err)
	require.Equal(t, status, p.Status)
	require.Equal(t, code, p.Code)
}

// TestPage_Empty_SerialisesItemsAsAnArray pins the wire shape of an empty page.
//
// `"items": null` is what a nil slice marshals to, and it is a shape every bot author has to
// special-case before ranging over the result. The envelope promises `[]`.
func TestPage_Empty_SerialisesItemsAsAnArray(t *testing.T) {
	t.Parallel()

	page, err := NewPage(testPaginator(t), PageRequest{Limit: DefaultPageLimit}, []row{}, rowKey)
	require.NoError(t, err)

	encoded, err := json.Marshal(page)
	require.NoError(t, err)

	require.JSONEq(t, `{"items":[],"next_cursor":null,"has_more":false}`, string(encoded))
}

// TestPaginator_Limit_DefaultsAndClamps holds the one page-size contract at the helper.
//
// Huma applies the `default` and `maximum` tags to a request that reaches it through the router, so
// this is the second half: a handler called from a test, or from a future internal caller, arrives
// with whatever integer it was given, and an unbounded page there is the same statement-budget
// problem as an unbounded page from the wire.
func TestPaginator_Limit_DefaultsAndClamps(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   int
		want int
	}{
		{"unset takes the default", 0, DefaultPageLimit},
		{"negative takes the default", -7, DefaultPageLimit},
		{"one is the smallest page", 1, MinPageLimit},
		{"the default passes through", DefaultPageLimit, DefaultPageLimit},
		{"the cap passes through", MaxPageLimit, MaxPageLimit},
		{"one over the cap clamps", MaxPageLimit + 1, MaxPageLimit},
		{"absurd clamps", 100_000, MaxPageLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req, err := testPaginator(t).Request(ListParams{Limit: tc.in}, testClass, testFilter)
			require.NoError(t, err)
			require.Equal(t, tc.want, req.Limit)
		})
	}
}

// TestPageRequest_Fetch_IsOneMoreThanTheLimit is how `has_more` is answered without a second query.
func TestPageRequest_Fetch_IsOneMoreThanTheLimit(t *testing.T) {
	t.Parallel()

	req, err := testPaginator(t).Request(ListParams{Limit: 50}, testClass, testFilter)
	require.NoError(t, err)
	require.Equal(t, 51, req.Fetch())
}

// TestPaginator_Request_NoCursor_StartsAtTheBeginning: an absent cursor is the first page, not an
// error and not a decode of the empty string.
func TestPaginator_Request_NoCursor_StartsAtTheBeginning(t *testing.T) {
	t.Parallel()

	req, err := testPaginator(t).Request(ListParams{}, testClass, testFilter)
	require.NoError(t, err)
	require.False(t, req.HasCursor)
	require.Equal(t, core.Cursor{}, req.After)
}

// TestPaginator_RoundTrip_ResumesAfterTheLastReturnedRow is the whole mechanism, end to end.
//
// Over-fetch, trim, mint, and present the minted token back: the decoded position must be the last
// row the client actually SAW. Minting from the trimmed row instead — the obvious off-by-one — hands
// the caller a position past a row it was never sent, and that row is then silently missing from the
// walk with no error anywhere.
func TestPaginator_RoundTrip_ResumesAfterTheLastReturnedRow(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	req, err := p.Request(ListParams{Limit: 3}, testClass, testFilter)
	require.NoError(t, err)

	fetched := rows(t, req.Fetch())
	require.Len(t, fetched, 4)

	page, err := NewPage(p, req, fetched, rowKey)
	require.NoError(t, err)

	require.Len(t, page.Items, 3, "the page must be trimmed to the requested limit")
	require.True(t, page.HasMore, "a full over-fetch means there is another page")
	require.NotNil(t, page.NextCursor)

	next, err := p.Request(ListParams{Limit: 3, Cursor: *page.NextCursor}, testClass, testFilter)
	require.NoError(t, err)
	require.True(t, next.HasCursor)
	require.Equal(t, fetched[2].Key, next.After.Key,
		"the cursor must resume after the last row the client was sent, not after the trimmed one")
	require.Equal(t, fetched[2].ID, next.After.ID)
}

// TestNewPage_ExactPage_HasNoNextCursor: a query that returned exactly `limit` rows is the end.
//
// The over-fetch is what distinguishes "a full page" from "a full page and more behind it", so a
// helper that minted a cursor whenever the page was full would give every walk one extra round trip
// returning nothing — and `has_more: true` on the last page, which is the field bots loop on.
func TestNewPage_ExactPage_HasNoNextCursor(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	req, err := p.Request(ListParams{Limit: 3}, testClass, testFilter)
	require.NoError(t, err)

	page, err := NewPage(p, req, rows(t, 3), rowKey)
	require.NoError(t, err)

	require.Len(t, page.Items, 3)
	require.False(t, page.HasMore)
	require.Nil(t, page.NextCursor)
}

// TestPaginator_Request_TamperedCursor_IsCursorInvalid: a token this server did not sign.
func TestPaginator_Request_TamperedCursor_IsCursorInvalid(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	req, err := p.Request(ListParams{Limit: 2}, testClass, testFilter)
	require.NoError(t, err)

	page, err := NewPage(p, req, rows(t, 3), rowKey)
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)

	// Flip the last character of the signature.
	token := *page.NextCursor
	flipped := token[:len(token)-1] + "A"

	if flipped == token {
		flipped = token[:len(token)-1] + "B"
	}

	_, err = p.Request(ListParams{Limit: 2, Cursor: flipped}, testClass, testFilter)
	requireProblem(t, err, http.StatusBadRequest, CodeCursorInvalid)
}

// TestPaginator_Request_DifferentFilter_IsCursorFilterMismatch is the recoverable refusal.
//
// It has its OWN code because it means something different to a bot: the token is genuine and the
// caller is entitled to it, they just changed the filters mid-scroll, and restarting the scan fixes
// it. Collapsing it into `cursor_invalid` would tell an author their cursor handling is broken when
// their query is what changed. The alternative — honouring it — returns silently wrong rows.
func TestPaginator_Request_DifferentFilter_IsCursorFilterMismatch(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	req, err := p.Request(ListParams{Limit: 2}, testClass, testFilter)
	require.NoError(t, err)

	page, err := NewPage(p, req, rows(t, 3), rowKey)
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)

	_, err = p.Request(ListParams{Limit: 2, Cursor: *page.NextCursor}, testClass, otherFilter)
	requireProblem(t, err, http.StatusBadRequest, CodeCursorFilterMismatch)
}

// TestPaginator_Request_ForeignPrincipalClass_IsCursorInvalid: crossing an authorization boundary
// gets the OPAQUE code, never the informative one.
//
// The distinction matters in exactly one direction. `cursor_filter_mismatch` says "this is a real
// cursor, aimed elsewhere" — which, answered to a principal presenting a class they were not issued,
// confirms the token is genuine. `cursor_invalid` says nothing. core.Cursor.Decode checks the class
// first for this reason; this test is what stops the API layer undoing it.
func TestPaginator_Request_ForeignPrincipalClass_IsCursorInvalid(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	req, err := p.Request(ListParams{Limit: 2}, testClass, testFilter)
	require.NoError(t, err)

	page, err := NewPage(p, req, rows(t, 3), rowKey)
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)

	// Another class AND another filter: the class must win, so this cannot answer filter-mismatch.
	_, err = p.Request(ListParams{Limit: 2, Cursor: *page.NextCursor}, otherClass, otherFilter)
	requireProblem(t, err, http.StatusBadRequest, CodeCursorInvalid)
}

// TestPaginator_Request_ForeignInstanceCursor_IsCursorInvalid: a token signed by a different
// instance's key. This is the property the HMAC exists for, checked through the helper a handler
// actually calls.
func TestPaginator_Request_ForeignInstanceCursor_IsCursorInvalid(t *testing.T) {
	t.Parallel()

	foreignCodec, err := core.NewCursorCodec([]byte("a-completely-different-instance!!"))
	require.NoError(t, err)

	token, err := foreignCodec.Encode(core.Cursor{
		Key:            core.ULID("01JZ0RAW000000000000000001"),
		ID:             core.ULID("01JZ0RAW000000000000000001"),
		Filter:         testFilter,
		PrincipalClass: testClass,
	})
	require.NoError(t, err)

	_, err = testPaginator(t).Request(ListParams{Cursor: token}, testClass, testFilter)
	requireProblem(t, err, http.StatusBadRequest, CodeCursorInvalid)
}

// TestPaginator_NoCodec_RefusesRatherThanMintingUnsignedCursors.
//
// An unsigned cursor is a client-editable WHERE clause. The zero Paginator is the shape api.New
// produces when Config.Cursors was never wired, and it has to look like an outage rather than like a
// working endpoint — 503, on both the read side and the mint side.
func TestPaginator_NoCodec_RefusesRatherThanMintingUnsignedCursors(t *testing.T) {
	t.Parallel()

	unwired := NewPaginator(nil)

	t.Run("a presented cursor is refused", func(t *testing.T) {
		t.Parallel()

		_, err := unwired.Request(ListParams{Cursor: "anything"}, testClass, testFilter)
		requireProblem(t, err, http.StatusServiceUnavailable, CodeServiceUnavailable)
	})

	t.Run("minting the next cursor is refused", func(t *testing.T) {
		t.Parallel()

		req, err := unwired.Request(ListParams{Limit: 2}, testClass, testFilter)
		require.NoError(t, err, "the first page needs no codec until there is a next one to mint")

		_, err = NewPage(unwired, req, rows(t, 3), rowKey)
		requireProblem(t, err, http.StatusServiceUnavailable, CodeServiceUnavailable)
	})

	t.Run("a page with nothing behind it still serves", func(t *testing.T) {
		t.Parallel()

		req, err := unwired.Request(ListParams{Limit: 5}, testClass, testFilter)
		require.NoError(t, err)

		page, err := NewPage(unwired, req, rows(t, 2), rowKey)
		require.NoError(t, err, "a last page mints no cursor, so it needs no key")
		require.False(t, page.HasMore)
	})
}

// TestPaginator_UnboundListing_IsAServerFault: a handler that never named a principal class.
//
// It is 503 and not 400 because the caller did nothing wrong and can do nothing about it. Reporting
// it as a bad request would send a bot author hunting their own cursor handling for a binding the
// server forgot to declare.
func TestPaginator_UnboundListing_IsAServerFault(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	t.Run("with no cursor", func(t *testing.T) {
		t.Parallel()

		_, err := p.Request(ListParams{}, "", testFilter)
		requireProblem(t, err, http.StatusServiceUnavailable, CodeServiceUnavailable)
	})

	t.Run("with a cursor", func(t *testing.T) {
		t.Parallel()

		_, err := p.Request(ListParams{Cursor: "anything"}, "", testFilter)
		requireProblem(t, err, http.StatusServiceUnavailable, CodeServiceUnavailable)
	})

	t.Run("minting one", func(t *testing.T) {
		t.Parallel()

		_, err := NewPage(p, PageRequest{Limit: 1, filter: testFilter}, rows(t, 2), rowKey)
		requireProblem(t, err, http.StatusServiceUnavailable, CodeServiceUnavailable)
	})
}

// TestPaginator_Request_RebindsClassAndFilter: a PageRequest carries the binding it was verified
// under, so NewPage cannot mint the next cursor for a different one.
//
// This is why class and filter are unexported fields of PageRequest rather than two more arguments
// to NewPage. Passing them twice is two chances to pass a different pair the second time, and a
// cursor minted under a filter the request was not checked against is a cursor that will be honoured
// for a query it does not describe.
func TestPaginator_Request_RebindsClassAndFilter(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	req, err := p.Request(ListParams{Limit: 1}, testClass, testFilter)
	require.NoError(t, err)

	page, err := NewPage(p, req, rows(t, 2), rowKey)
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)

	// The minted token verifies under exactly the pair Request was given, and nothing else.
	codec, err := core.NewCursorCodec([]byte("a-thirty-two-byte-instance-key!!"))
	require.NoError(t, err)

	_, err = codec.Decode(*page.NextCursor, testClass, testFilter)
	require.NoError(t, err)

	_, err = codec.Decode(*page.NextCursor, testClass, otherFilter)
	require.True(t, errors.Is(err, core.ErrCursorFilterMismatch))

	_, err = codec.Decode(*page.NextCursor, otherClass, testFilter)
	require.True(t, errors.Is(err, core.ErrCursorInvalid))
}

// TestNewPage_HasMoreAndNextCursor_AgreeAlways is the invariant a client's loop rests on.
//
// `has_more: true` with a null `next_cursor` is a dead end — the bot is told there is another page
// and handed no way to ask for it — and `has_more: false` with a token is a walk that never ends.
// Neither is reachable, and the matrix is what says so rather than the one happy path above.
func TestNewPage_HasMoreAndNextCursor_AgreeAlways(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	for _, limit := range []int{1, 2, 3, 7} {
		for _, available := range []int{0, 1, 2, 3, 8, 20} {
			t.Run(fmt.Sprintf("limit=%d/rows=%d", limit, available), func(t *testing.T) {
				t.Parallel()

				req, err := p.Request(ListParams{Limit: limit}, testClass, testFilter)
				require.NoError(t, err)

				// What the query would return for req.Fetch(): everything available, capped at the
				// over-fetch.
				fetched := rows(t, min(available, req.Fetch()))

				page, err := NewPage(p, req, fetched, rowKey)
				require.NoError(t, err)

				require.Equal(t, page.HasMore, page.NextCursor != nil,
					"has_more and next_cursor disagree: has_more=%v next_cursor=%v",
					page.HasMore, page.NextCursor)
				require.LessOrEqual(t, len(page.Items), limit, "the page exceeded the limit")
				require.NotNil(t, page.Items, "items must never be nil")

				if page.HasMore {
					require.Len(t, page.Items, limit,
						"a page with more behind it must be full")
				}
			})
		}
	}
}

// TestNewPage_ZeroLimitRequest_DoesNotStrandTheCaller.
//
// A PageRequest built by literal rather than through Request carries Limit 0. Read raw, that trims
// every row away while still reporting `has_more`, which is an empty page with no way onward — so
// NewPage clamps too, and this is the test that keeps the second clamp from being tidied away as
// redundant.
func TestNewPage_ZeroLimitRequest_DoesNotStrandTheCaller(t *testing.T) {
	t.Parallel()

	p := testPaginator(t)

	page, err := NewPage(p, PageRequest{class: testClass, filter: testFilter}, rows(t, 3), rowKey)
	require.NoError(t, err)

	require.Len(t, page.Items, 3, "a zero limit must fall back to the default page size, not to zero")
	require.False(t, page.HasMore)
	require.Nil(t, page.NextCursor)
}
