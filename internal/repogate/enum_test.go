package repogate

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/require"
)

// scanSchema runs ENUM001 over a schema body with the given declared markers, and returns what it
// reported.
//
// Calling the rule directly is the point of this file. test/repo's fixture drives the same logic
// through `bash scripts/repo-gates.sh` against a t.TempDir() tree — one subprocess and one
// filesystem per case — which is why the shell version had ONE fixture carrying sixteen interleaved
// cases rather than sixteen tests. Both are worth having: that one proves the gate fires end to
// end, these prove each case on its own, and a failure here names the case instead of the tree.
//
// The cases below are the ones the awk scanner was specified by (issue #116), and they are the
// specification of the HCL parse that replaced it: what a rule DECIDES is not allowed to change
// when how it decides does.
func scanSchema(t *testing.T, body string, declared ...string) []string {
	t.Helper()

	markers := make(map[string]bool, len(declared))
	for _, marker := range declared {
		markers[marker] = true
	}

	return scanEnums(strings.Split(strings.TrimSuffix(body, "\n"), "\n"), markers)
}

// TestEnumScan_Vocabularies_AreReportedAndBooleansAreNot walks every shape the rule has to tell
// apart, one case at a time.
//
// The must-NOT-fire half is the larger one and it is what keeps the rule usable: a generated
// region, a shape CHECK that merely quotes a value, a boolean `IN (0, 1)`, an index predicate and
// the file's own prose all have to stay quiet, or the first author to hit a false positive reaches
// for --no-verify rather than for the rule id.
func TestEnumScan_Vocabularies_AreReportedAndBooleansAreNot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		expr string
		want bool
		why  string
	}{
		{
			name: "the plain rendered form",
			expr: `expr = "state IN ('draft', 'open')"`,
			want: true,
			why:  "CheckExpr's `x IN (…)` is what a catalogue renders",
		},
		{
			name: "the nullable rendered form",
			expr: `expr = "mode IS NULL OR mode IN ('auction_open')"`,
			want: true,
			why:  "NullableCheckExpr is the second shape internal/schemaenum renders, and account.system_key uses it",
		},
		{
			name: "double-quoted values",
			expr: `expr = "visibility IN (\"blind\", \"open\")"`,
			want: true,
			why: "SQLite treats a double-quoted token that resolves to no column as a string literal, " +
				"so changing quote style must not be a way past the rule",
		},
		{
			name: "a lowercase keyword",
			expr: `expr = "tier in ('main', 'alt')"`,
			want: true,
			why:  "SQL keywords are case-insensitive; the generator's uppercase is a convention, not a rule",
		},
		{
			name: "a block comment between the keyword and its parenthesis",
			expr: `expr = "source IN /* the vocabulary */ ('web', 'discord')"`,
			want: true,
			why:  "a comment does not change what the CHECK says, so it must not change what the rule sees",
		},
		{
			name: "a boolean",
			expr: `expr = "blind IN (0, 1)"`,
			want: false,
			why:  "no catalogue could generate a boolean",
		},
		{
			name: "a boolean whose SQL comment quotes two enum values",
			expr: "expr = \"retry IN (0, 1) -- never 'draft', never 'open'\"",
			want: false,
			why: "comments are STRIPPED, not merely tolerated — otherwise every documented CHECK " +
				"reads as a vocabulary",
		},
		{
			name: "a boolean whose HCL comment quotes two enum values",
			expr: "expr = \"retry IN (0, 1)\" // never 'draft', never 'open'",
			want: false,
			why: "the same case one level out: a comment ABOUT the expression is not part of it, and " +
				"the parse is what makes that free rather than a shape to model",
		},
		{
			name: "a shape check that quotes one value",
			expr: `expr = "((kind = 'person') = (person_id IS NOT NULL))"`,
			want: false,
			why:  "a comparison is not a vocabulary: there is no IN list at all",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hits := scanSchema(t, "table \"t\" {\n  check \"c\" {\n    "+tc.expr+"\n  }\n}\n")

			if tc.want {
				require.Len(t, hits, 1, "%s\n%v", tc.why, hits)
				require.Contains(t, hits[0], `check "c"`, "the failure must name the check a waiver would key to")

				return
			}

			require.Empty(t, hits, "%s", tc.why)
		})
	}
}

// TestEnumScan_ListAcrossLines_IsOneExpression is the property a line-scoped scan cannot have.
//
// A wrapped or heredoc expression puts `IN (` on one line and the values on the next, so a
// line-scoped scan sees no quote on the first and no `IN (` on the rest — which would make the
// LONGEST vocabularies, the ones most worth generating, the ones that walk through. The boolean
// case is the other half: carrying list state across lines must not turn every multi-line CHECK
// into a hit.
func TestEnumScan_ListAcrossLines_IsOneExpression(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		expr string
		want bool
	}{
		{
			name: "a heredoc vocabulary",
			expr: "expr = <<-SQL\n      phase IN (\n        'pull',\n        'looting'\n      )\n    SQL",
			want: true,
		},
		{
			name: "the keyword and its parenthesis split across the break",
			expr: "expr = <<-SQL\n      lockout IN\n      (\n        'none',\n        'shared'\n      )\n    SQL",
			want: true,
		},
		{
			name: "a comment-only line between the keyword and the list",
			expr: "expr = <<-SQL\n      outcome IN\n      -- these four are the resolution ladder\n      ('won', 'void')\n    SQL",
			want: true,
		},
		{
			name: "a heredoc boolean",
			expr: "expr = <<-SQL\n      flags IN (\n        0,\n        1\n      )\n    SQL",
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hits := scanSchema(t, "table \"t\" {\n  check \"c\" {\n    "+tc.expr+"\n  }\n}\n")

			require.Equal(t, tc.want, len(hits) == 1, "%v", hits)
		})
	}
}

// TestEnumScan_GeneratedRegion_IsOneACatalogueOwns closes the self-service exemption, and it is the
// bypass that matters most: everything else the rule does is undone by two comment lines without
// it.
//
// The markers are comments. Nothing stops an author wrapping a brand-new literal in a balanced
// BEGIN/END pair — and nothing downstream notices either, because `make gen` rewrites only the
// regions its catalogues declare, so a fabricated one is invisible to `make verify-generated` as
// well. The region would be "generated" in the sense that nothing generates it.
func TestEnumScan_GeneratedRegion_IsOneACatalogueOwns(t *testing.T) {
	t.Parallel()

	const (
		begin = "  // BEGIN GENERATED — account enum CHECKs, from internal/account/kinds. Run make gen."
		end   = "  // END GENERATED — account enum CHECKs."
	)

	body := "table \"account\" {\n" + begin + "\n" +
		"  check \"account_kind_enum\" {\n    expr = \"kind IN ('person', 'system')\"\n  }\n" + end + "\n}\n" +
		"\ntable \"bid_session\" {\n" +
		"  // BEGIN GENERATED — bid_session enum CHECKs, from internal/bids/kinds. Run make gen.\n" +
		"  check \"bid_session_state_enum\" {\n    expr = \"state IN ('draft', 'open')\"\n  }\n" +
		"  // END GENERATED — bid_session enum CHECKs.\n}\n"

	hits := strings.Join(scanSchema(t, body, begin, end), "\n")

	require.Contains(t, hits, "no Go catalogue declares",
		"the marker itself must be reported: a region nothing generates is a claim, not a fact, and "+
			"saying only \"hand-written CHECK\" would leave the author re-adding the markers more "+
			"carefully\n%s", hits)
	require.Contains(t, hits, "bid_session_state_enum",
		"the smuggled CHECK must be reported too — the fabricated markers exempt nothing\n%s", hits)
	require.NotContains(t, hits, "account_kind_enum",
		"the region the catalogue DOES declare must still be exempt; a scanner that stopped "+
			"honouring generated regions would satisfy every assertion above\n%s", hits)
}

// TestEnumScan_UnclosedRegion_IsAViolation closes the bypass that would otherwise make the rule
// self-disabling: region state is line-ordered, so a BEGIN with no matching END exempts the entire
// rest of the file from one unbalanced comment line.
func TestEnumScan_UnclosedRegion_IsAViolation(t *testing.T) {
	t.Parallel()

	const begin = "  // BEGIN GENERATED — account enum CHECKs, from internal/account/kinds. Run make gen."

	hits := strings.Join(scanSchema(t, "table \"t\" {\n"+begin+
		"\n  check \"c\" {\n    expr = \"state IN ('draft')\"\n  }\n}\n", begin), "\n")

	require.Contains(t, hits, "unclosed BEGIN GENERATED", "%s", hits)
	require.NotContains(t, hits, "no Go catalogue declares",
		"the marker IS declared — a hit here means this test is proving the wrong thing\n%s", hits)
}

// TestEnumScan_Waiver_NeedsAReason pins the property that makes the escape hatch worth having.
//
// `// dkp:enum-literal` with nothing after it is the box ticked without the thought — the same
// defect as a bare `adr: n/a`, and the same answer: the reason is the artefact, the marker is only
// its carrier. A waiver that costs one token is a waiver that gets pasted onto the next literal.
func TestEnumScan_Waiver_NeedsAReason(t *testing.T) {
	t.Parallel()

	check := "  check \"bid_tier_enum\" {\n    expr = \"tier IN ('main', 'alt')\"\n  }\n"

	withReason := scanSchema(t, "table \"t\" {\n"+
		"  // dkp:enum-literal — a vendor vocabulary the importer reads, not a DKP catalogue.\n"+check+"}\n")
	require.Empty(t, withReason, "a waiver WITH a reason is the documented exception and must be honoured")

	bare := strings.Join(scanSchema(t, "table \"t\" {\n  // dkp:enum-literal\n"+check+"}\n"), "\n")
	require.Contains(t, bare, "no reason",
		"the rule must say the waiver lacks a reason, not merely that a literal exists\n%s", bare)
	require.Contains(t, bare, "bid_tier_enum",
		"the unexempted check must still be reported — canonical §5 makes bid.tier's declaration "+
			"ORDER semantic, so this is the literal that costs the most\n%s", bare)

	// The waiver reaches the block below it, not the rest of the file.
	reach := strings.Join(scanSchema(t, "table \"t\" {\n"+
		"  // dkp:enum-literal — a vendor vocabulary.\n"+check+"\n"+
		"  check \"second_enum\" {\n    expr = \"state IN ('draft')\"\n  }\n}\n"), "\n")
	require.Contains(t, reach, "second_enum",
		"a waiver that outlived its own check block would exempt everything after it\n%s", reach)
	require.NotContains(t, reach, "bid_tier_enum", "%s", reach)
}

// TestEnumScan_UnparseableSchema_IsAFinding is the fail-closed half of the parse (issue #116), and
// the second case is the one a fixture cannot reach.
//
// ADR-0018 rejected an HCL parse partly because "a gate that reports could not parse is a gate that
// gets bypassed". The answer is that it does not report a PASS either — so the branch has to produce
// a finding, and it has to survive a diagnostic with no Subject.
//
// `hcl.Diagnostic.Subject` is a POINTER, and hcl leaves it nil for a diagnostic about the file as a
// whole rather than about a position in it. Reading through it unguarded is a panic on exactly the
// input this branch exists to handle, and a gate that panics tells a CI log the same thing as a gate
// that passes: no rule id, no line, nothing to fix. A parser diagnostic normally carries a subject,
// which is why the malformed-schema fixture cannot exercise this and the constructed one can.
func TestEnumScan_UnparseableSchema_IsAFinding(t *testing.T) {
	t.Parallel()

	hits := scanSchema(t, "table \"t\" {\n  check \"c\" {\n    expr = \n  }\n}\n")

	require.Len(t, hits, 1, "%v", hits)
	require.Contains(t, hits[0], "did not run",
		"the finding must say the SCAN did not run: \"hand-written enum CHECK\" would send the reader "+
			"looking for a literal that may not be there\n%s", hits[0])

	require.Equal(t, 1, diagLine(&hcl.Diagnostic{Summary: "a whole-file complaint", Detail: "no subject"}),
		"a diagnostic with no Subject must report line 1, not panic")
	require.Equal(t, 7, diagLine(&hcl.Diagnostic{Subject: &hcl.Range{Start: hcl.Pos{Line: 7}}}),
		"a diagnostic WITH a subject must still name its line — a fallback that swallowed every "+
			"position would make the finding unactionable")
}

// TestEnumScan_IndexPredicate_IsNotACheck is #97: a partial index over a SUBSET of a vocabulary is
// not the vocabulary, so it cannot be rendered from a catalogue as-is and a rule that demanded it
// would fire on correct work.
func TestEnumScan_IndexPredicate_IsNotACheck(t *testing.T) {
	t.Parallel()

	hits := scanSchema(t, "table \"t\" {\n  index \"ux_live\" {\n"+
		"    where   = \"state IN ('open', 'extended')\"\n"+
		"    columns = [column.item_instance_id]\n  }\n}\n")

	require.Empty(t, hits, "%v", hits)
}
