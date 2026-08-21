package kinds

import (
	"fmt"
	"strings"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// Package kinds is the feed_token enum catalogue — canonical §5's "one Go catalogue" for
// feed_token.kind.
//
// ITS OWN PACKAGE for the constraint .claude/rules/migrations.md states: one region per catalogue,
// matched by the schemaEnumBegin/schemaEnumEnd identifier pair, so one package cannot own two
// regions.
//
// A LEAF PACKAGE: standard library and internal/schemaenum only, never internal/auth. See the note
// in internal/auth/appuser/kinds.
//
// WHY A FEED TOKEN IS NOT A PAT (ADR-0011, docs/design/03-security.md §6.3). Calendar clients cannot
// set headers, so a feed credential has to travel in the path — and a credential in a URL ends up in
// proxy logs, browser history and a shared Google Calendar. The answer is that it is a DIFFERENT
// CLASS of credential rather than a PAT with a relaxed transport: single-purpose, read-only, scoped
// to one feed kind, independently revocable, and carrying no scopes column at all. This is the
// column that makes "single-purpose" true — one token answers for one feed and nothing else.
//
// THE TABLE SHIPS AHEAD OF ITS READER, deliberately. Wave 0d builds the credential layer; the feed
// routes (/feeds/{feed_token}/raids.ics) land with the calendar and article surfaces. The vocabulary
// is docs/design/01-domain-model.md §4.3's and is not in doubt, and adding a value to a CHECK later
// is a 12-step table rebuild — so the four values ship now and the routes arrive against a schema
// that already fits them.

// ErrSchemaMarkersMissing reports that db/schema.hcl no longer carries the generated-region markers
// RenderSchemaHCL rewrites between. It IS schemaenum.ErrMarkersMissing rather than a second sentinel
// wrapping it: one condition, one value.
var ErrSchemaMarkersMissing = schemaenum.ErrMarkersMissing

// The feed_token.kind vocabulary (docs/design/01-domain-model.md §4.3): WHICH FEED this token
// answers for, and the whole of what it may read.
//
//   - RaidsICal     the raid schedule as iCalendar, for a phone's calendar app.
//   - CalendarICal  guild events as iCalendar — the portal calendar, not the raid list.
//   - StandingsRSS  the standings table as a feed.
//   - ArticlesRSS   news articles from the CMS.
//
// NONE OF THEM CARRIES AN EMAIL ADDRESS (§6.3), which is a property of the renderers rather than of
// this list — but the list is what bounds how many renderers have to honour it.
const (
	KindRaidsICal    = "raids_ical"
	KindCalendarICal = "calendar_ical"
	KindStandingsRSS = "standings_rss"
	KindArticlesRSS  = "articles_rss"
)

// Kinds returns every legal feed_token.kind, in the order the CHECK constraint carries them. A FRESH
// SLICE from a function, never a package-level var; the order is fixed because CheckExpr renders in
// it, and reordering rewrites the CHECK.
func Kinds() []string {
	return []string{
		KindRaidsICal,
		KindCalendarICal,
		KindStandingsRSS,
		KindArticlesRSS,
	}
}

// IsKind reports whether v is a legal feed_token.kind — the runtime half, so the first feed route
// refuses an unknown kind with a Go error rather than having SQLite name a constraint from inside a
// write transaction.
func IsKind(v string) bool {
	for _, candidate := range Kinds() {
		if candidate == v {
			return true
		}
	}

	return false
}

// The column this catalogue governs. Unexported: KindCheckExpr is the only thing that needs to name
// it.
const kindColumn = "kind"

// KindCheckExpr renders the body of feed_token's kind CHECK constraint:
//
//	kind IN ('raids_ical', 'calendar_ical', 'standings_rss', 'articles_rss')
func KindCheckExpr() string {
	return schemaenum.CheckExpr(kindColumn, Kinds())
}

// The markers delimiting this catalogue's generated region of db/schema.hcl, inside `table
// "feed_token"`. HCL line comments, so Atlas parses the file unchanged; the marker text names the
// catalogue because each region is found by an exact whole-line match on ITS OWN markers.
const (
	schemaEnumBegin = "  // BEGIN GENERATED — feed_token enum CHECK, from internal/auth/feedtoken/kinds. Run `make gen`."
	schemaEnumEnd   = "  // END GENERATED — feed_token enum CHECK."
)

func schemaRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   schemaEnumBegin,
		End:     schemaEnumEnd,
		Subject: "the feed_token kind CHECK",
	}
}

// SchemaEnumBlock renders the generated region of db/schema.hcl, markers included, indented to sit
// inside `table "feed_token"`. No trailing newline: Replace joins it back into the file's line
// stream.
func SchemaEnumBlock() string {
	return strings.Join([]string{
		schemaEnumBegin,
		"  //",
		"  // Canonical §5: the wire value is the database value, and both the CHECK and the OpenAPI",
		"  // enum are generated from one Go catalogue. Adding a value here by hand is drift that",
		"  // TestFeedTokenKinds_CheckMatchesCatalogue fails on.",
		`  check "feed_token_kind_enum" {`,
		fmt.Sprintf(`    expr = %q`, KindCheckExpr()),
		"  }",
		schemaEnumEnd,
	}, "\n")
}

// RenderSchemaHCL returns src with this catalogue's generated region replaced by SchemaEnumBlock().
// Idempotent, and it touches only this catalogue's region.
func RenderSchemaHCL(src string) (string, error) {
	return schemaRegion().Replace(src, SchemaEnumBlock())
}
