package schemaenum_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prokopto-dev/dragonkillparty/internal/schemaenum"
)

// The mechanism's own tests. Each catalogue's suite proves its VALUES reach db/schema.hcl; these
// prove the two things every catalogue borrows from here behave — the CHECK renders one way, and a
// region rewrite touches nothing outside its own markers.
//
// The second is what makes a second catalogue safe. db/schema.hcl now carries two generated regions
// and `make gen` composes their renders over one file, so a Replace that reached past its markers
// would let one catalogue silently rewrite the other's CHECK, on a table it has no business naming.

// ledger and audit stand in for the two real regions. Their marker text is deliberately similar —
// same prefix, same shape — because a near-miss is what a whole-line match has to survive.
func ledgerRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   "  // BEGIN GENERATED — first CHECK, from a/one. Run `make gen`.",
		End:     "  // END GENERATED — first CHECK.",
		Subject: "the first CHECK",
	}
}

func auditRegion() schemaenum.Region {
	return schemaenum.Region{
		Begin:   "  // BEGIN GENERATED — second CHECK, from b/two. Run `make gen`.",
		End:     "  // END GENERATED — second CHECK.",
		Subject: "the second CHECK",
	}
}

func block(r schemaenum.Region, expr string) string {
	return strings.Join([]string{r.Begin, "  check {", "    expr = " + expr, "  }", r.End}, "\n")
}

// twoRegionFile is a miniature schema carrying both regions with hand-authored lines around, between
// and inside neither of them.
func twoRegionFile(first, second string) string {
	return strings.Join([]string{
		"// hand-authored header",
		"table \"one\" {",
		block(ledgerRegion(), first),
		"  check \"untouched\" {",
		"    expr = \"outcome IN ('success')\"",
		"  }",
		"}",
		"",
		"table \"two\" {",
		block(auditRegion(), second),
		"}",
		"",
	}, "\n")
}

func TestCheckExpr_RendersASQLInList(t *testing.T) {
	t.Parallel()

	require.Equal(t, "kind IN ('a', 'b_c')", schemaenum.CheckExpr("kind", []string{"a", "b_c"}))
	require.Equal(t, "source IN ('web')", schemaenum.CheckExpr("source", []string{"web"}))
}

// TestRegion_Replace_RewritesOnlyItsOwnRegion is the property a second catalogue depends on.
func TestRegion_Replace_RewritesOnlyItsOwnRegion(t *testing.T) {
	t.Parallel()

	src := twoRegionFile(`"stale one"`, `"stale two"`)

	out, err := auditRegion().Replace(src, block(auditRegion(), `"fresh two"`))
	require.NoError(t, err)

	require.Equal(t, twoRegionFile(`"stale one"`, `"fresh two"`), out,
		"replacing the second region changed something outside its markers")
}

// TestRegion_Replace_ComposesInEitherOrder pins the property that lets the generator apply the
// catalogues as a plain loop: each render is independent, so their composition does not depend on the
// order and adding a third catalogue cannot reorder the first two.
func TestRegion_Replace_ComposesInEitherOrder(t *testing.T) {
	t.Parallel()

	src := twoRegionFile(`"stale one"`, `"stale two"`)
	want := twoRegionFile(`"fresh one"`, `"fresh two"`)

	firstThenSecond, err := ledgerRegion().Replace(src, block(ledgerRegion(), `"fresh one"`))
	require.NoError(t, err)

	firstThenSecond, err = auditRegion().Replace(firstThenSecond, block(auditRegion(), `"fresh two"`))
	require.NoError(t, err)

	secondThenFirst, err := auditRegion().Replace(src, block(auditRegion(), `"fresh two"`))
	require.NoError(t, err)

	secondThenFirst, err = ledgerRegion().Replace(secondThenFirst, block(ledgerRegion(), `"fresh one"`))
	require.NoError(t, err)

	require.Equal(t, want, firstThenSecond)
	require.Equal(t, want, secondThenFirst)
}

// TestRegion_Replace_IsIdempotent is what lets a drift test be "generating again changes nothing" and
// lets `make gen` be safe to run at any time.
func TestRegion_Replace_IsIdempotent(t *testing.T) {
	t.Parallel()

	current := block(ledgerRegion(), `"fresh one"`)
	src := twoRegionFile(`"fresh one"`, `"stale two"`)

	once, err := ledgerRegion().Replace(src, current)
	require.NoError(t, err)
	require.Equal(t, src, once)

	twice, err := ledgerRegion().Replace(once, current)
	require.NoError(t, err)
	require.Equal(t, src, twice)
}

// TestRegion_Replace_MissingMarkers_IsAnError covers every way the markers can fail to delimit
// exactly one region. A generator that cannot find its target and returns the file unchanged is the
// dangerous answer: every gate downstream reports success while the CHECK stays frozen.
func TestRegion_Replace_MissingMarkers_IsAnError(t *testing.T) {
	t.Parallel()

	r := ledgerRegion()

	tests := []struct {
		name string
		src  string
	}{
		{name: "no markers at all", src: "table \"one\" {\n}\n"},
		{name: "begin only", src: r.Begin + "\n"},
		{name: "end only", src: r.End + "\n"},
		{name: "begin twice", src: r.Begin + "\n" + r.Begin + "\n" + r.End + "\n"},
		{name: "end twice", src: r.Begin + "\n" + r.End + "\n" + r.End + "\n"},
		{name: "end before begin", src: r.End + "\n" + r.Begin + "\n"},
		{name: "another region's markers only", src: onlyRegion(auditRegion())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := r.Replace(tt.src, block(r, `"fresh"`))
			require.ErrorIs(t, err, schemaenum.ErrMarkersMissing)
			require.Empty(t, out, "a failed replace must not return a half-written schema")
		})
	}
}

// onlyRegion is a file carrying one region and not the other — the shape that must make the absent
// catalogue's render refuse rather than quietly succeed.
func onlyRegion(r schemaenum.Region) string {
	return "table \"two\" {\n" + block(r, `"stale"`) + "\n}\n"
}

// TestRegion_Replace_MarkerMatchIsWholeLine proves the match is not a substring one. A comment that
// merely mentions the marker — this repository documents its own markers in .claude/rules and in
// AGENTS.md, and somebody will eventually quote one into the schema — must not be mistaken for the
// marker itself, or the region would swallow every line between the mention and the real end marker.
func TestRegion_Replace_MarkerMatchIsWholeLine(t *testing.T) {
	t.Parallel()

	r := ledgerRegion()

	src := strings.Join([]string{
		"// see " + strings.TrimSpace(r.Begin) + " below, which is generated",
		"table \"one\" {",
		block(r, `"stale"`),
		"}",
		"",
	}, "\n")

	out, err := r.Replace(src, block(r, `"fresh"`))
	require.NoError(t, err, "a line mentioning the marker was mistaken for a second marker")

	require.Contains(t, out, `"fresh"`)
	require.Contains(t, out, "// see "+strings.TrimSpace(r.Begin)+" below, which is generated",
		"the mentioning line was rewritten")
}
