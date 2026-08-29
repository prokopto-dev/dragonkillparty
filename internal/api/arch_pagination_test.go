package api

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/require"
)

// The pagination gate: every collection this API serves uses the ONE shared envelope, and nothing
// else in the document is allowed to describe a second way to walk a list.
//
// docs/design/02-api-design.md's gate table names it ("every collection uses the shared envelope;
// `?since_seq=` appears only on the three permitted prefixes"), .claude/rules/api-endpoints.md's
// arch_test.go table names it, and until this file neither was true. It lives beside arch_test.go
// rather than inside it because it is a self-contained rule with its own fixture set, and because
// two Wave 1 branches are editing that file for unrelated reasons.
//
// WHY A GATE AND NOT A CONVENTION. A bespoke list shape is the cheapest mistake in the repository to
// make and the most expensive to withdraw: `{results, page, total}` on one resource compiles, passes
// review as "it's just a list", ships into two generated SDKs and a documented contract, and then
// cannot be removed inside v1 (canonical §7 — the surface is additive only). The envelope has to be
// enforced at the first collection, not at the fortieth, which is the same argument arch_test.go
// makes for itself while the registry still holds two operations.
//
// It runs over the real registry (vacuously today — there are no collections yet) and over fixtures
// that prove each branch fires. The fixtures are the load-bearing half until the first collection
// lands: a gate nobody has seen go red is a gate nobody knows works.

// envelopeProperties are the three keys the shared envelope is made of. All three are required.
func envelopeProperties() []string { return []string{"items", "next_cursor", "has_more"} }

// envelopeExtras are the additional keys a collection MAY carry, and the complete list of them.
//
// `total` and `total_estimated` answer `?include_total=true`; `head_seq` and `server_time` are what
// the `?since_seq=` catch-up loop needs to report "behind by 14 batches" instead of guessing
// (docs/api/pagination-and-sync.md). Anything outside this set plus envelopeProperties is a second
// pagination contract being published, whatever it is called.
func envelopeExtras() []string {
	return []string{"total", "total_estimated", "head_seq", "server_time"}
}

// bannedPaginationParams are query parameters that describe an offset-based walk.
//
// There is no `?offset=` and no `?page=` anywhere in this API and there never will be inside v1
// (docs/api/pagination-and-sync.md, "Why there is no offset"). Offset pagination shifts under
// concurrent inserts on an append-heavy ledger: rows appear twice or never, and the symptom is a
// member's balance being slightly wrong weeks later rather than an error anybody sees.
func bannedPaginationParams() []string {
	return []string{
		"offset", "page", "page_num", "page_number", "page_size", "per_page", "pagesize",
		"skip", "page_token", "pagetoken", "start_index",
	}
}

// bespokeBodyKeys are response-body property names that mean "this is a paginated list" in some
// other API's dialect.
//
// Their presence is what makes a body pagination-shaped even when it never says `items`, so
// `{results: [...], page: 1, total_pages: 9}` is caught by the envelope check below rather than
// sailing past it as an ordinary DTO. `cursor` is here because the envelope's key is `next_cursor`:
// a bare `cursor` in a response is a second spelling of the same thing.
func bespokeBodyKeys() []string {
	return []string{
		"cursor", "next", "prev", "previous", "next_page", "prev_page", "previous_page",
		"page", "pages", "page_size", "per_page", "offset", "total_pages", "total_count",
		"pagination", "paging", "links", "page_token", "next_page_token", "has_next",
		// camelCase spellings of the envelope's own keys: the wire is snake_case (canonical §2), so
		// these are a hand-rolled envelope rather than the shared one.
		"nextCursor", "hasMore",
	}
}

// sinceSeqAllowedPrefixes are the only three places `?since_seq=` is a valid parameter.
//
// Canonical §4 and docs/api/pagination-and-sync.md. `since_seq` is incremental SYNC, not pagination,
// and it is meaningful only where a gapless sequence exists to sync on: the per-pool ledger `seq`,
// the audit log's own `seq`, and the global outbox `event_seq`. `/raids` and `/awards` have no `seq`
// column at all, and adjustments project over per-pool batches, so a single global `since_seq`
// across pools would return a wrong answer rather than an error — which is exactly why the allowlist
// is a gate and not a docs paragraph.
func sinceSeqAllowedPrefixes() []string {
	return []string{BasePath + "/ledger", BasePath + "/audit", BasePath + "/events/replay"}
}

// TestArch_Collections_UseTheSharedEnvelope is the gate over the operations this binary serves.
//
// It is vacuous today: the registry holds `/meta` and `/guild`, neither of which is a collection. It
// is here anyway and it is not decoration — the moment the first `GET /api/v1/raids` is registered
// this becomes the assertion that its response is `{items, next_cursor, has_more}` and not whatever
// its author reached for. TestArch_BespokePagination_FailsBuild is what proves the check itself
// works in the meantime.
func TestArch_Collections_UseTheSharedEnvelope(t *testing.T) {
	t.Parallel()

	require.Empty(t, paginationViolations(registeredDocument(t)))
}

// paginationViolations returns one message per pagination defect in doc.
//
// Extracted rather than inlined into the test for the reason arch_test.go's other gates are: the
// fixture tests below drive THIS function, so a check that is subtly wrong fails there instead of
// passing quietly over a registry that has nothing for it to read.
func paginationViolations(doc *huma.OpenAPI) []string {
	var problems []string

	for _, op := range operationsOf(doc) {
		problems = append(problems, paramViolations(op)...)
		problems = append(problems, bodyViolations(doc, op)...)
	}

	return problems
}

// paramViolations checks an operation's query parameters: no offset dialect, a `limit` that matches
// the published contract, and `since_seq` only where a sequence exists to sync on.
func paramViolations(op registeredOp) []string {
	var problems []string

	params := queryParams(op)

	for _, banned := range bannedPaginationParams() {
		if _, ok := params[banned]; ok {
			problems = append(problems, fmt.Sprintf(
				"%s declares a `%s` query parameter. This API is cursor-paginated only: offset walks "+
					"skip and duplicate rows under concurrent inserts, which is invisible until a "+
					"balance is wrong weeks later. Embed api.ListParams and page with `cursor`.",
				op, banned))
		}
	}

	if limit, ok := params["limit"]; ok {
		problems = append(problems, limitViolations(op, limit)...)
	}

	if _, ok := params["since_seq"]; ok {
		allowed := slices.ContainsFunc(sinceSeqAllowedPrefixes(),
			func(p string) bool { return strings.HasPrefix(op.Path, p) })
		if !allowed {
			problems = append(problems, fmt.Sprintf(
				"%s declares `?since_seq=`, which is valid only under %s (canonical §4). Everywhere "+
					"else there is no gapless sequence to sync on, so the parameter would return a "+
					"wrong answer rather than an error. Use the opaque cursor.",
				op, strings.Join(sinceSeqAllowedPrefixes(), ", ")))
		}
	}

	return problems
}

// limitViolations checks that a declared `limit` publishes the one page-size contract.
//
// The default and the cap reach both generated SDKs and the reference documentation, so a resource
// that quietly caps at 1000 is not a local choice — it is a second, contradictory contract with the
// same name, and the first person to find it is a bot author whose loop behaves differently on one
// endpoint than on every other.
func limitViolations(op registeredOp, limit *huma.Param) []string {
	var problems []string

	if limit.Schema == nil {
		return []string{fmt.Sprintf("%s declares `limit` with no schema, so it publishes no bounds.", op)}
	}

	want := func(name string, got float64, ok bool, expect int) {
		if !ok || got != float64(expect) {
			problems = append(problems, fmt.Sprintf(
				"%s declares `limit` with %s %s, want %d. The page-size contract is one contract: "+
					"embed api.ListParams instead of retyping the tags (api.DefaultPageLimit, "+
					"api.MinPageLimit, api.MaxPageLimit).",
				op, name, describeBound(got, ok), expect))
		}
	}

	def, defOK := numeric(limit.Schema.Default)
	want("default", def, defOK, DefaultPageLimit)

	minimum, minOK := ptrValue(limit.Schema.Minimum)
	want("minimum", minimum, minOK, MinPageLimit)

	maximum, maxOK := ptrValue(limit.Schema.Maximum)
	want("maximum", maximum, maxOK, MaxPageLimit)

	return problems
}

// bodyViolations checks an operation's success response body against the shared envelope.
func bodyViolations(doc *huma.OpenAPI, op registeredOp) []string {
	var problems []string

	if hasLinkHeader(op) {
		problems = append(problems, fmt.Sprintf(
			"%s declares a `Link` response header. Pagination lives in the body envelope; bots parse "+
				"JSON far more reliably than RFC 8288 (docs/api/pagination-and-sync.md).", op))
	}

	body, ok := successBody(doc, op)
	if !ok {
		return problems
	}

	// A top-level JSON array is a bespoke envelope with no room to grow: there is nowhere to put
	// `has_more`, so the only way to add pagination later is a breaking change.
	if body.Type == "array" {
		return append(problems, fmt.Sprintf(
			"%s returns a bare JSON array. Every collection returns the object envelope "+
				"{items, next_cursor, has_more} — an array has nowhere to carry `has_more`, so "+
				"paginating it later is a breaking change. Return api.ListOutput[T].", op))
	}

	if body.Type != "" && body.Type != "object" {
		return problems
	}

	names := slices.Sorted(maps.Keys(body.Properties))

	paginationShaped := slices.Contains(names, "items") ||
		slices.ContainsFunc(names, func(n string) bool { return slices.Contains(bespokeBodyKeys(), n) })

	// An operation that declares `cursor` or `limit` is paginated by declaration, whatever its body
	// happens to look like — this is the branch that catches a paginated operation whose response
	// forgot the envelope entirely.
	params := queryParams(op)
	if _, ok := params["cursor"]; ok {
		paginationShaped = true
	}

	if _, ok := params["limit"]; ok {
		paginationShaped = true
	}

	if !paginationShaped {
		return problems
	}

	return append(problems, envelopeViolations(op, body, names, params)...)
}

// envelopeViolations checks a pagination-shaped body against the one permitted shape.
func envelopeViolations(op registeredOp, body *huma.Schema, names []string, params map[string]*huma.Param) []string {
	var problems []string

	allowed := append(envelopeProperties(), envelopeExtras()...)

	for _, name := range names {
		if !slices.Contains(allowed, name) {
			problems = append(problems, fmt.Sprintf(
				"%s returns a list body carrying `%s`, which is not part of the shared envelope. A "+
					"collection returns {items, next_cursor, has_more}, optionally with %s. Use "+
					"api.ListOutput[T].", op, name, strings.Join(envelopeExtras(), ", ")))
		}
	}

	for _, want := range envelopeProperties() {
		if !slices.Contains(names, want) {
			problems = append(problems, fmt.Sprintf(
				"%s returns a list body with no `%s`. The envelope is {items, next_cursor, has_more} "+
					"and all three are required: `has_more` is what a client loops on, and a short "+
					"page is not the end of the collection. Use api.ListOutput[T].", op, want))
		}
	}

	if items := body.Properties["items"]; items != nil {
		if items.Type != "" && items.Type != "array" {
			problems = append(problems, fmt.Sprintf(
				"%s returns `items` typed %q rather than an array.", op, items.Type))
		}

		// Huma makes every Go slice a nullable array by default, so a hand-rolled envelope publishes
		// `items: T[] | null` and every SDK consumer inherits a null check for a case that never
		// happens. api.Page carries `nullable:"false"`; this is what makes reaching for it the only
		// way to get the published shape right.
		if items.Nullable {
			problems = append(problems, fmt.Sprintf(
				"%s publishes a nullable `items`. An empty page is `[]`, never null — tag the field "+
					"`nullable:\"false\"`, or use api.ListOutput[T], which already does.", op))
		}
	}

	if more := body.Properties["has_more"]; more != nil && more.Type != "" && more.Type != "boolean" {
		problems = append(problems, fmt.Sprintf(
			"%s returns `has_more` typed %q rather than a boolean.", op, more.Type))
	}

	// The reverse direction: a body that IS the envelope, on an operation nobody can page through.
	// One page of at most 200 rows, no way to ask for the next, and the missing rows are silent.
	for _, want := range []string{"cursor", "limit"} {
		if _, ok := params[want]; !ok {
			problems = append(problems, fmt.Sprintf(
				"%s returns a list envelope but declares no `%s` query parameter, so a caller cannot "+
					"walk past the first page and the rows beyond it are silently unreachable. Embed "+
					"api.ListParams in its input struct.", op, want))
		}
	}

	return problems
}

// successBody resolves the JSON schema of an operation's success response, following the `$ref` into
// the document's schema registry.
//
// Huma registers a response body as a reference to a named component, so reading
// `Responses["200"].Content["application/json"].Schema.Properties` directly finds nothing at all —
// which would make every check above pass on every operation. The `$ref` hop is the whole reason
// this gate takes the document rather than the operation.
func successBody(doc *huma.OpenAPI, op registeredOp) (*huma.Schema, bool) {
	if op.Op.Responses == nil {
		return nil, false
	}

	for _, status := range slices.Sorted(maps.Keys(op.Op.Responses)) {
		if !strings.HasPrefix(status, "2") {
			continue
		}

		resp := op.Op.Responses[status]
		if resp == nil || resp.Content == nil {
			continue
		}

		media := resp.Content["application/json"]
		if media == nil || media.Schema == nil {
			continue
		}

		if schema := resolveSchema(doc, media.Schema); schema != nil {
			return schema, true
		}
	}

	return nil, false
}

// resolveSchema follows one `$ref` hop into the document's registry.
//
// One hop and not a loop: Huma names every response body component, so a body schema is either
// inline or a single reference to one. A chain would mean a shape nothing in this repository
// produces, and following it silently would hide that.
func resolveSchema(doc *huma.OpenAPI, s *huma.Schema) *huma.Schema {
	if s == nil || s.Ref == "" {
		return s
	}

	if doc.Components == nil || doc.Components.Schemas == nil {
		return nil
	}

	return doc.Components.Schemas.SchemaFromRef(s.Ref)
}

// queryParams indexes an operation's query parameters by name.
func queryParams(op registeredOp) map[string]*huma.Param {
	out := map[string]*huma.Param{}

	for _, p := range op.Op.Parameters {
		if p != nil && p.In == "query" {
			out[p.Name] = p
		}
	}

	return out
}

// hasLinkHeader reports whether any response declares an RFC 8288 `Link` header.
func hasLinkHeader(op registeredOp) bool {
	for _, resp := range op.Op.Responses {
		if resp == nil {
			continue
		}

		for name := range resp.Headers {
			if strings.EqualFold(name, "Link") {
				return true
			}
		}
	}

	return false
}

// numeric coerces a schema `default` to a number.
//
// Huma builds it by unmarshalling the struct tag as JSON, so an integer default arrives as a
// float64 — but the field's static type is `any`, and a check that type-asserted to one
// representation would silently accept every default the day that changed. The switch is the
// cheaper half of the bargain.
func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// ptrValue reads an optional numeric schema bound.
func ptrValue(p *float64) (float64, bool) {
	if p == nil {
		return 0, false
	}

	return *p, true
}

// describeBound renders a bound for a failure message, distinguishing absent from wrong.
func describeBound(v float64, ok bool) string {
	if !ok {
		return "unset"
	}

	return fmt.Sprintf("%g", v)
}

// ---------------------------------------------------------------------------
// Fixtures. Every branch of the gate above is driven against one, because the
// real registry holds no collection yet and a check that never runs is a check
// nobody knows works.
// ---------------------------------------------------------------------------

// fixtureThingDTO is an ordinary resource representation: the thing a collection is a collection of.
type fixtureThingDTO struct {
	ID   string `json:"id"   format:"ulid"`
	Name string `json:"name"`
}

// fixtureThingOutput is a SINGLE resource — the control that stops the gate being "fixed" into
// something that fires on every operation in the document.
type fixtureThingOutput struct {
	Body fixtureThingDTO
}

// fixtureListInput is the shape every collection is supposed to have: api.ListParams, embedded.
type fixtureListInput struct {
	ListParams
}

// fixtureBespokeOutput is the page-and-total dialect: what an author reaches for from memory.
type fixtureBespokeOutput struct {
	Body struct {
		Results    []fixtureThingDTO `json:"results"`
		Page       int               `json:"page"`
		TotalPages int               `json:"total_pages"`
	}
}

// fixtureArrayOutput is a bare JSON array, the other common answer to "return a list".
type fixtureArrayOutput struct {
	Body []fixtureThingDTO
}

// fixtureCamelOutput is the envelope's own three keys, spelled the way a JavaScript author would.
type fixtureCamelOutput struct {
	Body struct {
		Items      []fixtureThingDTO `json:"items"`
		NextCursor *string           `json:"nextCursor"`
		HasMore    bool              `json:"hasMore"`
	}
}

// fixtureSyncOutput is the shape a `?since_seq=` collection needs: the envelope, embedded, plus the
// two catch-up keys docs/api/pagination-and-sync.md's loop reads.
type fixtureSyncOutput struct {
	Body struct {
		Page[fixtureThingDTO]

		HeadSeq    int64  `json:"head_seq"`
		ServerTime string `json:"server_time" format:"date-time"`
	}
}

// fixtureTotalOutput is the `?include_total=true` shape.
type fixtureTotalOutput struct {
	Body struct {
		Page[fixtureThingDTO]

		Total int64 `json:"total"`
	}
}

// fixtureNullableItemsOutput has the three right keys, hand-rolled — so `items` is a nullable array,
// which is Huma's default for a Go slice and the shape api.Page tags its way out of.
type fixtureNullableItemsOutput struct {
	Body struct {
		Items      []fixtureThingDTO `json:"items"`
		NextCursor *string           `json:"next_cursor"`
		HasMore    bool              `json:"has_more"`
	}
}

// fixtureLinkOutput carries an RFC 8288 Link header alongside a perfectly ordinary body.
type fixtureLinkOutput struct {
	Link string `header:"Link"`
	Body fixtureThingDTO
}

// fixtureOffsetInput is offset pagination, declared alongside a correct limit so the gate must catch
// the offset on its own rather than by way of a bad bound.
type fixtureOffsetInput struct {
	ListParams

	Offset int `query:"offset"`
}

// fixtureBadLimitInput retypes the page-size contract instead of embedding it.
type fixtureBadLimitInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"25" minimum:"1" maximum:"1000"`
}

// fixtureSinceSeqInput adds the incremental-sync parameter to a cursor-paginated collection.
type fixtureSinceSeqInput struct {
	ListParams

	SinceSeq int64 `query:"since_seq"`
}

// fixtureDoc registers one operation with the given input and output types into a throwaway
// registry and returns its document.
//
// Generic over both, unlike arch_test.go's fixtureRegistry, because this gate reads the RESPONSE
// SCHEMA and the QUERY PARAMETERS — the two things a fixture with one fixed I/O pair cannot vary.
// It builds through humaConfig(), the same configuration the served document uses, so a fixture and
// a real operation are described by the same schema namer and the same registry.
func fixtureDoc[I, O any](t *testing.T, op huma.Operation) *huma.OpenAPI {
	t.Helper()

	a := humago.New(http.NewServeMux(), humaConfig())

	huma.Register(a, op, func(_ context.Context, _ *I) (*O, error) { return nil, nil })

	doc := a.OpenAPI()
	require.NotEmpty(t, operationsOf(doc), "the fixture operation did not register")

	return doc
}

// listOp is a GET fixture operation at the given path.
func listOp(id, path string) huma.Operation {
	return huma.Operation{
		OperationID: id,
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "A fixture collection",
	}
}

// TestArch_SharedEnvelope_PassesItsOwnGate is the positive control, and it is the assertion this
// file exists to make first.
//
// api.ListParams plus api.ListOutput[T] — the helper AGENTS.md tells an author to use — must satisfy
// the gate that rejects everything else. If it ever does not, the gate is wrong rather than the
// helper, because every collection in the product will be built from these two types.
func TestArch_SharedEnvelope_PassesItsOwnGate(t *testing.T) {
	t.Parallel()

	doc := fixtureDoc[fixtureListInput, ListOutput[fixtureThingDTO]](t,
		listOp("listThings", BasePath+"/things"))

	require.Empty(t, paginationViolations(doc),
		"the shared envelope helper does not satisfy the gate that requires it")
}

// TestArch_EnvelopeExtras_AreAccepted holds the other half of the closed list.
//
// The four permitted extras are permitted, and they are reached by EMBEDDING Page[T] rather than by
// re-declaring the three keys beside them — which is what keeps "the envelope plus head_seq" from
// drifting into "an envelope-shaped struct somebody typed out". A gate that rejected these would
// make `?include_total=true` and the `?since_seq=` catch-up loop unwritable, and both are published
// contracts.
func TestArch_EnvelopeExtras_AreAccepted(t *testing.T) {
	t.Parallel()

	t.Run("head_seq and server_time", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[fixtureSinceSeqInput, fixtureSyncOutput](t,
			listOp("listEntries", BasePath+"/ledger/entries"))

		require.Empty(t, paginationViolations(doc))
	})

	t.Run("total", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[fixtureListInput, fixtureTotalOutput](t,
			listOp("listThings", BasePath+"/things"))

		require.Empty(t, paginationViolations(doc))
	})
}

// TestArch_SingleResource_IsNotTreatedAsACollection is the other control.
//
// A gate that fired on every operation would pass every negative test below while making the
// document unwritable. This is the assertion that the pagination rules apply to collections and to
// nothing else.
func TestArch_SingleResource_IsNotTreatedAsACollection(t *testing.T) {
	t.Parallel()

	doc := fixtureDoc[struct{}, fixtureThingOutput](t, listOp("getThing", BasePath+"/things/{id}"))

	require.Empty(t, paginationViolations(doc),
		"the pagination gate fired on a single-resource GET")
}

// TestArch_BespokePagination_FailsBuild drives every rejecting branch of the gate.
//
// One subtest per branch, each naming the shape it rejects, because a single "some violation was
// reported" assertion would stay green if four of the five branches stopped working.
func TestArch_BespokePagination_FailsBuild(t *testing.T) {
	t.Parallel()

	t.Run("a page-and-total envelope is rejected", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[fixtureListInput, fixtureBespokeOutput](t,
			listOp("listThings", BasePath+"/things"))

		requireViolation(t, paginationViolations(doc), "results",
			"a {results, page, total_pages} body was accepted as a list envelope")
	})

	t.Run("a bare JSON array is rejected", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[fixtureListInput, fixtureArrayOutput](t,
			listOp("listThings", BasePath+"/things"))

		requireViolation(t, paginationViolations(doc), "bare JSON array",
			"a top-level array response was accepted as a collection")
	})

	t.Run("camelCase envelope keys are rejected", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[fixtureListInput, fixtureCamelOutput](t,
			listOp("listThings", BasePath+"/things"))

		requireViolation(t, paginationViolations(doc), "nextCursor",
			"a camelCase spelling of the envelope was accepted")
	})

	t.Run("an offset parameter is rejected", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[fixtureOffsetInput, ListOutput[fixtureThingDTO]](t,
			listOp("listThings", BasePath+"/things"))

		requireViolation(t, paginationViolations(doc), "`offset` query parameter",
			"offset pagination was accepted")
	})

	t.Run("a retyped limit is rejected", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[fixtureBadLimitInput, ListOutput[fixtureThingDTO]](t,
			listOp("listThings", BasePath+"/things"))

		problems := paginationViolations(doc)
		requireViolation(t, problems, "default 25", "a limit defaulting to 25 was accepted")
		requireViolation(t, problems, "maximum 1000", "a limit capped at 1000 was accepted")
	})

	t.Run("an envelope nobody can page through is rejected", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[struct{}, ListOutput[fixtureThingDTO]](t,
			listOp("listThings", BasePath+"/things"))

		problems := paginationViolations(doc)
		requireViolation(t, problems, "no `cursor` query parameter",
			"a collection with no way to request the next page was accepted")
		requireViolation(t, problems, "no `limit` query parameter",
			"a collection with no page-size parameter was accepted")
	})

	t.Run("a hand-rolled envelope with nullable items is rejected", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[fixtureListInput, fixtureNullableItemsOutput](t,
			listOp("listThings", BasePath+"/things"))

		requireViolation(t, paginationViolations(doc), "nullable `items`",
			"a hand-rolled envelope publishing `items: T[] | null` was accepted")
	})

	t.Run("a Link header is rejected", func(t *testing.T) {
		t.Parallel()

		doc := fixtureDoc[struct{}, fixtureLinkOutput](t, listOp("getThing", BasePath+"/things/{id}"))

		requireViolation(t, paginationViolations(doc), "`Link` response header",
			"RFC 8288 pagination headers were accepted")
	})
}

// TestArch_SinceSeq_IsAllowlistedToThreePrefixes holds canonical §4's closed list in both
// directions.
//
// The permitted half matters as much as the forbidden one: an allowlist that rejected `/ledger` too
// would pass every negative case here while making the three endpoints that need incremental sync
// unwritable, and the first person to find out would be whoever ships `/ledger/entries`.
func TestArch_SinceSeq_IsAllowlistedToThreePrefixes(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		BasePath + "/ledger/entries",
		BasePath + "/ledger/batches",
		BasePath + "/audit",
		BasePath + "/events/replay",
	} {
		t.Run("permitted on "+path, func(t *testing.T) {
			t.Parallel()

			doc := fixtureDoc[fixtureSinceSeqInput, ListOutput[fixtureThingDTO]](t,
				listOp("listThings", path))

			require.Empty(t, paginationViolations(doc),
				"`?since_seq=` was rejected on %s, where canonical §4 requires it", path)
		})
	}

	for _, path := range []string{
		BasePath + "/raids",
		BasePath + "/awards",
		BasePath + "/adjustments",
		BasePath + "/events",
	} {
		t.Run("forbidden on "+path, func(t *testing.T) {
			t.Parallel()

			doc := fixtureDoc[fixtureSinceSeqInput, ListOutput[fixtureThingDTO]](t,
				listOp("listThings", path))

			requireViolation(t, paginationViolations(doc), "`?since_seq=`",
				"`?since_seq=` was accepted on %s, which has no sequence to sync on", path)
		})
	}
}

// requireViolation asserts that some reported problem mentions want, and prints the whole set when
// none does — a bare "not empty" assertion would let one branch's message stand in for another's.
func requireViolation(t *testing.T, problems []string, want, msg string, args ...any) {
	t.Helper()

	require.True(t, slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) }),
		"%s\nno reported problem mentioned %q; reported:\n%s",
		fmt.Sprintf(msg, args...), want, strings.Join(problems, "\n"))
}
